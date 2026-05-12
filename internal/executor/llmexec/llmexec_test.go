package llmexec

import (
	"context"
	"errors"
	"testing"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/llm"
)

type mockProvider struct {
	response string
	err      error
	captured []llm.Message
}

func (m *mockProvider) Chat(_ context.Context, messages []llm.Message) (string, error) {
	m.captured = messages
	return m.response, m.err
}

func TestExecute_BasicConversation(t *testing.T) {
	provider := &mockProvider{response: "hello back"}
	exec := New(provider)

	result, err := exec.Execute(context.Background(), &executor.Context{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "previous message"},
		},
		Task: &domain.Task{Input: "hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Response != "hello back" {
		t.Errorf("Response = %q, want %q", result.Response, "hello back")
	}

	if len(provider.captured) != 3 {
		t.Fatalf("len(messages) = %d, want 3 (system + history + task input)", len(provider.captured))
	}
	if provider.captured[0].Role != domain.RoleSystem {
		t.Errorf("messages[0].Role = %q, want %q", provider.captured[0].Role, domain.RoleSystem)
	}
	if provider.captured[1].Role != domain.RoleUser {
		t.Errorf("messages[1].Role = %q, want %q", provider.captured[1].Role, domain.RoleUser)
	}
	if provider.captured[1].Content != "previous message" {
		t.Errorf("messages[1].Content = %q, want %q", provider.captured[1].Content, "previous message")
	}
	if provider.captured[2].Role != domain.RoleUser {
		t.Errorf("messages[2].Role = %q, want %q", provider.captured[2].Role, domain.RoleUser)
	}
	if provider.captured[2].Content != "hello" {
		t.Errorf("messages[2].Content = %q, want %q", provider.captured[2].Content, "hello")
	}
}

func TestExecute_ProviderError(t *testing.T) {
	providerErr := errors.New("rate limited")
	provider := &mockProvider{err: providerErr}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Task: &domain.Task{Input: "hello"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, providerErr) {
		t.Errorf("error should wrap provider error, got: %v", err)
	}
}

func TestExecute_NoTaskInput(t *testing.T) {
	provider := &mockProvider{response: "ok"}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "hi"},
		},
		Task: &domain.Task{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.captured) != 2 {
		t.Errorf("len(messages) = %d, want 2 (system + history only)", len(provider.captured))
	}
}

func TestExecute_NilTask(t *testing.T) {
	provider := &mockProvider{response: "ok"}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.captured) != 2 {
		t.Errorf("len(messages) = %d, want 2 (system + history only)", len(provider.captured))
	}
}

func TestExecute_DocumentContext(t *testing.T) {
	provider := &mockProvider{response: "review done"}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Event: &domain.IncomingEvent{
			Metadata: map[string]string{
				"comment_thread":   "[Alice]: Please fix the typo\n",
				"quoted_text":      "teh quick brown fox",
				"document_context": "=== DOCUMENT ===\nThe quick brown fox jumps over the lazy dog.",
			},
		},
		Task: &domain.Task{Input: "fix it"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	systemPrompt := provider.captured[0].Content
	if provider.captured[0].Role != domain.RoleSystem {
		t.Fatalf("first message role = %q, want %q", provider.captured[0].Role, domain.RoleSystem)
	}

	checks := []string{
		"YOUR TASK",
		"teh quick brown fox",
		"[Alice]: Please fix the typo",
		"=== DOCUMENT ===",
	}
	for _, check := range checks {
		if !contains(systemPrompt, check) {
			t.Errorf("system prompt missing %q", check)
		}
	}
}

func TestExecute_NilEvent(t *testing.T) {
	provider := &mockProvider{response: "ok"}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Task: &domain.Task{Input: "hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.captured) != 2 {
		t.Errorf("len(messages) = %d, want 2", len(provider.captured))
	}
}

func TestExecute_EmptyMessages(t *testing.T) {
	provider := &mockProvider{response: "ok"}
	exec := New(provider)

	_, err := exec.Execute(context.Background(), &executor.Context{
		Task: &domain.Task{Input: "hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.captured) != 2 {
		t.Errorf("len(messages) = %d, want 2 (system + task input)", len(provider.captured))
	}
}

func TestExecute_MultipleHistoryMessages(t *testing.T) {
	provider := &mockProvider{response: "final answer"}
	exec := New(provider)

	history := []domain.Message{
		{Role: domain.RoleUser, Content: "first"},
		{Role: domain.RoleAssistant, Content: "reply"},
		{Role: domain.RoleUser, Content: "followup"},
	}

	_, err := exec.Execute(context.Background(), &executor.Context{
		Messages: history,
		Task:     &domain.Task{Input: "new question"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(provider.captured) != 5 {
		t.Fatalf("len(messages) = %d, want 5 (system + 3 history + task)", len(provider.captured))
	}
	for i, msg := range history {
		captured := provider.captured[i+1]
		if captured.Role != msg.Role || captured.Content != msg.Content {
			t.Errorf("messages[%d] = {%s, %q}, want {%s, %q}", i+1, captured.Role, captured.Content, msg.Role, msg.Content)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
