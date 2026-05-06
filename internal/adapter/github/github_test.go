package github

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/google/go-github/v68/github"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
)

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "valid owner/repo",
			input:     "owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "valid with nested path",
			input:     "my-org/my-repo",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
			wantErr:   false,
		},
		{
			name:    "no slash",
			input:   "noslash",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := splitRepo(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("splitRepo(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitRepo(%q) unexpected error: %v", tt.input, err)
			}
			if owner != tt.wantOwner {
				t.Errorf("splitRepo(%q) owner = %q, want %q", tt.input, owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("splitRepo(%q) repo = %q, want %q", tt.input, repo, tt.wantRepo)
			}
		})
	}
}

// generateTestPEMKey generates a PEM-encoded RSA private key for testing.
func generateTestPEMKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

// newTestAdapter creates an Adapter suitable for webhook tests without
// needing a real GitHub App. It uses a generated RSA key and the given
// webhook secret and bot username.
func newTestAdapter(t *testing.T, webhookSecret, botUsername string) *Adapter {
	t.Helper()
	return newTestAdapterWithUsers(t, webhookSecret, botUsername, []string{"*"})
}

func newTestAdapterWithUsers(t *testing.T, webhookSecret, botUsername string, allowedUsers []string) *Adapter {
	t.Helper()
	pemKey := generateTestPEMKey(t)
	a, err := New(12345, pemKey, webhookSecret, botUsername, ":8080", allowedUsers)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return a
}

