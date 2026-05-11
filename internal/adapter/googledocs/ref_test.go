package googledocs

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
)

func TestNewRef(t *testing.T) {
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

func TestParseRef(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:   "googledocs",
		ThreadKey: "doc123#comment456",
		Properties: map[string]string{
			"document_id": "doc123",
			"comment_id":  "comment456",
		},
	}

	parsed := ParseRef(ref)

	if parsed.DocumentID != "doc123" {
		t.Errorf("DocumentID = %q, want %q", parsed.DocumentID, "doc123")
	}
	if parsed.CommentID != "comment456" {
		t.Errorf("CommentID = %q, want %q", parsed.CommentID, "comment456")
	}
}

func TestParseRef_NilProperties(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:    "googledocs",
		ThreadKey:  "doc123#comment456",
		Properties: nil,
	}

	parsed := ParseRef(ref)

	if parsed.DocumentID != "" {
		t.Errorf("DocumentID = %q, want empty", parsed.DocumentID)
	}
	if parsed.CommentID != "" {
		t.Errorf("CommentID = %q, want empty", parsed.CommentID)
	}
}

func TestNewRef_Roundtrip(t *testing.T) {
	ref := NewRef("docABC", "commentXYZ")
	parsed := ParseRef(ref)

	if parsed.DocumentID != "docABC" {
		t.Errorf("DocumentID = %q, want %q", parsed.DocumentID, "docABC")
	}
	if parsed.CommentID != "commentXYZ" {
		t.Errorf("CommentID = %q, want %q", parsed.CommentID, "commentXYZ")
	}
}
