//go:build systemtest

package systemtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// issueComment represents a comment posted via the fake GitHub API.
type issueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// reaction represents a reaction (e.g. "eyes") created via the fake GitHub API.
type reaction struct {
	ID      int64  `json:"id"`
	Content string `json:"content"`
}

// fakeGitHub is an in-memory GitHub API server that records comments and reactions
// posted by the adapter, allowing tests to assert on the responses.
type fakeGitHub struct {
	server *httptest.Server

	mu             sync.Mutex
	issueComments  map[string][]issueComment
	prComments     map[string][]issueComment
	reactions      map[string][]reaction
	nextCommentID  int64
	nextReactionID int64
}

func (fg *fakeGitHub) url() string {
	return fg.server.URL
}

// handleGetApp returns a minimal GitHub App descriptor (GET /app).
func (fg *fakeGitHub) handleGetApp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":   12345,
		"name": "test-bot",
		"slug": "test-bot",
	})
}

// handleCreateAccessToken returns a fake installation access token (POST /app/installations/{id}/access_tokens).
func (fg *fakeGitHub) handleCreateAccessToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token":      "ghs_fake_installation_token",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

// handleGetInstallation returns a fake installation for the repository (GET /repos/{owner}/{repo}/installation).
func (fg *fakeGitHub) handleGetInstallation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          99,
		"app_id":      12345,
		"target_type": "Organization",
	})
}

// handleCreateIssueComment stores a comment posted to an issue or PR (POST /repos/{owner}/{repo}/issues/{num}/comments).
func (fg *fakeGitHub) handleCreateIssueComment(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	issueNum := r.PathValue("issueNum")

	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fg.mu.Lock()
	defer fg.mu.Unlock()

	comment := issueComment{
		ID:        fg.nextCommentID,
		Body:      body.Body,
		CreatedAt: time.Now().UTC(),
	}
	fg.nextCommentID++

	key := fmt.Sprintf("%s/%s/issues/%s", owner, repo, issueNum)
	fg.issueComments[key] = append(fg.issueComments[key], comment)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         comment.ID,
		"body":       comment.Body,
		"created_at": comment.CreatedAt.Format(time.RFC3339),
	})
}

// handleCreatePRComment stores a review comment on a PR (POST /repos/{owner}/{repo}/pulls/{num}/comments).
func (fg *fakeGitHub) handleCreatePRComment(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	prNum := r.PathValue("prNum")

	var body struct {
		Body      string `json:"body"`
		InReplyTo int64  `json:"in_reply_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fg.mu.Lock()
	defer fg.mu.Unlock()

	comment := issueComment{
		ID:        fg.nextCommentID,
		Body:      body.Body,
		CreatedAt: time.Now().UTC(),
	}
	fg.nextCommentID++

	key := fmt.Sprintf("%s/%s/pulls/%s", owner, repo, prNum)
	fg.prComments[key] = append(fg.prComments[key], comment)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         comment.ID,
		"body":       comment.Body,
		"created_at": comment.CreatedAt.Format(time.RFC3339),
	})
}

// handleListReviewComments returns an empty list of inline review comments (GET /repos/{owner}/{repo}/pulls/{num}/reviews/{id}/comments).
func (fg *fakeGitHub) handleListReviewComments(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []interface{}{})
}

// handleGetPRComment returns a stub PR comment (GET /repos/{owner}/{repo}/pulls/comments/{id}).
func (fg *fakeGitHub) handleGetPRComment(w http.ResponseWriter, r *http.Request) {
	commentID := r.PathValue("commentID")
	id, _ := strconv.ParseInt(commentID, 10, 64)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":   id,
		"body": "parent comment",
		"line": 10,
	})
}

// handleCreatePRCommentReaction records a reaction on a PR comment.
func (fg *fakeGitHub) handleCreatePRCommentReaction(w http.ResponseWriter, r *http.Request) {
	fg.createReaction(w, r, "pr_comment")
}

// handleCreateIssueCommentReaction records a reaction on an issue comment.
func (fg *fakeGitHub) handleCreateIssueCommentReaction(w http.ResponseWriter, r *http.Request) {
	fg.createReaction(w, r, "issue_comment")
}

// createReaction is the shared implementation for PR and issue comment reaction endpoints.
func (fg *fakeGitHub) createReaction(w http.ResponseWriter, r *http.Request, commentType string) {
	commentID := r.PathValue("commentID")

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fg.mu.Lock()
	defer fg.mu.Unlock()

	rx := reaction{
		ID:      fg.nextReactionID,
		Content: body.Content,
	}
	fg.nextReactionID++

	key := fmt.Sprintf("%s/%s", commentType, commentID)
	fg.reactions[key] = append(fg.reactions[key], rx)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      rx.ID,
		"content": rx.Content,
	})
}

// getIssueComments returns all comments recorded for the given issue.
func (fg *fakeGitHub) getIssueComments(owner, repo string, issueNum int) []issueComment {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	key := fmt.Sprintf("%s/%s/issues/%d", owner, repo, issueNum)
	return fg.issueComments[key]
}

// getReactions returns all reactions recorded for the given comment.
func (fg *fakeGitHub) getReactions(commentType string, commentID int64) []reaction {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	key := fmt.Sprintf("%s/%d", commentType, commentID)
	return fg.reactions[key]
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// waitForIssueComment polls until the first non-empty comment appears on the given issue.
func (fg *fakeGitHub) waitForIssueComment(owner, repo string, issueNum int, timeout time.Duration) (issueComment, bool) {
	return fg.waitForNthIssueComment(owner, repo, issueNum, 1, timeout)
}

// waitForNthIssueComment polls until at least n non-empty comments exist, then returns the nth.
func (fg *fakeGitHub) waitForNthIssueComment(owner, repo string, issueNum, n int, timeout time.Duration) (issueComment, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		comments := fg.getIssueComments(owner, repo, issueNum)
		count := 0
		for _, c := range comments {
			if strings.TrimSpace(c.Body) != "" {
				count++
				if count == n {
					return c, true
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return issueComment{}, false
}
