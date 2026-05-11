package googledocs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"google.golang.org/api/drive/v3"
)

func TestParseContentMaxSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"100KB", 102400, false},
		{"1MB", 1048576, false},
		{"10MB", 10485760, false},
		{"0", 0, false},
		{"", 0, true},
		{"abc", 0, true},
		{"KB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseContentMaxSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseContentMaxSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseContentMaxSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterComments_Mention(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content: "Hey @bot@example.com can you fix this?",
		Author:  &drive.User{EmailAddress: "user@example.com"},
	}

	if !a.isRelevantComment(comment) {
		t.Error("expected comment mentioning bot email to be relevant")
	}
}

func TestFilterComments_ActionItem(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content:     "Please fix this bug",
		HtmlContent: `<span class="action_item">bot@example.com</span>`,
		Author:      &drive.User{EmailAddress: "user@example.com"},
	}

	if !a.isRelevantComment(comment) {
		t.Error("expected comment with action item assigned to bot to be relevant")
	}
}

func TestFilterComments_Resolved(t *testing.T) {
	comment := &drive.Comment{
		Content:  "Hey @bot@example.com fix this",
		Resolved: true,
		Author:   &drive.User{EmailAddress: "user@example.com"},
	}

	if !comment.Resolved {
		t.Error("expected resolved comment to be filtered out by caller")
	}
}

func TestFilterComments_ReplyWithoutMention(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content: "Hey @bot@example.com fix this",
		Author:  &drive.User{EmailAddress: "user@example.com"},
		Replies: []*drive.Reply{
			{Content: "Done!", Author: &drive.User{Me: true}},
			{Content: "can you recheck", Author: &drive.User{EmailAddress: "user@example.com"}},
		},
	}

	if a.isRelevantComment(comment) {
		t.Error("expected reply without bot mention to not be relevant")
	}
}

func TestFilterComments_ReplyWithMention(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content: "Hey @bot@example.com fix this",
		Author:  &drive.User{EmailAddress: "user@example.com"},
		Replies: []*drive.Reply{
			{Content: "Done!", Author: &drive.User{Me: true}},
			{Content: "@bot@example.com can you recheck", Author: &drive.User{EmailAddress: "user@example.com"}},
		},
	}

	if !a.isRelevantComment(comment) {
		t.Error("expected reply mentioning bot to be relevant")
	}
}

func TestFilterComments_BotLastReply(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content: "Hey @bot@example.com fix this",
		Author:  &drive.User{EmailAddress: "user@example.com"},
		Replies: []*drive.Reply{
			{Content: "Looking into this...", Author: &drive.User{Me: true}},
		},
	}

	if a.isRelevantComment(comment) {
		t.Error("expected comment where bot posted the last reply to not be relevant")
	}
}

func TestFilterComments_NoMatch(t *testing.T) {
	a := &Adapter{botEmail: "bot@example.com"}

	comment := &drive.Comment{
		Content: "This is a regular comment with no mention",
		Author:  &drive.User{EmailAddress: "user@example.com"},
	}

	if a.isRelevantComment(comment) {
		t.Error("expected comment without mention or action item to not be relevant")
	}
}

func TestBuildIncomingEvent(t *testing.T) {
	ref := NewRef("doc123", "comment456")

	if ref.Channel != "googledocs" {
		t.Errorf("Channel = %q, want %q", ref.Channel, "googledocs")
	}
	if ref.ThreadKey != "doc123#comment456" {
		t.Errorf("ThreadKey = %q, want %q", ref.ThreadKey, "doc123#comment456")
	}
	if ref.Properties["document_id"] != "doc123" {
		t.Errorf("Properties[document_id] = %q, want %q", ref.Properties["document_id"], "doc123")
	}
	if ref.Properties["comment_id"] != "comment456" {
		t.Errorf("Properties[comment_id] = %q, want %q", ref.Properties["comment_id"], "comment456")
	}
}

func TestTruncateContent(t *testing.T) {
	content := "Hello, this is a test document with some content."
	result := truncateContent(content, 10)

	if len(result) <= 10 {
		t.Errorf("expected truncated content to include truncation notice")
	}
	if result[:10] != content[:10] {
		t.Errorf("truncated content prefix = %q, want %q", result[:10], content[:10])
	}
	if !contains(result, "[Content truncated due to size limit]") {
		t.Error("expected truncation notice in output")
	}
}

