package github

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v68/github"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
)

type Adapter struct {
	client        *gh.Client
	webhookSecret []byte
	botUsername   string
	server        *http.Server
}

func New(webhookSecret, botUsername string) *Adapter {
	return &Adapter{
		client:        gh.NewClient(nil),
		webhookSecret: []byte(webhookSecret),
		botUsername:   botUsername,
	}
}

func (a *Adapter) WithClient(client *gh.Client) *Adapter {
	a.client = client
	return a
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
			log.Printf("error closing github webhook server: %v", err)
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
		if err := a.handleIssueComment(ctx, e, handler); err != nil {
			log.Printf("error handling issue comment event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewCommentEvent:
		if err := a.handlePRReviewComment(ctx, e, handler); err != nil {
			log.Printf("error handling PR review comment event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	case *gh.PullRequestReviewEvent:
		if err := a.handlePRReview(ctx, e, handler); err != nil {
			log.Printf("error handling PR review event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

	default:
		// Ignore unhandled event types
	}

	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) handleIssueComment(ctx context.Context, e *gh.IssueCommentEvent, handler adapter.EventHandler) error {
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

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			Repo:      repoFullName,
			IssueNum:  issueNum,
			CommentID: e.GetComment().GetID(),
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repoFullName, issueNum),
		UserID:   e.GetComment().GetUser().GetLogin(),
		Content:  content,
		Metadata: map[string]string{
			"type":      "issue_comment",
			"repo":      repoFullName,
			"issue_num": strconv.Itoa(issueNum),
			"is_pr":     strconv.FormatBool(isPR),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) handlePRReviewComment(ctx context.Context, e *gh.PullRequestReviewCommentEvent, handler adapter.EventHandler) error {
	if e.GetAction() != "created" {
		return nil
	}

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			Repo:      repoFullName,
			IssueNum:  prNum,
			CommentID: e.GetComment().GetID(),
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:   e.GetComment().GetUser().GetLogin(),
		Content:  e.GetComment().GetBody(),
		Metadata: map[string]string{
			"type":      "review_comment",
			"repo":      repoFullName,
			"issue_num": strconv.Itoa(prNum),
			"is_pr":     "true",
			"path":      e.GetComment().GetPath(),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) handlePRReview(ctx context.Context, e *gh.PullRequestReviewEvent, handler adapter.EventHandler) error {
	if e.GetAction() != "submitted" {
		return nil
	}

	repoFullName := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     repoFullName,
			IssueNum: prNum,
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repoFullName, prNum),
		UserID:   e.GetReview().GetUser().GetLogin(),
		Content:  e.GetReview().GetBody(),
		Metadata: map[string]string{
			"type":         "review",
			"repo":         repoFullName,
			"issue_num":    strconv.Itoa(prNum),
			"is_pr":        "true",
			"review_state": e.GetReview().GetState(),
		},
	}

	return handler(ctx, incoming)
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	owner, repo, err := splitRepo(ref.Repo)
	if err != nil {
		return err
	}

	comment := &gh.IssueComment{
		Body: gh.Ptr(message),
	}

	_, _, err = a.client.Issues.CreateComment(ctx, owner, repo, ref.IssueNum, comment)
	if err != nil {
		return fmt.Errorf("failed to create issue comment: %w", err)
	}

	return nil
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	owner, repo, err := splitRepo(ref.Repo)
	if err != nil {
		return err
	}

	_, _, err = a.client.Reactions.CreateIssueCommentReaction(ctx, owner, repo, ref.CommentID, "eyes")
	if err != nil {
		return fmt.Errorf("failed to create reaction: %w", err)
	}

	return nil
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
