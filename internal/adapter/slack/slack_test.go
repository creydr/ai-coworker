package slack

import (
	"strings"
	"testing"

	slacklib "github.com/slack-go/slack"
)

func TestFormatThreadContext_IncludesAllMessages(t *testing.T) {
	msgs := []slacklib.Message{
		{Msg: slacklib.Msg{User: "U001", Text: "can you summarize PR #18?", Timestamp: "1000.0001"}},
		{Msg: slacklib.Msg{User: "", Text: "Here is the summary...", Timestamp: "1000.0002"}},
		{Msg: slacklib.Msg{User: "U001", Text: "which items are open?", Timestamp: "1000.0003"}},
		{Msg: slacklib.Msg{User: "U001", Text: "<@BOTID> ^", Timestamp: "1000.0004"}},
	}

	result := formatThreadContext(msgs, "1000.0004")

	if result == "" {
		t.Fatal("expected non-empty context")
	}
	if !strings.Contains(result, "U001: can you summarize PR #18?") {
		t.Error("should include user messages")
	}
	if !strings.Contains(result, "Bot: Here is the summary...") {
		t.Error("should include bot messages (user field empty)")
	}
	if !strings.Contains(result, "U001: which items are open?") {
		t.Error("should include non-mentioned user messages")
	}
	if strings.Contains(result, "<@BOTID> ^") {
		t.Error("should exclude the current message")
	}
}

func TestFormatThreadContext_ExcludesCurrentMessage(t *testing.T) {
	msgs := []slacklib.Message{
		{Msg: slacklib.Msg{User: "U001", Text: "hello", Timestamp: "1000.0001"}},
		{Msg: slacklib.Msg{User: "U001", Text: "current msg", Timestamp: "1000.0002"}},
	}

	result := formatThreadContext(msgs, "1000.0002")

	if !strings.Contains(result, "U001: hello") {
		t.Error("should include earlier message")
	}
	if strings.Contains(result, "current msg") {
		t.Error("should exclude current message")
	}
}

func TestFormatThreadContext_Empty(t *testing.T) {
	msgs := []slacklib.Message{
		{Msg: slacklib.Msg{User: "U001", Text: "the only message", Timestamp: "1000.0001"}},
	}

	result := formatThreadContext(msgs, "1000.0001")

	if result != "" {
		t.Errorf("expected empty string when only message is current, got %q", result)
	}
}

func TestFormatThreadContext_PreservesOrder(t *testing.T) {
	msgs := []slacklib.Message{
		{Msg: slacklib.Msg{User: "U001", Text: "first", Timestamp: "1000.0001"}},
		{Msg: slacklib.Msg{User: "U002", Text: "second", Timestamp: "1000.0002"}},
		{Msg: slacklib.Msg{User: "U001", Text: "third", Timestamp: "1000.0003"}},
		{Msg: slacklib.Msg{User: "U001", Text: "current", Timestamp: "1000.0004"}},
	}

	result := formatThreadContext(msgs, "1000.0004")

	firstIdx := indexOf(result, "first")
	secondIdx := indexOf(result, "second")
	thirdIdx := indexOf(result, "third")

	if firstIdx == -1 || secondIdx == -1 || thirdIdx == -1 {
		t.Fatalf("missing messages in context: %q", result)
	}
	if firstIdx >= secondIdx || secondIdx >= thirdIdx {
		t.Errorf("messages not in chronological order: %q", result)
	}
}

func TestFormatThreadContext_SkipsEmptyText(t *testing.T) {
	msgs := []slacklib.Message{
		{Msg: slacklib.Msg{User: "U001", Text: "", Timestamp: "1000.0001"}},
		{Msg: slacklib.Msg{User: "U002", Text: "has content", Timestamp: "1000.0002"}},
		{Msg: slacklib.Msg{User: "U001", Text: "current", Timestamp: "1000.0003"}},
	}

	result := formatThreadContext(msgs, "1000.0003")

	if strings.Contains(result, "U001: \n") {
		t.Error("should skip messages with empty text")
	}
	if !strings.Contains(result, "U002: has content") {
		t.Error("should include messages with content")
	}
}

func indexOf(s, substr string) int {
	return strings.Index(s, substr)
}
