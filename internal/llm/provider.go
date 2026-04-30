package llm

import "context"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type Provider interface {
	Chat(ctx context.Context, messages []Message) (string, error)
}
