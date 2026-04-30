package domain

import "time"

type Thread struct {
	ID         string
	ChannelRef ChannelRef
	Status     ThreadStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadResolved ThreadStatus = "resolved"
	ThreadExpired  ThreadStatus = "expired"
)
