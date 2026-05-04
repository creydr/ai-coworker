package github

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
)

func TestNewRef(t *testing.T) {
	ref := NewRef("myorg/myrepo", 42)

	if ref.Channel != "github" {
		t.Errorf("Channel = %q, want %q", ref.Channel, "github")
	}
	if ref.ThreadKey != "myorg/myrepo#42" {
		t.Errorf("ThreadKey = %q, want %q", ref.ThreadKey, "myorg/myrepo#42")
	}
	if ref.Properties["repo"] != "myorg/myrepo" {
		t.Errorf("Properties[repo] = %q, want %q", ref.Properties["repo"], "myorg/myrepo")
	}
	if ref.Properties["issue_num"] != "42" {
		t.Errorf("Properties[issue_num] = %q, want %q", ref.Properties["issue_num"], "42")
	}
}

func TestWithComment(t *testing.T) {
	ref := NewRef("org/repo", 7)
	ref = WithComment(ref, 12345, "review_comment")

	if ref.Properties["comment_id"] != "12345" {
		t.Errorf("Properties[comment_id] = %q, want %q", ref.Properties["comment_id"], "12345")
	}
	if ref.Properties["comment_type"] != "review_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", ref.Properties["comment_type"], "review_comment")
	}
	// Original properties should still be present.
	if ref.Properties["repo"] != "org/repo" {
		t.Errorf("Properties[repo] = %q, want %q", ref.Properties["repo"], "org/repo")
	}
	if ref.Properties["issue_num"] != "7" {
		t.Errorf("Properties[issue_num] = %q, want %q", ref.Properties["issue_num"], "7")
	}
}

func TestWithComment_NilProperties(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:    "github",
		ThreadKey:  "org/repo#1",
		Properties: nil,
	}
	ref = WithComment(ref, 999, "issue_comment")

	if ref.Properties == nil {
		t.Fatal("Properties should be initialized, got nil")
	}
	if ref.Properties["comment_id"] != "999" {
		t.Errorf("Properties[comment_id] = %q, want %q", ref.Properties["comment_id"], "999")
	}
	if ref.Properties["comment_type"] != "issue_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", ref.Properties["comment_type"], "issue_comment")
	}
}

func TestParseRef(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:   "github",
		ThreadKey: "myorg/myrepo#42",
		Properties: map[string]string{
			"repo":         "myorg/myrepo",
			"issue_num":    "42",
			"comment_id":   "99999",
			"comment_type": "issue_comment",
		},
	}

	parsed := ParseRef(ref)

	if parsed.Repo != "myorg/myrepo" {
		t.Errorf("Repo = %q, want %q", parsed.Repo, "myorg/myrepo")
	}
	if parsed.IssueNum != 42 {
		t.Errorf("IssueNum = %d, want %d", parsed.IssueNum, 42)
	}
	if parsed.CommentID != 99999 {
		t.Errorf("CommentID = %d, want %d", parsed.CommentID, 99999)
	}
	if parsed.CommentType != "issue_comment" {
		t.Errorf("CommentType = %q, want %q", parsed.CommentType, "issue_comment")
	}
}

func TestParseRef_InvalidNumbers(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:   "github",
		ThreadKey: "org/repo#abc",
		Properties: map[string]string{
			"repo":       "org/repo",
			"issue_num":  "not-a-number",
			"comment_id": "also-not-a-number",
		},
	}

	parsed := ParseRef(ref)

	if parsed.Repo != "org/repo" {
		t.Errorf("Repo = %q, want %q", parsed.Repo, "org/repo")
	}
	if parsed.IssueNum != 0 {
		t.Errorf("IssueNum = %d, want %d (zero value on parse failure)", parsed.IssueNum, 0)
	}
	if parsed.CommentID != 0 {
		t.Errorf("CommentID = %d, want %d (zero value on parse failure)", parsed.CommentID, 0)
	}
}

func TestParseRef_NilProperties(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:    "github",
		ThreadKey:  "org/repo#1",
		Properties: nil,
	}

	// Should not panic.
	parsed := ParseRef(ref)

	if parsed.Repo != "" {
		t.Errorf("Repo = %q, want empty string", parsed.Repo)
	}
	if parsed.IssueNum != 0 {
		t.Errorf("IssueNum = %d, want 0", parsed.IssueNum)
	}
	if parsed.CommentID != 0 {
		t.Errorf("CommentID = %d, want 0", parsed.CommentID)
	}
}

func TestNewRef_Roundtrip(t *testing.T) {
	ref := NewRef("myorg/myrepo", 100)
	ref = WithComment(ref, 55555, "review_comment")
	parsed := ParseRef(ref)

	if parsed.Repo != "myorg/myrepo" {
		t.Errorf("Repo = %q, want %q", parsed.Repo, "myorg/myrepo")
	}
	if parsed.IssueNum != 100 {
		t.Errorf("IssueNum = %d, want %d", parsed.IssueNum, 100)
	}
	if parsed.CommentID != 55555 {
		t.Errorf("CommentID = %d, want %d", parsed.CommentID, 55555)
	}
	if parsed.CommentType != "review_comment" {
		t.Errorf("CommentType = %q, want %q", parsed.CommentType, "review_comment")
	}
}
