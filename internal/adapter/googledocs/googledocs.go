package googledocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/store"
)

var _ adapter.Adapter = (*Adapter)(nil)

type Config struct {
	ServiceAccountKeyPath  string
	ListenAddr             string
	WebhookURL             string
	DocumentContentMaxSize string
	Store                  store.Store
}

type Adapter struct {
	driveService   *drive.Service
	botEmail       string
	store          store.Store
	handler        adapter.EventHandler
	listenAddr     string
	webhookURL     string
	server         *http.Server
	contentMaxSize int64
	channelToken   string
	mu             sync.Mutex
	pageToken      string
	channelID      string
	resourceID     string
}

func New(cfg Config) (*Adapter, error) {
	ctx := context.Background()
	driveService, err := drive.NewService(ctx, option.WithCredentialsFile(cfg.ServiceAccountKeyPath))
	if err != nil {
		return nil, fmt.Errorf("creating drive service: %w", err)
	}

	email, err := extractServiceAccountEmail(cfg.ServiceAccountKeyPath)
	if err != nil {
		return nil, fmt.Errorf("extracting service account email: %w", err)
	}

	maxSize, err := parseContentMaxSize(cfg.DocumentContentMaxSize)
	if err != nil {
		return nil, fmt.Errorf("parsing documentContentMaxSize: %w", err)
	}

	return &Adapter{
		driveService:   driveService,
		botEmail:       email,
		store:          cfg.Store,
		listenAddr:     cfg.ListenAddr,
		webhookURL:     cfg.WebhookURL,
		contentMaxSize: maxSize,
		channelToken:   uuid.New().String(),
	}, nil
}

func (a *Adapter) Name() string {
	return "googledocs"
}

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	a.handler = handler

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/googledocs", func(w http.ResponseWriter, r *http.Request) {
		a.handleNotification(r.Context(), w, r)
	})

	a.server = &http.Server{
		Addr:    a.listenAddr,
		Handler: mux,
	}

	if err := a.registerWatch(ctx); err != nil {
		slog.Warn("failed to register drive watch, will retry on next notification", "error", err)
	}

	go func() {
		<-ctx.Done()
		a.stopWatch()
		if err := a.server.Close(); err != nil {
			slog.Error("error closing googledocs webhook server", "error", err)
		}
	}()

	if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("googledocs webhook server error: %w", err)
	}

	return nil
}

func (a *Adapter) registerWatch(ctx context.Context) error {
	if a.webhookURL == "" {
		return fmt.Errorf("webhookUrl is required for Drive push notifications")
	}

	startToken, err := a.driveService.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("getting start page token: %w", err)
	}
	a.pageToken = startToken.StartPageToken

	channelID := uuid.New().String()

	channel, err := a.driveService.Changes.Watch(a.pageToken, &drive.Channel{
		Id:      channelID,
		Type:    "web_hook",
		Address: a.webhookURL,
		Token:   a.channelToken,
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("registering drive watch: %w", err)
	}

	a.channelID = channel.Id
	a.resourceID = channel.ResourceId
	slog.Info("registered drive push notifications", "channel_id", a.channelID, "expiration", channel.Expiration)

	return nil
}

func (a *Adapter) stopWatch() {
	if a.channelID == "" {
		return
	}
	err := a.driveService.Channels.Stop(&drive.Channel{
		Id:         a.channelID,
		ResourceId: a.resourceID,
	}).Do()
	if err != nil {
		slog.Warn("failed to stop drive watch channel", "error", err)
	}
}

func (a *Adapter) handleNotification(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Goog-Channel-Token")
	if token != a.channelToken {
		http.Error(w, "invalid channel token", http.StatusForbidden)
		return
	}

	state := r.Header.Get("X-Goog-Resource-State")
	if state == "sync" {
		w.WriteHeader(http.StatusOK)
		return
	}

	go func() {
		if err := a.processChanges(context.Background()); err != nil {
			slog.Error("error processing drive changes", "error", err)
		}
	}()

	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) processChanges(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.pageToken == "" {
		return nil
	}

	changeList, err := a.driveService.Changes.List(a.pageToken).
		Fields("nextPageToken", "newStartPageToken", "changes(fileId, file(mimeType))").
		Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("listing drive changes: %w", err)
	}

	for _, change := range changeList.Changes {
		if change.File == nil || change.File.MimeType != "application/vnd.google-apps.document" {
			continue
		}
		if err := a.checkDocumentComments(ctx, change.FileId); err != nil {
			slog.Error("error checking document comments", "file_id", change.FileId, "error", err)
		}
	}

	if changeList.NewStartPageToken != "" {
		a.pageToken = changeList.NewStartPageToken
	} else if changeList.NextPageToken != "" {
		a.pageToken = changeList.NextPageToken
	}

	return nil
}

