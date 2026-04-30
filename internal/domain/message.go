package domain

import "time"

type Message struct {
	ID        string
	ThreadID  string
	Role      Role
	Content   string
	CreatedAt time.Time
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)
