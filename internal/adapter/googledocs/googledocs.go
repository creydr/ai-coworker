package googledocs

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
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
	MaxPaginationPages     int
	Store                  store.Store
}

type Adapter struct {
	driveService       *drive.Service
	botEmail           string
	store              store.Store
	handler            adapter.EventHandler
	listenAddr         string
	webhookURL         string
	server             *http.Server
	contentMaxSize     int64
	maxPaginationPages int
	channelToken       string
	ctx                context.Context
	mu                 sync.Mutex
	docLocks           sync.Map
	pageToken          string
	channelID          string
	resourceID         string
}

func New(cfg Config) (*Adapter, error) {
	ctx := context.Background()
	driveService, err := drive.NewService(ctx, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.ServiceAccountKeyPath))
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
		driveService:       driveService,
		botEmail:           email,
		store:              cfg.Store,
		listenAddr:         cfg.ListenAddr,
		webhookURL:         cfg.WebhookURL,
		contentMaxSize:     maxSize,
		maxPaginationPages: cfg.MaxPaginationPages,
		channelToken:       uuid.New().String(),
	}, nil
}

func (a *Adapter) Name() string {
	return "googledocs"
}

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	a.handler = handler
	a.ctx = ctx

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/googledocs", func(w http.ResponseWriter, r *http.Request) {
		a.handleNotification(r.Context(), w, r)
	})

	a.server = &http.Server{
		Addr:    a.listenAddr,
		Handler: mux,
	}

	go a.watchRenewalLoop(ctx)

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

func (a *Adapter) watchRenewalLoop(ctx context.Context) {
	for {
		expiration, err := a.registerWatch(ctx)
		if err != nil {
			slog.Warn("failed to register drive watch, retrying in 1 minute", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
				continue
			}
		}

		renewAt := time.Until(expiration) - 5*time.Minute
		if renewAt < time.Minute {
			renewAt = time.Minute
		}
		slog.Info("drive watch registered, will renew", "renew_in", renewAt.Round(time.Second))

		select {
		case <-ctx.Done():
			return
		case <-time.After(renewAt):
			a.stopWatch()
		}
	}
}

func (a *Adapter) registerWatch(ctx context.Context) (time.Time, error) {
	if a.webhookURL == "" {
		return time.Time{}, fmt.Errorf("webhookUrl is required for Drive push notifications")
	}

	startToken, err := a.driveService.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		return time.Time{}, fmt.Errorf("getting start page token: %w", err)
	}

	a.mu.Lock()
	a.pageToken = startToken.StartPageToken
	token := a.pageToken
	a.mu.Unlock()

	channelID := uuid.New().String()

	channel, err := a.driveService.Changes.Watch(token, &drive.Channel{
		Id:      channelID,
		Type:    "web_hook",
		Address: a.webhookURL,
		Token:   a.channelToken,
	}).Context(ctx).Do()
	if err != nil {
		return time.Time{}, fmt.Errorf("registering drive watch: %w", err)
	}

	a.channelID = channel.Id
	a.resourceID = channel.ResourceId
	expiration := time.UnixMilli(channel.Expiration)
	slog.Info("registered drive push notifications", "channel_id", a.channelID, "expiration", expiration)

	return expiration, nil
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

func (a *Adapter) handleNotification(_ context.Context, w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Goog-Channel-Token")
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.channelToken)) != 1 {
		http.Error(w, "invalid channel token", http.StatusForbidden)
		return
	}

	state := r.Header.Get("X-Goog-Resource-State")
	if state == "sync" {
		w.WriteHeader(http.StatusOK)
		return
	}

	go func() {
		if err := a.processChanges(a.ctx); err != nil {
			slog.Error("error processing drive changes", "error", err)
		}
	}()

	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) processChanges(ctx context.Context) error {
	a.mu.Lock()
	if a.pageToken == "" {
		a.mu.Unlock()
		return nil
	}
	token := a.pageToken
	a.mu.Unlock()

	seen := map[string]bool{}
	var docIDs []string
	for page := 0; page < a.maxPaginationPages; page++ {
		changeList, err := a.driveService.Changes.List(token).
			Fields("nextPageToken", "newStartPageToken", "changes(fileId, file(mimeType))").
			Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("listing drive changes: %w", err)
		}

		for _, change := range changeList.Changes {
			if change.File == nil || change.File.MimeType != "application/vnd.google-apps.document" {
				continue
			}
			if !seen[change.FileId] {
				seen[change.FileId] = true
				docIDs = append(docIDs, change.FileId)
			}
		}

		if changeList.NewStartPageToken != "" {
			token = changeList.NewStartPageToken
			break
		}
		if changeList.NextPageToken != "" {
			token = changeList.NextPageToken
			continue
		}
		break
	}

	a.mu.Lock()
	a.pageToken = token
	a.mu.Unlock()

	for _, fileID := range docIDs {
		docMu := a.docLock(fileID)
		docMu.Lock()
		if err := a.checkDocumentComments(ctx, fileID); err != nil {
			slog.Error("error checking document comments", "file_id", fileID, "error", err)
		}
		docMu.Unlock()
	}

	return nil
}

