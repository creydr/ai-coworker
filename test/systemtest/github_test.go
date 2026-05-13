//go:build systemtest

package systemtest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	webhookSecret = "test-webhook-secret"
	botUsername   = "test-bot"
	testOwner     = "test-org"
	testRepo      = "test-repo"
	adapterAddr   = "127.0.0.1:18080"
	pollTimeout   = 10 * time.Minute
)

var (
	fakeGH     *fakeGitHub
	binaryPath string
	binaryCmd  *exec.Cmd
)

func TestMain(m *testing.M) {
	databaseURL := requireEnv("SYSTEMTEST_DATABASE_URL")
	ollamaURL := requireEnv("SYSTEMTEST_OLLAMA_URL")
	model := requireEnv("SYSTEMTEST_MODEL")
	sandboxImage := requireEnv("SYSTEMTEST_SANDBOX_IMAGE")

	if err := resetDatabase(databaseURL); err != nil {
		log.Fatalf("resetting database: %v", err)
	}

	pemKey, err := generateRSAKey()
	if err != nil {
		log.Fatalf("generating RSA key: %v", err)
	}

	fg := newFakeGitHubForMain()
	fakeGH = fg

	configPath, err := renderConfig(configParams{
		DatabaseURL:   databaseURL,
		OllamaURL:     ollamaURL,
		Model:         model,
		SandboxImage:  sandboxImage,
		PrivateKey:    indentPEM(string(pemKey), "    "),
		WebhookSecret: webhookSecret,
		FakeGitHubURL: fg.url(),
	})
	if err != nil {
		log.Fatalf("rendering config: %v", err)
	}
	defer os.Remove(configPath)

	binaryPath, err = buildBinary()
	if err != nil {
		log.Fatalf("building binary: %v", err)
	}
	defer os.Remove(binaryPath)

	ctx, cancel := context.WithCancel(context.Background())
	binaryCmd, err = startBinary(ctx, binaryPath, configPath)
	if err != nil {
		cancel()
		log.Fatalf("starting binary: %v", err)
	}

	if err := waitForReady(adapterAddr, 30*time.Second); err != nil {
		cancel()
		log.Fatalf("binary not ready: %v", err)
	}

	code := m.Run()

	cancel()
	if binaryCmd.Process != nil {
		_ = binaryCmd.Process.Signal(syscall.SIGTERM)
		_ = binaryCmd.Wait()
	}
	fg.server.Close()

	os.Exit(code)
}

func TestGitHubIssueCommentResponse(t *testing.T) {
	tests := []struct {
		name        string
		issueNum    int
		commentID   int64
		message     string
		wantContain string
		wantAbsent  string
	}{
		{
			name:        "Discussion",
			issueNum:    1,
			commentID:   42,
			message:     "Hello! How are you today?",
			wantAbsent:  "Task completed successfully by system test sandbox",
			wantContain: "",
		},
		{
			name:        "Code task",
			issueNum:    3,
			commentID:   44,
			message:     "Write a function called Add that returns the sum of two integers and commit it to a new branch",
			wantContain: "Task completed successfully by system test sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := buildIssueCommentPayload(testOwner, testRepo, tt.issueNum, tt.commentID, fmt.Sprintf("@%s %s", botUsername, tt.message), "testuser", 99)
			sendWebhook(t, "issue_comment", payload)

			comment, ok := fakeGH.waitForIssueComment(testOwner, testRepo, tt.issueNum, pollTimeout)
			if !ok {
				t.Fatal("timed out waiting for GitHub comment response")
			}
			if comment.Body == "" {
				t.Fatal("expected non-empty response comment")
			}
			if tt.wantContain != "" && !strings.Contains(comment.Body, tt.wantContain) {
				t.Errorf("response should contain %q, got: %s", tt.wantContain, comment.Body)
			}
			if tt.wantAbsent != "" && strings.Contains(comment.Body, tt.wantAbsent) {
				t.Errorf("response should not contain %q, got: %s", tt.wantAbsent, comment.Body)
			}
			t.Logf("received response: %s", comment.Body)
		})
	}
}

func TestGitHubIssueCommentAcknowledgement(t *testing.T) {
	issueNum := 2
	commentID := int64(43)

	payload := buildIssueCommentPayload(testOwner, testRepo, issueNum, commentID, fmt.Sprintf("@%s Hello!", botUsername), "testuser", 99)
	sendWebhook(t, "issue_comment", payload)

	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		reactions := fakeGH.getReactions("issue_comment", commentID)
		for _, r := range reactions {
			if r.Content == "eyes" {
				t.Log("received eyes reaction acknowledgement")
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("timed out waiting for eyes reaction acknowledgement")
}

func TestGitHubPRReviewResponse(t *testing.T) {
	prNum := 5
	reviewID := int64(200)

	payload := buildPullRequestReviewPayload(testOwner, testRepo, prNum, reviewID,
		fmt.Sprintf("@%s Write a function called Multiply that returns the product of two integers and commit it to a new branch", botUsername),
		"testuser", "commented", "feature-branch", 99)
	sendWebhook(t, "pull_request_review", payload)

	comment, ok := fakeGH.waitForIssueComment(testOwner, testRepo, prNum, pollTimeout)
	if !ok {
		t.Fatal("timed out waiting for PR review response")
	}
	if comment.Body == "" {
		t.Fatal("expected non-empty response comment")
	}
	if !strings.Contains(comment.Body, "Task completed successfully by system test sandbox") {
		t.Errorf("response should contain sandbox success message, got: %s", comment.Body)
	}
	t.Logf("received response: %s", comment.Body)
}

func TestGitHubConversationHistory(t *testing.T) {
	issueNum := 10

	payload := buildIssueCommentPayload(testOwner, testRepo, issueNum, 100,
		fmt.Sprintf("@%s My name is SystemTestUser", botUsername), "testuser", 99)
	sendWebhook(t, "issue_comment", payload)

	first, ok := fakeGH.waitForIssueComment(testOwner, testRepo, issueNum, pollTimeout)
	if !ok {
		t.Fatal("timed out waiting for first response")
	}
	t.Logf("first response: %s", first.Body)

	payload = buildIssueCommentPayload(testOwner, testRepo, issueNum, 101,
		fmt.Sprintf("@%s What is my name?", botUsername), "testuser", 99)
	sendWebhook(t, "issue_comment", payload)

	second, ok := fakeGH.waitForNthIssueComment(testOwner, testRepo, issueNum, 2, pollTimeout)
	if !ok {
		t.Fatal("timed out waiting for second response")
	}
	if !strings.Contains(second.Body, "SystemTestUser") {
		t.Errorf("second response should contain 'SystemTestUser' (proving conversation history), got: %s", second.Body)
	}
	t.Logf("second response: %s", second.Body)
}

type configParams struct {
	DatabaseURL   string
	OllamaURL     string
	Model         string
	SandboxImage  string
	PrivateKey    string
	WebhookSecret string
	FakeGitHubURL string
}

func renderConfig(params configParams) (string, error) {
	tmpl, err := template.ParseFiles("testdata/config.yaml.tmpl")
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	f, err := os.CreateTemp("", "systemtest-config-*.yaml")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, params); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("executing template: %w", err)
	}

	return f.Name(), nil
}

