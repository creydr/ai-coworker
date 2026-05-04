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
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)
