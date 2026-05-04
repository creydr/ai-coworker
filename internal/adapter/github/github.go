package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v68/github"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
)

type Adapter struct {
	appsTransport       *ghinstallation.AppsTransport
	installationClients sync.Map // maps int64 installationID → *gh.Client
	repoInstallations   sync.Map // maps string repo → int64 installationID
	webhookSecret       []byte
	botUsername         string
	server              *http.Server
}

func New(appID int64, privateKeyPEM []byte, webhookSecret, botUsername string) (*Adapter, error) {
	atr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub Apps transport: %w", err)
	}

	return &Adapter{
		appsTransport: atr,
		webhookSecret: []byte(webhookSecret),
		botUsername:   botUsername,
	}, nil
}

func (a *Adapter) getInstallationClient(installationID int64) *gh.Client {
	itr := ghinstallation.NewFromAppsTransport(a.appsTransport, installationID)
	client := gh.NewClient(&http.Client{Transport: itr})
	v, _ := a.installationClients.LoadOrStore(installationID, client)
	return v.(*gh.Client)
}

func (a *Adapter) getClientForRepo(repo string) (*gh.Client, error) {
	v, ok := a.repoInstallations.Load(repo)
	if !ok {
		return nil, fmt.Errorf("no installation ID known for repo %q", repo)
	}
	installationID := v.(int64)
	return a.getInstallationClient(installationID), nil
}

func (a *Adapter) Name() string {
	return "github"
}

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})

	a.server = &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		if err := a.server.Close(); err != nil {
			slog.Error("error closing github webhook server", "error", err)
		}
	}()

	if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("github webhook server error: %w", err)
	}

	return nil
}