// signPayload computes the HMAC-SHA256 signature in the format GitHub uses
// (sha256=<hex>).
func signPayload(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// issueCommentPayload builds a minimal IssueCommentEvent JSON body.
func issueCommentPayload(t *testing.T, body, repoFullName, login string, issueNum int, commentID, installationID int64, isPR bool) []byte {
	t.Helper()
	return issueCommentPayloadWithAuthor(t, body, repoFullName, login, "", issueNum, commentID, installationID, isPR)
}

func issueCommentPayloadWithAuthor(t *testing.T, body, repoFullName, login, issueAuthor string, issueNum int, commentID, installationID int64, isPR bool) []byte {
	t.Helper()

	issue := map[string]interface{}{
		"number": issueNum,
	}
	if issueAuthor != "" {
		issue["user"] = map[string]interface{}{
			"login": issueAuthor,
		}
	}

	event := map[string]interface{}{
		"action": "created",
		"comment": map[string]interface{}{
			"id":   commentID,
			"body": body,
			"user": map[string]interface{}{
				"login": login,
			},
		},
		"repository": map[string]interface{}{
			"full_name": repoFullName,
		},
		"issue": issue,
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}

	if isPR {
		issue["pull_request"] = map[string]interface{}{
			"url": "https://api.github.com/repos/" + repoFullName + "/pulls/" + fmt.Sprint(issueNum),
		}
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return data
}

func TestHandleIssueComment(t *testing.T) {
	const (
		secret       = "test-webhook-secret"
		botUser      = "ai-coworker"
		repoFullName = "myorg/myrepo"
		login        = "someuser"
		issueNum     = 42
		commentID    = int64(99999)
		installID    = int64(77777)
	)

	a := newTestAdapter(t, secret, botUser)

	var captured domain.IncomingEvent
	var handlerCalled bool
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		captured = ev
		return nil
	})

	// Build the handler via the adapter's handleWebhook, served behind a test server.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := "@ai-coworker fix this"
	payload := issueCommentPayload(t, body, repoFullName, login, issueNum, commentID, installID, true)
	sig := signPayload([]byte(secret), payload)

	req, err := http.NewRequest("POST", ts.URL+"/webhook/github", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	if !handlerCalled {
		t.Fatal("event handler was not called")
	}

	// Verify IncomingEvent fields.
	if captured.ChannelRef.Channel != "github" {
		t.Errorf("ChannelRef.Channel = %q, want %q", captured.ChannelRef.Channel, "github")
	}
	expectedThreadID := fmt.Sprintf("github-%s-%d", repoFullName, issueNum)
	if captured.ThreadID != expectedThreadID {
		t.Errorf("ThreadID = %q, want %q", captured.ThreadID, expectedThreadID)
	}
	if captured.UserID != login {
		t.Errorf("UserID = %q, want %q", captured.UserID, login)
	}
	// The mention should be stripped from Content.
	if captured.Content != "fix this" {
		t.Errorf("Content = %q, want %q", captured.Content, "fix this")
	}
	if captured.ChannelRef.Properties["repo"] != repoFullName {
		t.Errorf("ChannelRef.Properties[repo] = %q, want %q", captured.ChannelRef.Properties["repo"], repoFullName)
	}
	if captured.ChannelRef.Properties["issue_num"] != fmt.Sprint(issueNum) {
		t.Errorf("ChannelRef.Properties[issue_num] = %q, want %q", captured.ChannelRef.Properties["issue_num"], fmt.Sprint(issueNum))
	}
	if captured.ChannelRef.Properties["comment_id"] != fmt.Sprint(commentID) {
		t.Errorf("ChannelRef.Properties[comment_id] = %q, want %q", captured.ChannelRef.Properties["comment_id"], fmt.Sprint(commentID))
	}
	expectedThreadKey := fmt.Sprintf("%s#%d", repoFullName, issueNum)
	if captured.ChannelRef.ThreadKey != expectedThreadKey {
		t.Errorf("ChannelRef.ThreadKey = %q, want %q", captured.ChannelRef.ThreadKey, expectedThreadKey)
	}

	// Metadata checks.
	wantMeta := map[string]string{
		"type":            "issue_comment",
		"repo":            repoFullName,
		"issue_num":       "42",
		"is_pr":           "true",
		"installation_id": "77777",
	}
	for k, v := range wantMeta {
		if captured.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %q, want %q", k, captured.Metadata[k], v)
		}
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	const secret = "correct-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		t.Fatal("handler should not be called for invalid signature")
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := []byte(`{"action":"created"}`)
	wrongSig := signPayload([]byte("wrong-secret"), payload)

	req, err := http.NewRequest("POST", ts.URL+"/webhook/github", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", wrongSig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandleIssueComment_CommentType(t *testing.T) {
	const (
		secret       = "test-webhook-secret"
		botUser      = "ai-coworker"
		repoFullName = "myorg/myrepo"
		login        = "someuser"
		issueNum     = 10
		commentID    = int64(55555)
		installID    = int64(77777)
	)

	a := newTestAdapter(t, secret, botUser)

	var captured domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		captured = ev
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := "@ai-coworker do something"
	payload := issueCommentPayload(t, body, repoFullName, login, issueNum, commentID, installID, false)
	sig := signPayload([]byte(secret), payload)

	req, _ := http.NewRequest("POST", ts.URL+"/webhook/github", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	_ = resp.Body.Close()

	if captured.ChannelRef.Properties["comment_type"] != "issue_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", captured.ChannelRef.Properties["comment_type"], "issue_comment")
	}
}

func prReviewCommentPayload(t *testing.T, body, repoFullName, login string, prNum int, commentID, installationID int64, path, branch string) []byte {
	t.Helper()

	event := map[string]interface{}{
		"action": "created",
		"comment": map[string]interface{}{
			"id":   commentID,
			"body": body,
			"path": path,
			"user": map[string]interface{}{
				"login": login,
			},
		},
		"pull_request": map[string]interface{}{
			"number": prNum,
			"head": map[string]interface{}{
				"ref": branch,
			},
		},
		"repository": map[string]interface{}{
			"full_name": repoFullName,
		},
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return data
}

func prReviewPayload(t *testing.T, body, repoFullName, login string, prNum int, installationID int64, branch, state string) []byte {
	t.Helper()

	event := map[string]interface{}{
		"action": "submitted",
		"review": map[string]interface{}{
			"body":  body,
			"state": state,
			"user": map[string]interface{}{
				"login": login,
			},
		},
		"pull_request": map[string]interface{}{
			"number": prNum,
			"head": map[string]interface{}{
				"ref": branch,
			},
		},
		"repository": map[string]interface{}{
			"full_name": repoFullName,
		},
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return data
}

func sendWebhook(t *testing.T, ts *httptest.Server, eventType string, payload []byte, secret string) *http.Response {
	t.Helper()
	sig := signPayload([]byte(secret), payload)
	req, err := http.NewRequest("POST", ts.URL+"/webhook/github", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	return resp
}

func TestHandlePRReviewComment_WithMention(t *testing.T) {
	const (
		secret  = "test-secret"
		botUser = "ai-coworker"
	)

	a := newTestAdapter(t, secret, botUser)

	var captured domain.IncomingEvent
	var handlerCalled bool
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		captured = ev
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewCommentPayload(t, "@ai-coworker add error handling here", "org/repo", "reviewer", 5, 88888, 77777, "main.go", "feat/branch")
	resp := sendWebhook(t, ts, "pull_request_review_comment", payload, secret)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !handlerCalled {
		t.Fatal("handler was not called")
	}
	if captured.Content != "add error handling here" {
		t.Errorf("Content = %q, want %q", captured.Content, "add error handling here")
	}
	if captured.ChannelRef.Properties["comment_type"] != "review_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", captured.ChannelRef.Properties["comment_type"], "review_comment")
	}
	if captured.ChannelRef.Properties["comment_id"] != "88888" {
		t.Errorf("Properties[comment_id] = %q, want %q", captured.ChannelRef.Properties["comment_id"], "88888")
	}
	if captured.Metadata["pr_branch"] != "feat/branch" {
		t.Errorf("Metadata[pr_branch] = %q, want %q", captured.Metadata["pr_branch"], "feat/branch")
	}
	if captured.Metadata["comment_id"] != "88888" {
		t.Errorf("Metadata[comment_id] = %q, want %q", captured.Metadata["comment_id"], "88888")
	}
	if captured.Metadata["is_pr"] != "true" {
		t.Errorf("Metadata[is_pr] = %q, want %q", captured.Metadata["is_pr"], "true")
	}
	if captured.Metadata["path"] != "main.go" {
		t.Errorf("Metadata[path] = %q, want %q", captured.Metadata["path"], "main.go")
	}
}

func TestHandlePRReviewComment_WithoutMention(t *testing.T) {
	a := newTestAdapter(t, "secret", "ai-coworker")

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewCommentPayload(t, "this looks wrong", "org/repo", "reviewer", 5, 88888, 77777, "main.go", "feat/branch")
	resp := sendWebhook(t, ts, "pull_request_review_comment", payload, "secret")
	_ = resp.Body.Close()

	if handlerCalled {
		t.Fatal("handler should not be called when bot is not mentioned")
	}
}

func TestHandlePRReview_WithMention(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	var captured domain.IncomingEvent
	var handlerCalled bool
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		captured = ev
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayload(t, "@ai-coworker please fix these issues", "org/repo", "reviewer", 3, 77777, "feat/fix", "changes_requested")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	if !handlerCalled {
		t.Fatal("handler was not called")
	}
	if captured.Content != "please fix these issues" {
		t.Errorf("Content = %q, want %q", captured.Content, "please fix these issues")
	}
	if captured.Metadata["pr_branch"] != "feat/fix" {
		t.Errorf("Metadata[pr_branch] = %q, want %q", captured.Metadata["pr_branch"], "feat/fix")
	}
	if captured.Metadata["review_state"] != "changes_requested" {
		t.Errorf("Metadata[review_state] = %q, want %q", captured.Metadata["review_state"], "changes_requested")
	}
	if captured.Metadata["is_pr"] != "true" {
		t.Errorf("Metadata[is_pr] = %q, want %q", captured.Metadata["is_pr"], "true")
	}
	if captured.Metadata["type"] != "review" {
		t.Errorf("Metadata[type] = %q, want %q", captured.Metadata["type"], "review")
	}
}

func TestHandlePRReview_WithoutMention(t *testing.T) {
	a := newTestAdapter(t, "secret", "ai-coworker")

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayload(t, "LGTM", "org/repo", "reviewer", 3, 77777, "feat/fix", "approved")
	resp := sendWebhook(t, ts, "pull_request_review", payload, "secret")
	_ = resp.Body.Close()

	if handlerCalled {
		t.Fatal("handler should not be called when bot is not mentioned")
	}
}

func TestGetInstallationClient_Caching(t *testing.T) {
	a := newTestAdapter(t, "secret", "bot")

	client1 := a.getInstallationClient(111)
	client2 := a.getInstallationClient(111)

	if client1 != client2 {
		t.Fatal("expected same *gh.Client instance for same installation ID, got different pointers")
	}

	// Different installation ID should yield a different client.
	client3 := a.getInstallationClient(222)
	if client1 == client3 {
		t.Fatal("expected different *gh.Client for different installation IDs")
	}

	// Suppress unused import lint for gh package (used in signature only via getInstallationClient return type).
	var _ *gh.Client
}

func TestIsUserAllowed(t *testing.T) {
	tests := []struct {
		name         string
		allowedUsers []string
		username     string
		want         bool
	}{
		{"wildcard allows all", []string{"*"}, "anyone", true},
		{"explicit user allowed", []string{"alice", "bob"}, "alice", true},
		{"explicit user denied", []string{"alice", "bob"}, "eve", false},
		{"empty list denies all", []string{}, "anyone", false},
		{"nil list denies all", nil, "anyone", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAdapterWithUsers(t, "secret", "bot", tt.allowedUsers)
			if got := a.isUserAllowed(tt.username); got != tt.want {
				t.Errorf("isUserAllowed(%q) = %v, want %v", tt.username, got, tt.want)
			}
		})
	}
}

func TestHandleIssueComment_UnauthorizedUser(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapterWithUsers(t, secret, "ai-coworker", []string{"alice"})

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := issueCommentPayload(t, "@ai-coworker fix this", "org/repo", "eve", 1, 111, 88888, false)
	resp := sendWebhook(t, ts, "issue_comment", payload, secret)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if handlerCalled {
		t.Fatal("handler should not be called for unauthorized user")
	}
}

func TestHandleIssueComment_EmptyAllowlistBlocksAll(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapterWithUsers(t, secret, "ai-coworker", []string{})

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := issueCommentPayload(t, "@ai-coworker fix this", "org/repo", "anyone", 1, 111, 77777, false)
	resp := sendWebhook(t, ts, "issue_comment", payload, secret)
	_ = resp.Body.Close()

	if handlerCalled {
		t.Fatal("handler should not be called when allowlist is empty")
	}
}

func TestHandleIssueComment_BotCreatedPR_NoMention(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	var received domain.IncomingEvent
	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		received = ev
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := issueCommentPayloadWithAuthor(t, "Please fix the tests", "org/repo", "reviewer", "ai-coworker[bot]", 5, 222, 77777, true)
	resp := sendWebhook(t, ts, "issue_comment", payload, secret)
	_ = resp.Body.Close()

	if !handlerCalled {
		t.Fatal("handler should be called for comments on bot-created PRs without mention")
	}
	if received.Content != "Please fix the tests" {
		t.Errorf("Content = %q, want %q", received.Content, "Please fix the tests")
	}
}

func TestHandleIssueComment_NotBotPR_NoMention_Ignored(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, ev domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := issueCommentPayloadWithAuthor(t, "Please fix the tests", "org/repo", "reviewer", "someone-else", 5, 222, 77777, true)
	resp := sendWebhook(t, ts, "issue_comment", payload, secret)
	_ = resp.Body.Close()

	if handlerCalled {
		t.Fatal("handler should not be called for comments on non-bot PRs without mention")
	}
}
