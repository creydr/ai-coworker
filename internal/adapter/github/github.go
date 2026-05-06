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
	"github.com/creydr/ai-coworker/internal/vcs"
	vcsgithub "github.com/creydr/ai-coworker/internal/vcs/github"
)

// Adapter implements the adapter interface for GitHub using webhooks and the GitHub API
type Adapter struct {
	appsTransport       *ghinstallation.AppsTransport
	vcsProvider         *vcsgithub.Provider
	installationClients sync.Map // maps int64 installationID → *gh.Client
	webhookSecret       []byte
	botUsername         string
	listenAddr          string
	server              *http.Server
	allowedUsers        map[string]bool
	allowAll            bool
}

// New creates a new GitHub adapter with the given app credentials and configuration.
// allowedUsers controls who can trigger the bot:
//   - empty list: nobody is allowed (secure by default)
//   - list containing "*": all users are allowed
//   - list of usernames: only those users are allowed
func New(appID int64, privateKeyPEM []byte, webhookSecret, botUsername, listenAddr string, allowedUsers []string) (*Adapter, error) {
	atr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub Apps transport: %w", err)
	}

	allowAll := false
	users := make(map[string]bool, len(allowedUsers))
	for _, u := range allowedUsers {
		if u == "*" {
			allowAll = true
			break
		}
		users[u] = true
	}

	return &Adapter{
		appsTransport: atr,
		vcsProvider:   vcsgithub.New(atr),
		webhookSecret: []byte(webhookSecret),
		botUsername:   botUsername,
		listenAddr:    listenAddr,
		allowedUsers:  users,
		allowAll:      allowAll,
	}, nil
}

func (a *Adapter) getInstallationClient(installationID int64) *gh.Client {
	itr := ghinstallation.NewFromAppsTransport(a.appsTransport, installationID)
	client := gh.NewClient(&http.Client{Transport: itr})
	v, _ := a.installationClients.LoadOrStore(installationID, client)
	return v.(*gh.Client)
}

func (a *Adapter) getClientForRepo(ctx context.Context, repo string) (*gh.Client, error) {
	installationID, err := a.vcsProvider.GetInstallationID(ctx, repo)
	if err != nil {
		return nil, err
	}
	return a.getInstallationClient(installationID), nil
}

func (a *Adapter) isUserAllowed(username string) bool {
	if a.allowAll {
		return true
	}
	return a.allowedUsers[username]
}

