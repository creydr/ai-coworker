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
	"net/url"
	"strings"
	"testing"

	gh "github.com/google/go-github/v68/github"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
)

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
	a, err := New(12345, pemKey, webhookSecret, botUsername, ":8080", "", allowedUsers)
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

	var captured []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		captured = append(captured, evs...)
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
	if len(captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured))
	}

	// Verify IncomingEvent fields.
	ev := captured[0]
	if ev.ChannelRef.Channel != "github" {
		t.Errorf("ChannelRef.Channel = %q, want %q", ev.ChannelRef.Channel, "github")
	}
	expectedThreadID := fmt.Sprintf("github-%s-%d", repoFullName, issueNum)
	if ev.ThreadID != expectedThreadID {
		t.Errorf("ThreadID = %q, want %q", ev.ThreadID, expectedThreadID)
	}
	if ev.UserID != login {
		t.Errorf("UserID = %q, want %q", ev.UserID, login)
	}
	// The mention should be stripped from Content.
	if ev.Content != "fix this" {
		t.Errorf("Content = %q, want %q", ev.Content, "fix this")
	}
	if ev.ChannelRef.Properties["repo"] != repoFullName {
		t.Errorf("ChannelRef.Properties[repo] = %q, want %q", ev.ChannelRef.Properties["repo"], repoFullName)
	}
	if ev.ChannelRef.Properties["issue_num"] != fmt.Sprint(issueNum) {
		t.Errorf("ChannelRef.Properties[issue_num] = %q, want %q", ev.ChannelRef.Properties["issue_num"], fmt.Sprint(issueNum))
	}
	if ev.ChannelRef.Properties["comment_id"] != fmt.Sprint(commentID) {
		t.Errorf("ChannelRef.Properties[comment_id] = %q, want %q", ev.ChannelRef.Properties["comment_id"], fmt.Sprint(commentID))
	}
	expectedThreadKey := fmt.Sprintf("%s#%d", repoFullName, issueNum)
	if ev.ChannelRef.ThreadKey != expectedThreadKey {
		t.Errorf("ChannelRef.ThreadKey = %q, want %q", ev.ChannelRef.ThreadKey, expectedThreadKey)
	}

	// Metadata checks.
	wantMeta := map[string]string{
		"vcs":             "github",
		"type":            "issue_comment",
		"repo":            repoFullName,
		"issue_num":       "42",
		"is_pr":           "true",
		"installation_id": "77777",
	}
	for k, v := range wantMeta {
		if ev.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %q, want %q", k, ev.Metadata[k], v)
		}
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	const secret = "correct-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
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

	var captured []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		captured = append(captured, evs...)
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

	if len(captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured))
	}
	if captured[0].ChannelRef.Properties["comment_type"] != "issue_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", captured[0].ChannelRef.Properties["comment_type"], "issue_comment")
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

func prReviewPayload(t *testing.T, body, repoFullName, login string, prNum int, reviewID, installationID int64, branch, state string) []byte { //nolint:unparam
	t.Helper()

	return prReviewPayloadWithPRAuthor(t, body, repoFullName, login, "", prNum, reviewID, installationID, branch, state)
}

func prReviewPayloadWithPRAuthor(t *testing.T, body, repoFullName, login, prAuthor string, prNum int, reviewID, installationID int64, branch, state string) []byte {
	t.Helper()

	pr := map[string]interface{}{
		"number": prNum,
		"head": map[string]interface{}{
			"ref": branch,
		},
	}
	if prAuthor != "" {
		pr["user"] = map[string]interface{}{
			"login": prAuthor,
		}
	}

	event := map[string]interface{}{
		"action": "submitted",
		"review": map[string]interface{}{
			"id":    reviewID,
			"body":  body,
			"state": state,
			"user": map[string]interface{}{
				"login": login,
			},
		},
		"pull_request": pr,
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

// injectMockGitHubAPI creates a mock GitHub API server and stores a client
// pointing to it in the adapter's installation client cache. The caller must
// defer server.Close().
func injectMockGitHubAPI(t *testing.T, a *Adapter, installationID int64, handler http.Handler) *httptest.Server { //nolint:unparam
	t.Helper()
	server := httptest.NewServer(handler)
	client := gh.NewClient(nil)
	baseURL, _ := url.Parse(server.URL + "/")
	client.BaseURL = baseURL
	a.installationClients.Store(installationID, client)
	return server
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

// Review comment webhooks are intentionally ignored — processing is deferred
// to handlePRReview when the review is submitted.
func TestHandlePRReviewComment_Ignored(t *testing.T) {
	a := newTestAdapter(t, "test-secret", "ai-coworker")

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewCommentPayload(t, "@ai-coworker add error handling here", "org/repo", "reviewer", 5, 88888, 77777, "main.go", "feat/branch")
	resp := sendWebhook(t, ts, "pull_request_review_comment", payload, "test-secret")
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if handlerCalled {
		t.Fatal("handler should not be called for review comment webhooks")
	}
}

func TestHandlePRReview_WithMention(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Mock GitHub API returning no inline comments for this review.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]interface{}{})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	var captured []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		captured = append(captured, evs...)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayload(t, "@ai-coworker please fix these issues", "org/repo", "reviewer", 3, 55555, 77777, "feat/fix", "changes_requested")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	if len(captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured))
	}
	ev := captured[0]
	if ev.Content != "please fix these issues" {
		t.Errorf("Content = %q, want %q", ev.Content, "please fix these issues")
	}
	if ev.Metadata["vcs"] != "github" {
		t.Errorf("Metadata[vcs] = %q, want %q", ev.Metadata["vcs"], "github")
	}
	if ev.Metadata["pr_branch"] != "feat/fix" {
		t.Errorf("Metadata[pr_branch] = %q, want %q", ev.Metadata["pr_branch"], "feat/fix")
	}
	if ev.Metadata["review_state"] != "changes_requested" {
		t.Errorf("Metadata[review_state] = %q, want %q", ev.Metadata["review_state"], "changes_requested")
	}
	if ev.Metadata["is_pr"] != "true" {
		t.Errorf("Metadata[is_pr] = %q, want %q", ev.Metadata["is_pr"], "true")
	}
	if ev.Metadata["type"] != "review" {
		t.Errorf("Metadata[type] = %q, want %q", ev.Metadata["type"], "review")
	}
}

