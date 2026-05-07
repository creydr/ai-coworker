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

func TestParseRef_MessageTS(t *testing.T) {
	ref := NewRef("C12345", "1000000000.000000")
	ref.Properties["message_ts"] = "1000000099.000000"

	parsed := ParseRef(ref)

	if parsed.ThreadTS != "1000000000.000000" {
		t.Errorf("ThreadTS = %q, want %q", parsed.ThreadTS, "1000000000.000000")
	}
	if parsed.MessageTS != "1000000099.000000" {
		t.Errorf("MessageTS = %q, want %q", parsed.MessageTS, "1000000099.000000")
	}
}

func TestAcknowledgeTimestamp(t *testing.T) {
	tests := []struct {
		name   string
		ref    domain.ChannelRef
		wantTS string
	}{
		{
			name: "follow-up message uses message_ts",
			ref: domain.ChannelRef{
				Properties: map[string]string{
					"channel_id": "C123",
					"thread_ts":  "1000.0000",
					"message_ts": "2000.0000",
				},
			},
			wantTS: "2000.0000",
		},
		{
			name: "initial message falls back to thread_ts",
			ref: domain.ChannelRef{
				Properties: map[string]string{
					"channel_id": "C123",
					"thread_ts":  "1000.0000",
				},
			},
			wantTS: "1000.0000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ParseRef(tt.ref)
			ts := s.MessageTS
			if ts == "" {
				ts = s.ThreadTS
			}
			if ts != tt.wantTS {
				t.Errorf("acknowledge timestamp = %q, want %q", ts, tt.wantTS)
			}
		})
	}
}
