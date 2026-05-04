package anthropic

import (
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/llm"
)

func TestConvertMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: domain.RoleUser, Content: "hello"},
		{Role: domain.RoleAssistant, Content: "hi there"},
		{Role: domain.RoleUser, Content: "how are you?"},
	}

	params := convertMessages(messages)
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}
}