func TestHandlePRReview_WithoutMention(t *testing.T) {
	a := newTestAdapter(t, "secret", "ai-coworker")

	// Mock API returns comments that also don't mention the bot.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 111, "body": "looks good", "path": "main.go"},
		})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayload(t, "LGTM", "org/repo", "reviewer", 3, 55555, 77777, "feat/fix", "approved")
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
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
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
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
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

	var captured []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		captured = append(captured, evs...)
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

	if len(captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(captured))
	}
	if captured[0].Content != "Please fix the tests" {
		t.Errorf("Content = %q, want %q", captured[0].Content, "Please fix the tests")
	}
}

func TestHandleIssueComment_NotBotPR_NoMention_Ignored(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
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

func TestHandlePRReview_EmptyBodyNoComments_Ignored(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Mock API returns no comments for this review.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]interface{}{})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	handlerCalled := false
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		handlerCalled = true
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayload(t, "", "org/repo", "reviewer", 3, 55555, 77777, "feat/fix", "commented")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	if handlerCalled {
		t.Fatal("handler should not be called for reviews with empty body and no comments")
	}
}

func TestHandlePRReview_WithComments(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Mock API returns 2 inline comments with line info.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 111, "body": "fix error handling", "path": "main.go", "line": 42, "start_line": 40},
			{"id": 222, "body": "add validation", "path": "utils.go", "line": 15},
		})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	var events []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		events = append(events, evs...)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayloadWithPRAuthor(t, "@ai-coworker fix these", "org/repo", "reviewer", "ai-coworker[bot]", 3, 55555, 77777, "feat/fix", "changes_requested")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	// Expect 3 events: 1 for review body + 2 for inline comments.
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// First event: review body.
	if events[0].Metadata["type"] != "review" {
		t.Errorf("event[0] type = %q, want %q", events[0].Metadata["type"], "review")
	}
	if events[0].Content != "fix these" {
		t.Errorf("event[0] content = %q, want %q", events[0].Content, "fix these")
	}

	// Second event: first inline comment (multi-line range).
	if events[1].Metadata["type"] != "review_comment" {
		t.Errorf("event[1] type = %q, want %q", events[1].Metadata["type"], "review_comment")
	}
	if events[1].Content != "fix error handling" {
		t.Errorf("event[1] content = %q, want %q", events[1].Content, "fix error handling")
	}
	if events[1].Metadata["path"] != "main.go" {
		t.Errorf("event[1] path = %q, want %q", events[1].Metadata["path"], "main.go")
	}
	if events[1].Metadata["line"] != "42" {
		t.Errorf("event[1] line = %q, want %q", events[1].Metadata["line"], "42")
	}
	if events[1].Metadata["start_line"] != "40" {
		t.Errorf("event[1] start_line = %q, want %q", events[1].Metadata["start_line"], "40")
	}
	if events[1].ChannelRef.Properties["comment_id"] != "111" {
		t.Errorf("event[1] comment_id = %q, want %q", events[1].ChannelRef.Properties["comment_id"], "111")
	}
	if events[1].ChannelRef.Properties["comment_type"] != "review_comment" {
		t.Errorf("event[1] comment_type = %q, want %q", events[1].ChannelRef.Properties["comment_type"], "review_comment")
	}

	// Third event: second inline comment (single line).
	if events[2].Metadata["type"] != "review_comment" {
		t.Errorf("event[2] type = %q, want %q", events[2].Metadata["type"], "review_comment")
	}
	if events[2].Metadata["path"] != "utils.go" {
		t.Errorf("event[2] path = %q, want %q", events[2].Metadata["path"], "utils.go")
	}
	if events[2].Metadata["line"] != "15" {
		t.Errorf("event[2] line = %q, want %q", events[2].Metadata["line"], "15")
	}
	if events[2].Metadata["start_line"] != "" {
		t.Errorf("event[2] start_line = %q, want empty (single-line comment)", events[2].Metadata["start_line"])
	}
}

