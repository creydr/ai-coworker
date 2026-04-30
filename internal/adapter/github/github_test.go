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
	pemKey := generateTestPEMKey(t)
	a, err := New(12345, pemKey, webhookSecret, botUsername)
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
		"issue": map[string]interface{}{
			"number": issueNum,
		},
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}

	if isPR {
		event["issue"].(map[string]interface{})["pull_request"] = map[string]interface{}{
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
	if captured.Channel != "github" {
		t.Errorf("Channel = %q, want %q", captured.Channel, "github")
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
	if captured.ChannelRef.Repo != repoFullName {
		t.Errorf("ChannelRef.Repo = %q, want %q", captured.ChannelRef.Repo, repoFullName)
	}
	if captured.ChannelRef.IssueNum != issueNum {
		t.Errorf("ChannelRef.IssueNum = %d, want %d", captured.ChannelRef.IssueNum, issueNum)
	}
	if captured.ChannelRef.CommentID != commentID {
		t.Errorf("ChannelRef.CommentID = %d, want %d", captured.ChannelRef.CommentID, commentID)
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