func buildBinary() (string, error) {
	f, err := os.CreateTemp("", "ai-coworker-systemtest-*")
	if err != nil {
		return "", err
	}
	_ = f.Close()

	cmd := exec.Command("go", "build", "-o", f.Name(), "../../cmd/ai-coworker")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("building binary: %w", err)
	}

	return f.Name(), nil
}

func startBinary(ctx context.Context, binaryPath, configPath string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, binaryPath, "--config", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting binary: %w", err)
	}
	return cmd, nil
}

func waitForReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://%s/webhook/github", addr)

	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("binary not ready after %s", timeout)
}

func generateRSAKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), nil
}

func buildIssueCommentPayload(owner, repo string, issueNum int, commentID int64, body, user string, installationID int64) []byte {
	payload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": issueNum,
			"user": map[string]interface{}{
				"login": "someone-else",
			},
		},
		"comment": map[string]interface{}{
			"id":   commentID,
			"body": body,
			"user": map[string]interface{}{
				"login": user,
			},
		},
		"repository": map[string]interface{}{
			"full_name": fmt.Sprintf("%s/%s", owner, repo),
		},
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

func buildPullRequestReviewPayload(owner, repo string, prNum int, reviewID int64, body, user, state, branch string, installationID int64) []byte {
	payload := map[string]interface{}{
		"action": "submitted",
		"review": map[string]interface{}{
			"id":    reviewID,
			"body":  body,
			"state": state,
			"user": map[string]interface{}{
				"login": user,
			},
		},
		"pull_request": map[string]interface{}{
			"number": prNum,
			"head": map[string]interface{}{
				"ref": branch,
			},
		},
		"repository": map[string]interface{}{
			"full_name": fmt.Sprintf("%s/%s", owner, repo),
		},
		"installation": map[string]interface{}{
			"id": installationID,
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

func sendWebhook(t *testing.T, eventType string, payload []byte) {
	t.Helper()

	sig := computeSignature([]byte(webhookSecret), payload)

	url := fmt.Sprintf("http://%s/webhook/github", adapterAddr)
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("creating webhook request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("test-%d", time.Now().UnixNano()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("webhook returned status %d: %s", resp.StatusCode, string(body))
	}
}

func computeSignature(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func resetDatabase(databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parsing database URL: %w", err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return fmt.Errorf("no database name in URL")
	}

	// Connect to the default "postgres" database to drop/create the target.
	u.Path = "/postgres"
	adminDB, err := sql.Open("pgx", u.String())
	if err != nil {
		return fmt.Errorf("connecting to admin database: %w", err)
	}
	defer adminDB.Close()

	// Terminate existing connections before dropping.
	_, _ = adminDB.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", dbName)
	if _, err := adminDB.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName)); err != nil {
		return fmt.Errorf("dropping database: %w", err)
	}
	if _, err := adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s", dbName)); err != nil {
		return fmt.Errorf("creating database: %w", err)
	}
	return nil
}

func indentPEM(pem, indent string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(pem, "\n"), "\n") {
		lines = append(lines, indent+line)
	}
	return strings.Join(lines, "\n")
}

func newFakeGitHubForMain() *fakeGitHub {
	fg := &fakeGitHub{
		issueComments:  make(map[string][]issueComment),
		prComments:     make(map[string][]issueComment),
		reactions:      make(map[string][]reaction),
		nextCommentID:  1000,
		nextReactionID: 1,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /app", fg.handleGetApp)
	mux.HandleFunc("POST /app/installations/{installationID}/access_tokens", fg.handleCreateAccessToken)
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", fg.handleGetInstallation)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{issueNum}/comments", fg.handleCreateIssueComment)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls/{prNum}/comments", fg.handleCreatePRComment)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{prNum}/reviews/{reviewID}/comments", fg.handleListReviewComments)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/comments/{commentID}", fg.handleGetPRComment)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls/comments/{commentID}/reactions", fg.handleCreatePRCommentReaction)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/comments/{commentID}/reactions", fg.handleCreateIssueCommentReaction)

	fg.server = httptest.NewServer(mux)
	return fg
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