func (a *Adapter) denyUnauthorized(ctx context.Context, installationID int64, repoFullName string, issueNum int, body string) {
	client := a.getInstallationClient(installationID)
	owner, repo, err := vcsgithub.SplitRepo(repoFullName)
	if err != nil {
		slog.Error("error splitting repo for denial response", "error", err)
		return
	}

	msg := fmt.Sprintf("Sorry, you don't have permission to interact with me.\n\n> %s", strings.ReplaceAll(body, "\n", "\n> "))
	comment := &gh.IssueComment{Body: gh.Ptr(msg)}
	if _, _, err := client.Issues.CreateComment(ctx, owner, repo, issueNum, comment); err != nil {
		slog.Error("error sending denial response", "error", err)
	}
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
		Addr:    a.listenAddr,
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
		// Cache the installation ID from the webhook so the VCS provider can
		// create scoped tokens later without an extra API round-trip.
		a.vcsProvider.TrackInstallation(repoFullName, installationID)

		if err := a.handleIssueComment(ctx, e, installationID, handler); err != nil {
			slog.Error("error handling issue comment event", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewCommentEvent:
		installationID := e.GetInstallation().GetID()
		repoFullName := e.GetRepo().GetFullName()
		a.vcsProvider.TrackInstallation(repoFullName, installationID)

		if err := a.handlePRReviewComment(ctx, e, installationID, handler); err != nil {
			slog.Error("error handling PR review comment event", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewEvent:
		installationID := e.GetInstallation().GetID()
		repoFullName := e.GetRepo().GetFullName()
		a.vcsProvider.TrackInstallation(repoFullName, installationID)

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

func (a *Adapter) isBotUser(login string) bool {
	return login == a.botUsername || login == a.botUsername+"[bot]"
}

func (a *Adapter) handleIssueComment(ctx context.Context, e *gh.IssueCommentEvent, installationID int64, handler adapter.EventHandler) error {
	if e.GetAction() != "created" {
		return nil
	}

	body := e.GetComment().GetBody()
	mention := "@" + a.botUsername
	mentioned := strings.Contains(body, mention)
	isBotPR := e.GetIssue().IsPullRequest() && a.isBotUser(e.GetIssue().GetUser().GetLogin())

	if !mentioned && !isBotPR {
		return nil
	}

	userLogin := e.GetComment().GetUser().GetLogin()
	if a.isBotUser(userLogin) {
		slog.Debug("ignoring own comment", "user", userLogin, "event", "issue_comment")
		return nil
	}
	if !a.isUserAllowed(userLogin) {
		slog.Info("unauthorized user", "user", userLogin, "event", "issue_comment")
		repoFullName := e.GetRepo().GetFullName()
		a.denyUnauthorized(ctx, installationID, repoFullName, e.GetIssue().GetNumber(), body)
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	issueNum := e.GetIssue().GetNumber()
	isPR := e.GetIssue().IsPullRequest()

	ref := WithComment(NewRef(repoFullName, issueNum), e.GetComment().GetID(), "issue_comment")

	incoming := domain.IncomingEvent{
		ChannelRef: ref,
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, issueNum),
		UserID:     userLogin,
		Content:    content,
		Metadata: map[string]string{
			"vcs":             a.Name(),
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
	mentioned := strings.Contains(body, mention)
	isBotPR := a.isBotUser(e.GetPullRequest().GetUser().GetLogin())

	if !mentioned && !isBotPR {
		return nil
	}

	userLogin := e.GetComment().GetUser().GetLogin()
	if a.isBotUser(userLogin) {
		slog.Debug("ignoring own comment", "user", userLogin, "event", "pr_review_comment")
		return nil
	}
	if !a.isUserAllowed(userLogin) {
		slog.Info("unauthorized user", "user", userLogin, "event", "pr_review_comment")
		repoFullName := e.GetRepo().GetFullName()
		a.denyUnauthorized(ctx, installationID, repoFullName, e.GetPullRequest().GetNumber(), body)
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	ref := WithComment(NewRef(repoFullName, prNum), e.GetComment().GetID(), "review_comment")

	incoming := domain.IncomingEvent{
		ChannelRef: ref,
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:     userLogin,
		Content:    content,
		Metadata: map[string]string{
			"vcs":             a.Name(),
			"type":            "review_comment",
			"repo":            repoFullName,
			"issue_num":       strconv.Itoa(prNum),
			"is_pr":           "true",
			"pr_branch":       e.GetPullRequest().GetHead().GetRef(),
			"path":            e.GetComment().GetPath(),
			"comment_id":      strconv.FormatInt(e.GetComment().GetID(), 10),
			"review_id":       strconv.FormatInt(e.GetComment().GetPullRequestReviewID(), 10),
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
	if strings.TrimSpace(body) == "" {
		return nil
	}

	mention := "@" + a.botUsername
	mentioned := strings.Contains(body, mention)
	isBotPR := a.isBotUser(e.GetPullRequest().GetUser().GetLogin())

	if !mentioned && !isBotPR {
		return nil
	}

	userLogin := e.GetReview().GetUser().GetLogin()
	if a.isBotUser(userLogin) {
		slog.Debug("ignoring own comment", "user", userLogin, "event", "pr_review")
		return nil
	}
	if !a.isUserAllowed(userLogin) {
		slog.Info("unauthorized user", "user", userLogin, "event", "pr_review")
		repoFullName := e.GetRepo().GetFullName()
		a.denyUnauthorized(ctx, installationID, repoFullName, e.GetPullRequest().GetNumber(), body)
		return nil
	}

	content := strings.TrimSpace(strings.ReplaceAll(body, mention, ""))

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		ChannelRef: NewRef(repoFullName, prNum),
		ThreadID:   fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:     userLogin,
		Content:    content,
		Metadata: map[string]string{
			"vcs":             a.Name(),
			"type":            "review",
			"repo":            repoFullName,
			"issue_num":       strconv.Itoa(prNum),
			"is_pr":           "true",
			"pr_branch":       e.GetPullRequest().GetHead().GetRef(),
			"review_state":    e.GetReview().GetState(),
			"review_id":       strconv.FormatInt(e.GetReview().GetID(), 10),
			"installation_id": strconv.FormatInt(installationID, 10),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	g := ParseRef(ref)
	owner, repo, err := vcsgithub.SplitRepo(g.Repo)
	if err != nil {
		return err
	}

	client, err := a.getClientForRepo(ctx, g.Repo)
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

	owner, repo, err := vcsgithub.SplitRepo(g.Repo)
	if err != nil {
		return err
	}

	client, err := a.getClientForRepo(ctx, g.Repo)
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

// VCSProvider returns the VCS provider for GitHub, for registration in the VCS registry.
func (a *Adapter) VCSProvider() vcs.Provider {
	return a.vcsProvider
}

// Ensure Adapter implements adapter.Adapter at compile time.
var _ adapter.Adapter = (*Adapter)(nil)
