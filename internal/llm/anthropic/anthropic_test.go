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

	systemBlocks, params := convertMessages(messages)
	if len(systemBlocks) != 0 {
		t.Fatalf("expected 0 system blocks, got %d", len(systemBlocks))
	}
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}
}

func TestConvertMessagesWithSystemRole(t *testing.T) {
	messages := []llm.Message{
		{Role: domain.RoleSystem, Content: "You are a helpful assistant."},
		{Role: domain.RoleUser, Content: "hello"},
		{Role: domain.RoleAssistant, Content: "hi there"},
	}

	systemBlocks, params := convertMessages(messages)
	if len(systemBlocks) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(systemBlocks))
	}
	if systemBlocks[0].Text != "You are a helpful assistant." {
		t.Fatalf("expected system text 'You are a helpful assistant.', got %q", systemBlocks[0].Text)
	}
	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(params))
	}
}