func TestTruncateContent_Unlimited(t *testing.T) {
	content := "Hello, this is a test document with some content."
	result := truncateContent(content, 0)

	if result != content {
		t.Errorf("expected no truncation when maxSize=0, got %q", result)
	}
}

func TestTruncateContent_UnderLimit(t *testing.T) {
	content := "Short"
	result := truncateContent(content, 1024)

	if result != content {
		t.Errorf("expected no truncation when content is under limit, got %q", result)
	}
}

func TestWebhookHandler_InvalidToken(t *testing.T) {
	a := &Adapter{
		channelToken: "valid-token",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/googledocs", func(w http.ResponseWriter, r *http.Request) {
		a.handleNotification(r.Context(), w, r)
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/googledocs", nil)
	req.Header.Set("X-Goog-Channel-Token", "wrong-token")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestWebhookHandler_SyncMessage(t *testing.T) {
	a := &Adapter{
		channelToken: "valid-token",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/googledocs", func(w http.ResponseWriter, r *http.Request) {
		a.handleNotification(r.Context(), w, r)
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/googledocs", nil)
	req.Header.Set("X-Goog-Channel-Token", "valid-token")
	req.Header.Set("X-Goog-Resource-State", "sync")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d for sync message", rec.Code, http.StatusOK)
	}
}

func TestExtractContent_CommentOnly(t *testing.T) {
	comment := &drive.Comment{
		Content: "Please fix this bug",
		Author:  &drive.User{EmailAddress: "user@example.com"},
	}

	content := extractContent(comment)
	if content != "Please fix this bug" {
		t.Errorf("content = %q, want %q", content, "Please fix this bug")
	}
}

func TestExtractContent_WithReplies(t *testing.T) {
	comment := &drive.Comment{
		Content: "Original comment",
		Author:  &drive.User{EmailAddress: "user@example.com"},
		Replies: []*drive.Reply{
			{Content: "First reply", Author: &drive.User{EmailAddress: "user@example.com"}},
			{Content: "Latest reply", Author: &drive.User{EmailAddress: "user@example.com"}},
		},
	}

	content := extractContent(comment)
	if content != "Latest reply" {
		t.Errorf("content = %q, want %q", content, "Latest reply")
	}
}

func TestExtractServiceAccountEmail_InvalidFile(t *testing.T) {
	_, err := extractServiceAccountEmail("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestExtractServiceAccountEmail_InvalidJSON(t *testing.T) {
	tmpFile := t.TempDir() + "/invalid.json"
	if err := writeTestFile(tmpFile, "not json"); err != nil {
		t.Fatal(err)
	}

	_, err := extractServiceAccountEmail(tmpFile)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExtractServiceAccountEmail_MissingEmail(t *testing.T) {
	tmpFile := t.TempDir() + "/no-email.json"
	if err := writeTestFile(tmpFile, `{"type": "service_account"}`); err != nil {
		t.Fatal(err)
	}

	_, err := extractServiceAccountEmail(tmpFile)
	if err == nil {
		t.Error("expected error for missing client_email")
	}
}

func TestExtractServiceAccountEmail_Valid(t *testing.T) {
	tmpFile := t.TempDir() + "/valid.json"
	if err := writeTestFile(tmpFile, `{"client_email": "bot@example.iam.gserviceaccount.com"}`); err != nil {
		t.Fatal(err)
	}

	email, err := extractServiceAccountEmail(tmpFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if email != "bot@example.iam.gserviceaccount.com" {
		t.Errorf("email = %q, want %q", email, "bot@example.iam.gserviceaccount.com")
	}
}

func TestIsActionItemAssignedTo(t *testing.T) {
	tests := []struct {
		name        string
		htmlContent string
		email       string
		want        bool
	}{
		{
			name:        "assigned",
			htmlContent: `<span class="action_item">bot@example.com</span>`,
			email:       "bot@example.com",
			want:        true,
		},
		{
			name:        "not assigned",
			htmlContent: `<span class="action_item">other@example.com</span>`,
			email:       "bot@example.com",
			want:        false,
		},
		{
			name:        "empty html",
			htmlContent: "",
			email:       "bot@example.com",
			want:        false,
		},
		{
			name:        "email without action_item",
			htmlContent: `<span>bot@example.com</span>`,
			email:       "bot@example.com",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := &drive.Comment{HtmlContent: tt.htmlContent}
			if got := isActionItemAssignedTo(comment, tt.email); got != tt.want {
				t.Errorf("isActionItemAssignedTo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