func TestHandlePRReview_EmptyBodyWithComments(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Mock API returns 1 comment — simulates a standalone single-file
	// comment ("Comment" button, not "Start a review").
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 111, "body": "@ai-coworker fix this", "path": "main.go"},
		})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	var events []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		events = append(events, evs...)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Empty review body — the comment is the only content.
	payload := prReviewPayload(t, "", "org/repo", "reviewer", 3, 55555, 77777, "feat/fix", "commented")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	// Only 1 event for the inline comment, no event for the empty body.
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Metadata["type"] != "review_comment" {
		t.Errorf("event type = %q, want %q", events[0].Metadata["type"], "review_comment")
	}
	if events[0].Content != "fix this" {
		t.Errorf("content = %q, want %q", events[0].Content, "fix this")
	}
}

func TestIsReviewRelevant(t *testing.T) {
	a := newTestAdapter(t, "secret", "ai-coworker")
	mention := "@ai-coworker"

	tests := []struct {
		name     string
		body     string
		comments []*gh.PullRequestComment
		isBotPR  bool
		want     bool
	}{
		{
			name: "mention in body",
			body: "@ai-coworker fix this",
			want: true,
		},
		{
			name:    "bot PR without mention",
			body:    "looks good",
			isBotPR: true,
			want:    true,
		},
		{
			name: "mention in comment",
			body: "some feedback",
			comments: []*gh.PullRequestComment{
				{Body: gh.Ptr("@ai-coworker fix this")},
			},
			want: true,
		},
		{
			name: "no mention anywhere",
			body: "some feedback",
			comments: []*gh.PullRequestComment{
				{Body: gh.Ptr("looks good")},
			},
			want: false,
		},
		{
			name: "empty everything",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.isReviewRelevant(tt.body, tt.comments, mention, tt.isBotPR)
			if got != tt.want {
				t.Errorf("isReviewRelevant() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandlePRReview_MentionOnlyInComment(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Review body does NOT mention the bot, but a comment does.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 111, "body": "@ai-coworker fix this function", "path": "main.go"},
			{"id": 222, "body": "also check this", "path": "utils.go"},
		})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	var events []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		events = append(events, evs...)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Body says "some feedback" (no mention), but a comment mentions the bot.
	payload := prReviewPayload(t, "some feedback", "org/repo", "reviewer", 3, 55555, 77777, "feat/fix", "changes_requested")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	// Expect 3 events: body + 2 comments (all included when review is relevant).
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Metadata["type"] != "review" {
		t.Errorf("event[0] type = %q, want %q", events[0].Metadata["type"], "review")
	}
	if events[1].Metadata["type"] != "review_comment" {
		t.Errorf("event[1] type = %q, want %q", events[1].Metadata["type"], "review_comment")
	}
}

func TestHandlePRReview_ReplyCommentResolvesLineInfo(t *testing.T) {
	const secret = "test-secret"
	a := newTestAdapter(t, secret, "ai-coworker")

	// Review contains a reply comment (in_reply_to_id set, line null) —
	// ListReviewComments returns null line info for replies.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/repos/org/repo/pulls/3/reviews/55555/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 333, "body": "still broken", "path": ".github/workflows/test.yml", "in_reply_to_id": 111},
		})
	})
	// Parent comment endpoint returns line info.
	apiMux.HandleFunc("/repos/org/repo/pulls/comments/111", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": 111, "body": "fix this", "path": ".github/workflows/test.yml", "line": 10, "start_line": 5,
		})
	})
	mockAPI := injectMockGitHubAPI(t, a, 77777, apiMux)
	defer mockAPI.Close()

	var events []domain.IncomingEvent
	handler := adapter.EventHandler(func(_ context.Context, evs []domain.IncomingEvent) error {
		events = append(events, evs...)
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	payload := prReviewPayloadWithPRAuthor(t, "", "org/repo", "reviewer", "ai-coworker[bot]", 3, 55555, 77777, "feat/fix", "commented")
	resp := sendWebhook(t, ts, "pull_request_review", payload, secret)
	_ = resp.Body.Close()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Metadata["line"] != "10" {
		t.Errorf("line = %q, want %q (should resolve from parent)", events[0].Metadata["line"], "10")
	}
	if events[0].Metadata["start_line"] != "5" {
		t.Errorf("start_line = %q, want %q (should resolve from parent)", events[0].Metadata["start_line"], "5")
	}
	if events[0].Metadata["path"] != ".github/workflows/test.yml" {
		t.Errorf("path = %q, want %q", events[0].Metadata["path"], ".github/workflows/test.yml")
	}
}
