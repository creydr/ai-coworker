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
	"log"
	"net/http"
	"net/http/httptest"
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
	pollTimeout   = 120 * time.Second
)

var (
	fakeGH     *fakeGitHub
	binaryPath string
	binaryCmd  *exec.Cmd
)

func TestMain(m *testing.M) {
	databaseURL := os.Getenv("SYSTEMTEST_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("SYSTEMTEST_DATABASE_URL is required")
	}

	ollamaURL := os.Getenv("SYSTEMTEST_OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434/v1"
	}

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

func TestGitHubIssueCommentLLMResponse(t *testing.T) {
	issueNum := 1
	commentID := int64(42)

	payload := buildIssueCommentPayload(testOwner, testRepo, issueNum, commentID, fmt.Sprintf("@%s What is 2+2?", botUsername), "testuser", 99)
	sendWebhook(t, "issue_comment", payload)

	comment, ok := fakeGH.waitForIssueComment(testOwner, testRepo, issueNum, pollTimeout)
	if !ok {
		t.Fatal("timed out waiting for GitHub comment response")
	}
	if comment.Body == "" {
		t.Fatal("expected non-empty response comment")
	}
	t.Logf("received response: %s", comment.Body)
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

func TestGitHubCodeTaskSandboxExecution(t *testing.T) {
	issueNum := 3
	commentID := int64(44)

	payload := buildIssueCommentPayload(testOwner, testRepo, issueNum, commentID, fmt.Sprintf("@%s Please fix the bug in main.go and create a PR", botUsername), "testuser", 99)
	sendWebhook(t, "issue_comment", payload)

	comment, ok := fakeGH.waitForIssueComment(testOwner, testRepo, issueNum, pollTimeout)
	if !ok {
		t.Fatal("timed out waiting for GitHub comment response")
	}
	if comment.Body == "" {
		t.Fatal("expected non-empty response comment")
	}
	t.Logf("received response: %s", comment.Body)
}

type configParams struct {
	DatabaseURL   string
	OllamaURL     string
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
		t.Fatalf("webhook returned status %d", resp.StatusCode)
	}
}

func computeSignature(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func resetDatabase(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer db.Close()

	for _, table := range []string{"tasks", "messages", "threads", "adapter_state"} {
		if _, err := db.Exec(fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			// Table may not exist yet (first run); ignore.
		}
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
