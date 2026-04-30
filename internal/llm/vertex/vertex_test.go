package vertex

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/llm"
)

func TestConvertMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
		{Role: llm.RoleAssistant, Content: "hi there"},
		{Role: llm.RoleUser, Content: "how are you?"},
	}

	params := convertMessages(messages)
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}
}
