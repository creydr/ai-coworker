package adapter

import (
	"context"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/vcs"
)

type EventHandler func(ctx context.Context, events []domain.IncomingEvent) error

type Adapter interface {
	Start(ctx context.Context, handler EventHandler) error
	SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error
	Acknowledge(ctx context.Context, ref domain.ChannelRef) error
	Name() string
}

type VCSAware interface {
	VCSProvider() vcs.Provider
}