func (a *Adapter) handleWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request, handler adapter.EventHandler) {
	payload, err := gh.ValidatePayload(r, a.webhookSecret)
	if err != nil {
		http.Error(w, "invalid payload", http.StatusUnauthorized)
		return
	}

	messageType := gh.WebHookType(r)
	event, err := gh.ParseWebHook(messageType, payload)
	if err != nil {
		http.Error(w, "could not parse webhook", http.StatusBadRequest)
		return
	}

	switch e := event.(type) {
	case *gh.IssueCommentEvent:
		installationID := e.GetInstallation().GetID()
		repoFullName := e.GetRepo().GetFullName()
		a.repoInstallations.Store(repoFullName, installationID)

		if err := a.handleIssueComment(ctx, e, installationID, handler); err != nil {
			slog.Error("error handling issue comment event", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewCommentEvent:
		installationID := e.GetInstallation().GetID()
		repoFullName := e.GetRepo().GetFullName()
		a.repoInstallations.Store(repoFullName, installationID)

		if err := a.handlePRReviewComment(ctx, e, installationID, handler); err != nil {
			slog.Error("error handling PR review comment event", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewEvent:
		installationID := e.GetInstallation().GetID()
		repoFullName := e.GetRepo().GetFullName()
		a.repoInstallations.Store(repoFullName, installationID)

		if err := a.handlePRReview(ctx, e, installationID, handler); err != nil {
			slog.Error("error handling PR review event", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	default:
		// Ignore unhandled event types
	}

	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) handleIssueComment(ctx context.Context, e *gh.IssueCommentEvent, installationID int64, handler adapter.EventHandler) error {
	if e.GetAction() != "created" {
		return nil
	}

	body := e.GetComment().GetBody()
	mention := "@" + a.botUsername
	if !strings.Contains(body, mention) {
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	issueNum := e.GetIssue().GetNumber()
	isPR := e.GetIssue().IsPullRequest()

	ref := WithComment(NewRef(repoFullName, issueNum), e.GetComment().GetID(), "issue_comment")

	incoming := domain.IncomingEvent{
		Channel:    "github",
		ChannelRef: ref,
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, issueNum),
		UserID:     e.GetComment().GetUser().GetLogin(),
		Content:    content,
		Metadata: map[string]string{
			"type":            "issue_comment",
			"repo":            repoFullName,
			"issue_num":       strconv.Itoa(issueNum),
			"is_pr":           strconv.FormatBool(isPR),
			"installation_id": strconv.FormatInt(installationID, 10),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) handlePRReviewComment(ctx context.Context, e *gh.PullRequestReviewCommentEvent, installationID int64, handler adapter.EventHandler) error {
	if e.GetAction() != "created" {
		return nil
	}

	body := e.GetComment().GetBody()
	mention := "@" + a.botUsername
	if !strings.Contains(body, mention) {
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	ref := WithComment(NewRef(repoFullName, prNum), e.GetComment().GetID(), "review_comment")

	incoming := domain.IncomingEvent{
		Channel:    "github",
		ChannelRef: ref,
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:     e.GetComment().GetUser().GetLogin(),
		Content:    content,
		Metadata: map[string]string{
			"type":            "review_comment",
			"repo":            repoFullName,
			"issue_num":       strconv.Itoa(prNum),
			"is_pr":           "true",
			"pr_branch":       e.GetPullRequest().GetHead().GetRef(),
			"path":            e.GetComment().GetPath(),
			"comment_id":      strconv.FormatInt(e.GetComment().GetID(), 10),
			"installation_id": strconv.FormatInt(installationID, 10),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) handlePRReview(ctx context.Context, e *gh.PullRequestReviewEvent, installationID int64, handler adapter.EventHandler) error {
	if e.GetAction() != "submitted" {
		return nil
	}

	body := e.GetReview().GetBody()
	mention := "@" + a.botUsername
	if !strings.Contains(body, mention) {
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		Channel:    "github",
		ChannelRef: NewRef(repoFullName, prNum),
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:     e.GetReview().GetUser().GetLogin(),
		Content:    content,
		Metadata: map[string]string{
			"type":            "review",
			"repo":            repoFullName,
			"issue_num":       strconv.Itoa(prNum),
			"is_pr":           "true",
			"pr_branch":       e.GetPullRequest().GetHead().GetRef(),
			"review_state":    e.GetReview().GetState(),
			"installation_id": strconv.FormatInt(installationID, 10),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	g := ParseRef(ref)
	owner, repo, err := splitRepo(g.Repo)
	if err != nil {
		return err
	}

	client, err := a.getClientForRepo(g.Repo)
	if err != nil {
		return err
	}

	if g.CommentType == "review_comment" && g.CommentID != 0 {
		_, _, err = client.PullRequests.CreateCommentInReplyTo(ctx, owner, repo, g.IssueNum, message, g.CommentID)
		if err != nil {
			return fmt.Errorf("failed to reply to review comment: %w", err)
		}
		return nil
	}

	comment := &gh.IssueComment{
		Body: gh.Ptr(message),
	}
	_, _, err = client.Issues.CreateComment(ctx, owner, repo, g.IssueNum, comment)
	if err != nil {
		return fmt.Errorf("failed to create issue comment: %w", err)
	}

	return nil
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	g := ParseRef(ref)
	if g.CommentID == 0 {
		return nil
	}

	owner, repo, err := splitRepo(g.Repo)
	if err != nil {
		return err
	}

	client, err := a.getClientForRepo(g.Repo)
	if err != nil {
		return err
	}

	switch g.CommentType {
	case "review_comment":
		_, _, err = client.Reactions.CreatePullRequestCommentReaction(ctx, owner, repo, g.CommentID, "eyes")
	default:
		_, _, err = client.Reactions.CreateIssueCommentReaction(ctx, owner, repo, g.CommentID, "eyes")
	}
	if err != nil {
		return fmt.Errorf("failed to create reaction: %w", err)
	}

	return nil
}

func (a *Adapter) CreateInstallationToken(ctx context.Context, installationID int64) (string, error) {
	appClient := gh.NewClient(&http.Client{Transport: a.appsTransport})

	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create installation token: %w", err)
	}

	return token.GetToken(), nil
}

func (a *Adapter) CreateInstallationTokenForRepo(ctx context.Context, repo string) (string, error) {
	v, ok := a.repoInstallations.Load(repo)
	if !ok {
		return "", fmt.Errorf("no installation ID known for repo %q", repo)
	}
	return a.CreateInstallationToken(ctx, v.(int64))
}

func splitRepo(fullName string) (owner, repo string, err error) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}

// Ensure Adapter implements adapter.Adapter at compile time.
var _ adapter.Adapter = (*Adapter)(nil)
