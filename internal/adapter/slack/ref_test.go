package slack

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
)

func TestNewRef(t *testing.T) {
	ref := NewRef("C12345", "1234567890.123456")

	if ref.Channel != "slack" {
		t.Errorf("Channel = %q, want %q", ref.Channel, "slack")
	}
	if ref.ThreadKey != "C12345/1234567890.123456" {
		t.Errorf("ThreadKey = %q, want %q", ref.ThreadKey, "C12345/1234567890.123456")
	}
	if ref.Properties["channel_id"] != "C12345" {
		t.Errorf("Properties[channel_id] = %q, want %q", ref.Properties["channel_id"], "C12345")
	}
	if ref.Properties["thread_ts"] != "1234567890.123456" {
		t.Errorf("Properties[thread_ts] = %q, want %q", ref.Properties["thread_ts"], "1234567890.123456")
	}
}

func TestParseRef(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:   "slack",
		ThreadKey: "C12345/1234567890.123456",
		Properties: map[string]string{
			"channel_id": "C12345",
			"thread_ts":  "1234567890.123456",
		},
	}

	parsed := ParseRef(ref)

	if parsed.ChannelID != "C12345" {
		t.Errorf("ChannelID = %q, want %q", parsed.ChannelID, "C12345")
	}
	if parsed.ThreadTS != "1234567890.123456" {
		t.Errorf("ThreadTS = %q, want %q", parsed.ThreadTS, "1234567890.123456")
	}
}

func TestParseRef_NilProperties(t *testing.T) {
	ref := domain.ChannelRef{
		Channel:    "slack",
		ThreadKey:  "C12345/1234567890.123456",
		Properties: nil,
	}

	// Should not panic.
	parsed := ParseRef(ref)

	if parsed.ChannelID != "" {
		t.Errorf("ChannelID = %q, want empty string", parsed.ChannelID)
	}
	if parsed.ThreadTS != "" {
		t.Errorf("ThreadTS = %q, want empty string", parsed.ThreadTS)
	}
}

func TestNewRef_Roundtrip(t *testing.T) {
	ref := NewRef("C99999", "9999999999.999999")
	parsed := ParseRef(ref)

	if parsed.ChannelID != "C99999" {
		t.Errorf("ChannelID = %q, want %q", parsed.ChannelID, "C99999")
	}
	if parsed.ThreadTS != "9999999999.999999" {
		t.Errorf("ThreadTS = %q, want %q", parsed.ThreadTS, "9999999999.999999")
	}
}
