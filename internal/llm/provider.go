package llm

import (
	"context"

	"github.com/creydr/ai-coworker/internal/domain"
)

type Message struct {
	Role    domain.Role
	Content string
}

type Provider interface {
	Chat(ctx context.Context, messages []Message) (string, error)
}