func (a *Adapter) docLock(fileID string) *sync.Mutex {
	v, _ := a.docLocks.LoadOrStore(fileID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (a *Adapter) checkDocumentComments(ctx context.Context, fileID string) error {
	lastSeen, err := a.store.GetAdapterState(ctx, "googledocs", fileID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("getting last seen timestamp: %w", err)
	}

	var allModifiedComments []*drive.Comment
	pageToken := ""
	for page := 0; page < a.maxPaginationPages; page++ {
		commentsCall := a.driveService.Comments.List(fileID).
			Fields("nextPageToken", "comments(id, content, resolved, author(emailAddress, displayName), replies(content, author(me, emailAddress, displayName)), quotedFileContent(value), createdTime, modifiedTime, htmlContent)").
			PageSize(100).
			IncludeDeleted(false).
			Context(ctx)

		if lastSeen != "" {
			commentsCall = commentsCall.StartModifiedTime(lastSeen)
		}
		if pageToken != "" {
			commentsCall = commentsCall.PageToken(pageToken)
		}

		commentList, err := commentsCall.Do()
		if err != nil {
			return fmt.Errorf("listing comments: %w", err)
		}

		allModifiedComments = append(allModifiedComments, commentList.Comments...)
		if commentList.NextPageToken == "" {
			break
		}
		pageToken = commentList.NextPageToken
	}

	var relevantComments []*drive.Comment
	var latestModified string
	for _, comment := range allModifiedComments {
		if comment.Resolved || !a.isRelevantComment(comment) {
			continue
		}
		if extractContent(comment) == "" {
			continue
		}
		if comment.ModifiedTime > latestModified {
			latestModified = comment.ModifiedTime
		}
		relevantComments = append(relevantComments, comment)
	}

	if len(relevantComments) == 0 {
		return nil
	}

	docText, err := a.fetchDocumentText(ctx, fileID)
	if err != nil {
		slog.Warn("failed to fetch document text", "file_id", fileID, "error", err)
	}

	allComments, err := a.fetchAllComments(ctx, fileID)
	if err != nil {
		slog.Warn("failed to fetch all comments", "file_id", fileID, "error", err)
	}

	var events []domain.IncomingEvent
	for _, comment := range relevantComments {
		docContext := buildDocumentContext(docText, allComments, comment.Id, a.contentMaxSize)

		var userID string
		if comment.Author != nil {
			userID = comment.Author.EmailAddress
		}

		var quotedText string
		if comment.QuotedFileContent != nil {
			quotedText = comment.QuotedFileContent.Value
		}

		ref := NewRef(fileID, comment.Id)
		event := domain.IncomingEvent{
			ChannelRef: ref,
			ThreadID:   fmt.Sprintf("googledocs-%s-%s", fileID, comment.Id),
			UserID:     userID,
			Content:    extractContent(comment),
			Metadata: map[string]string{
				"document_id":      fileID,
				"comment_id":       comment.Id,
				"document_context": docContext,
				"quoted_text":      quotedText,
				"comment_thread":   formatCommentThread(comment),
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

	if err := a.handler(ctx, events); err != nil {
		return fmt.Errorf("handling events: %w", err)
	}

	return nil
}

func (a *Adapter) isRelevantComment(comment *drive.Comment) bool {
	if len(comment.Replies) > 0 {
		lastReply := comment.Replies[len(comment.Replies)-1]
		if lastReply.Author != nil && lastReply.Author.Me {
			return false
		}
	}

	if comment.Author != nil && comment.Author.EmailAddress == a.botEmail {
		return false
	}

	content := extractContent(comment)
	if strings.Contains(content, a.botEmail) {
		return true
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

func formatCommentThread(comment *drive.Comment) string {
	var sb strings.Builder
	authorName := "Unknown"
	if comment.Author != nil && comment.Author.DisplayName != "" {
		authorName = comment.Author.DisplayName
	}
	fmt.Fprintf(&sb, "[%s]: %s\n", authorName, comment.Content)
	for _, r := range comment.Replies {
		replyAuthor := "Unknown"
		if r.Author != nil && r.Author.DisplayName != "" {
			replyAuthor = r.Author.DisplayName
		}
		fmt.Fprintf(&sb, "  [%s]: %s\n", replyAuthor, r.Content)
	}
	return sb.String()
}

func extractContent(comment *drive.Comment) string {
	if len(comment.Replies) > 0 {
		return strings.TrimSpace(comment.Replies[len(comment.Replies)-1].Content)
	}
	return strings.TrimSpace(comment.Content)
}

func (a *Adapter) fetchDocumentText(ctx context.Context, fileID string) (string, error) {
	resp, err := a.driveService.Files.Export(fileID, "text/plain").Context(ctx).Download()
	if err != nil {
		return "", fmt.Errorf("exporting document: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading document body: %w", err)
	}

	return string(body), nil
}

func (a *Adapter) fetchAllComments(ctx context.Context, fileID string) ([]*drive.Comment, error) {
	var all []*drive.Comment
	pageToken := ""
	for page := 0; page < a.maxPaginationPages; page++ {
		call := a.driveService.Comments.List(fileID).
			Fields("nextPageToken", "comments(id, content, resolved, author(displayName), replies(content, author(displayName)), quotedFileContent(value))").
			PageSize(100).
			IncludeDeleted(false).
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		commentList, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("listing all comments: %w", err)
		}

		all = append(all, commentList.Comments...)
		if commentList.NextPageToken == "" {
			break
		}
		pageToken = commentList.NextPageToken
	}
	return all, nil
}

type commentRef struct {
	index   int
	pos     int
	comment *drive.Comment
}

type markerInsertion struct {
	insertAt int
	marker   string
}

func buildDocumentContext(docText string, allComments []*drive.Comment, triggeringCommentID string, maxSize int64) string {
	refs := indexComments(docText, allComments, triggeringCommentID)
	annotated := insertInlineMarkers(docText, refs, allComments, triggeringCommentID)

	var sb strings.Builder
	sb.WriteString("=== DOCUMENT ===\n")
	sb.WriteString(annotated)

	if len(refs) > 0 {
		sb.WriteString("\n\n=== OTHER COMMENTS ===\n")
		sb.WriteString(formatCommentSection(refs))
	}

	return truncateContent(sb.String(), maxSize)
}

func indexComments(docText string, allComments []*drive.Comment, triggeringCommentID string) []commentRef {
	var refs []commentRef
	for _, c := range allComments {
		if c.Resolved || c.Id == triggeringCommentID {
			continue
		}
		pos := -1
		if c.QuotedFileContent != nil && c.QuotedFileContent.Value != "" {
			pos = strings.Index(docText, c.QuotedFileContent.Value)
		}
		refs = append(refs, commentRef{pos: pos, comment: c})
	}

	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].pos == -1 && refs[j].pos == -1 {
			return false
		}
		if refs[i].pos == -1 {
			return false
		}
		if refs[j].pos == -1 {
			return true
		}
		return refs[i].pos < refs[j].pos
	})

	for i := range refs {
		refs[i].index = i + 1
	}
	return refs
}

func insertInlineMarkers(docText string, refs []commentRef, allComments []*drive.Comment, triggeringCommentID string) string {
	var markers []markerInsertion
	for _, c := range allComments {
		if c.Id == triggeringCommentID && c.QuotedFileContent != nil && c.QuotedFileContent.Value != "" {
			if pos := strings.Index(docText, c.QuotedFileContent.Value); pos >= 0 {
				markers = append(markers, markerInsertion{
					insertAt: pos + len(c.QuotedFileContent.Value),
					marker:   " [Active Comment]",
				})
			}
			break
		}
	}
	for _, r := range refs {
		if r.pos == -1 {
			continue
		}
		markers = append(markers, markerInsertion{
			insertAt: r.pos + len(r.comment.QuotedFileContent.Value),
			marker:   fmt.Sprintf(" [Comment %d]", r.index),
		})
	}

	sort.SliceStable(markers, func(i, j int) bool {
		return markers[i].insertAt > markers[j].insertAt
	})

	result := docText
	for _, m := range markers {
		result = result[:m.insertAt] + m.marker + result[m.insertAt:]
	}
	return result
}

func formatCommentSection(refs []commentRef) string {
	var sb strings.Builder
	for _, r := range refs {
		if r.comment.QuotedFileContent != nil && r.comment.QuotedFileContent.Value != "" {
			fmt.Fprintf(&sb, "\n[Comment %d] On: %q\n", r.index, r.comment.QuotedFileContent.Value)
		} else {
			fmt.Fprintf(&sb, "\n[Comment %d] (general comment)\n", r.index)
		}
		authorName := "Unknown"
		if r.comment.Author != nil && r.comment.Author.DisplayName != "" {
			authorName = r.comment.Author.DisplayName
		}
		fmt.Fprintf(&sb, "  [%s]: %s\n", authorName, r.comment.Content)
		for _, reply := range r.comment.Replies {
			replyAuthor := "Unknown"
			if reply.Author != nil && reply.Author.DisplayName != "" {
				replyAuthor = reply.Author.DisplayName
			}
			fmt.Fprintf(&sb, "    [%s]: %s\n", replyAuthor, reply.Content)
		}
	}
	return sb.String()
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

	var multiplier int64
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
