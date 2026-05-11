package llmexec

import (
	"context"
	"fmt"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/llm"
)

// Executor runs tasks by sending conversation messages to an LLM provider
type Executor struct {
	provider llm.Provider
}

// New creates a new LLM executor using the given provider
func New(provider llm.Provider) *Executor {
	return &Executor{
		provider: provider,
	}
}

func (e *Executor) Execute(ctx context.Context, execCtx *executor.Context) (*executor.Result, error) {
	var messages []llm.Message

	systemPrompt := "You are a helpful AI coworker. Assist with questions, discussions, and reviews related to software development. Be concise and helpful."

	if execCtx.Event != nil && execCtx.Event.Metadata != nil {
		if docCtx := execCtx.Event.Metadata["document_context"]; docCtx != "" {
			systemPrompt += "\n\nDocument content:\n" + docCtx
		}
	}

	messages = append(messages, llm.Message{
		Role:    domain.RoleSystem,
		Content: systemPrompt,
	})

	for _, msg := range execCtx.Messages {
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	if execCtx.Task != nil && execCtx.Task.Input != "" {
		messages = append(messages, llm.Message{
			Role:    domain.RoleUser,
			Content: execCtx.Task.Input,
		})
	}

	response, err := e.provider.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("LLM chat failed: %w", err)
	}

	return &executor.Result{
		Response: response,
	}, nil
}
