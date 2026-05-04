package openai

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

func TestNew(t *testing.T) {
	p := New("https://example.com/v1", "test-key", "test-model")
	if p.model != "test-model" {
		t.Fatalf("expected model test-model, got %s", p.model)
	}
	if p.client == nil {
		t.Fatal("expected non-nil client")
	}
}