func (a *Adapter) checkDocumentComments(ctx context.Context, fileID string) error {
	lastSeen, err := a.store.GetAdapterState(ctx, "googledocs", fileID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("getting last seen timestamp: %w", err)
	}

	commentsCall := a.driveService.Comments.List(fileID).
		Fields("comments(id, content, resolved, author(emailAddress), replies(content, author(emailAddress)), anchor, createdTime, modifiedTime, htmlContent)").
		IncludeDeleted(false).
		Context(ctx)

	if lastSeen != "" {
		commentsCall = commentsCall.StartModifiedTime(lastSeen)
	}

	commentList, err := commentsCall.Do()
	if err != nil {
		return fmt.Errorf("listing comments: %w", err)
	}

	var latestModified string
	var events []domain.IncomingEvent

	for _, comment := range commentList.Comments {
		if comment.Resolved {
			continue
		}
		if !a.isRelevantComment(comment) {
			continue
		}

		content := extractContent(comment)
		if content == "" {
			continue
		}

		if comment.ModifiedTime > latestModified {
			latestModified = comment.ModifiedTime
		}

		docContext, err := a.fetchDocumentContext(ctx, fileID)
		if err != nil {
			slog.Warn("failed to fetch document context", "file_id", fileID, "error", err)
		}

		var userID string
		if comment.Author != nil {
			userID = comment.Author.EmailAddress
		}

		ref := NewRef(fileID, comment.Id)
		event := domain.IncomingEvent{
			ChannelRef: ref,
			ThreadID:   fmt.Sprintf("googledocs-%s-%s", fileID, comment.Id),
			UserID:     userID,
			Content:    content,
			Metadata: map[string]string{
				"document_id":      fileID,
				"comment_id":       comment.Id,
				"document_context": docContext,
			},
		}
		events = append(events, event)
	}

	if latestModified != "" {
		t, err := time.Parse(time.RFC3339, latestModified)
		if err == nil {
			newLastSeen := t.Add(time.Millisecond).Format(time.RFC3339Nano)
			if err := a.store.SetAdapterState(ctx, "googledocs", fileID, newLastSeen); err != nil {
				slog.Error("failed to update last seen timestamp", "file_id", fileID, "error", err)
			}
		}
	}

	if len(events) > 0 {
		if err := a.handler(ctx, events); err != nil {
			return fmt.Errorf("handling events: %w", err)
		}
	}

	return nil
}

func (a *Adapter) isRelevantComment(comment *drive.Comment) bool {
	if len(comment.Replies) > 0 {
		lastReply := comment.Replies[len(comment.Replies)-1]
		if lastReply.Author != nil && lastReply.Author.EmailAddress == a.botEmail {
			return false
		}
	}

	if comment.Author != nil && comment.Author.EmailAddress == a.botEmail {
		return false
	}

	if strings.Contains(comment.Content, a.botEmail) {
		return true
	}

	for _, reply := range comment.Replies {
		if strings.Contains(reply.Content, a.botEmail) {
			return true
		}
	}

	return isActionItemAssignedTo(comment, a.botEmail)
}

func isActionItemAssignedTo(comment *drive.Comment, email string) bool {
	if comment.HtmlContent == "" {
		return false
	}
	return strings.Contains(comment.HtmlContent, email) &&
		strings.Contains(comment.HtmlContent, "action_item")
}

func extractContent(comment *drive.Comment) string {
	if len(comment.Replies) > 0 {
		lastReply := comment.Replies[len(comment.Replies)-1]
		if lastReply.Author != nil {
			return strings.TrimSpace(lastReply.Content)
		}
	}
	return strings.TrimSpace(comment.Content)
}

func (a *Adapter) fetchDocumentContext(ctx context.Context, fileID string) (string, error) {
	resp, err := a.driveService.Files.Export(fileID, "text/plain").Context(ctx).Download()
	if err != nil {
		return "", fmt.Errorf("exporting document: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading document body: %w", err)
	}

	docText := string(body)

	commentList, err := a.driveService.Comments.List(fileID).
		Fields("comments(id, content, resolved, author(displayName), replies(content, author(displayName)))").
		IncludeDeleted(false).
		Context(ctx).Do()
	if err != nil {
		return docText, nil
	}

	var sb strings.Builder
	sb.WriteString(docText)
	sb.WriteString("\n\n--- Document Comments ---\n")
	for _, c := range commentList.Comments {
		if c.Resolved {
			continue
		}
		authorName := "Unknown"
		if c.Author != nil {
			authorName = c.Author.DisplayName
		}
		sb.WriteString(fmt.Sprintf("\n[%s]: %s\n", authorName, c.Content))
		for _, r := range c.Replies {
			replyAuthor := "Unknown"
			if r.Author != nil {
				replyAuthor = r.Author.DisplayName
			}
			sb.WriteString(fmt.Sprintf("  [%s]: %s\n", replyAuthor, r.Content))
		}
	}

	result := sb.String()
	return truncateContent(result, a.contentMaxSize), nil
}

func truncateContent(content string, maxSize int64) string {
	if maxSize == 0 || int64(len(content)) <= maxSize {
		return content
	}
	return content[:maxSize] + "\n\n[Content truncated due to size limit]"
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	g := ParseRef(ref)

	_, err := a.driveService.Replies.Create(g.DocumentID, g.CommentID, &drive.Reply{
		Content: message,
		Action:  "reopen",
	}).Fields("id").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to create reply: %w", err)
	}

	return nil
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	g := ParseRef(ref)

	_, err := a.driveService.Replies.Create(g.DocumentID, g.CommentID, &drive.Reply{
		Content: "Looking into this...",
		Action:  "reopen",
	}).Fields("id").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to acknowledge comment: %w", err)
	}

	return nil
}

func extractServiceAccountEmail(keyPath string) (string, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("reading key file: %w", err)
	}

	var keyFile struct {
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal(data, &keyFile); err != nil {
		return "", fmt.Errorf("parsing key file: %w", err)
	}
	if keyFile.ClientEmail == "" {
		return "", fmt.Errorf("client_email not found in key file")
	}

	return keyFile.ClientEmail, nil
}

func parseContentMaxSize(s string) (int64, error) {
	if s == "0" {
		return 0, nil
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size value")
	}

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	default:
		return 0, fmt.Errorf("unsupported size unit in %q (use KB or MB)", s)
	}

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing size value %q: %w", s, err)
	}

	return val * multiplier, nil
}
