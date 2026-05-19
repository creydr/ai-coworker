package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/store"
)

// Router dispatches incoming events to the correct adapter and manages
// thread/task lifecycle in the store.
type Router struct {
	store    store.Store
	adapters map[string]adapter.Adapter
}

// NewRouter creates a new Router backed by the given store.
func NewRouter(s store.Store) *Router {
	return &Router{
		store:    s,
		adapters: make(map[string]adapter.Adapter),
	}
}

// RegisterAdapter adds an adapter to the router, keyed by its Name().
func (r *Router) RegisterAdapter(a adapter.Adapter) {
	r.adapters[a.Name()] = a
}

// GetAdapter returns the adapter registered under the given name, or nil.
func (r *Router) GetAdapter(name string) adapter.Adapter {
	return r.adapters[name]
}

// HandleEvent processes incoming events: acknowledges receipt, ensures a
// thread exists, records user messages, and creates pending tasks.
// All events are acknowledged first, then all tasks are created back-to-back
// so they become claimable as a group rather than one at a time.
func (r *Router) HandleEvent(ctx context.Context, events []domain.IncomingEvent) error {
	if len(events) == 0 {
		return nil
	}

	ref := events[0].ChannelRef
	for i := 1; i < len(events); i++ {
		e := events[i].ChannelRef
		if e.Channel != ref.Channel || e.ThreadKey != ref.ThreadKey {
			return fmt.Errorf("batch contains mixed threads: event[0]=%s/%s, event[%d]=%s/%s",
				ref.Channel, ref.ThreadKey, i, e.Channel, e.ThreadKey)
		}
	}

	// Acknowledge all events first (HTTP round-trips) before inserting
	// any tasks, so workers can't claim the first task while later
	// acknowledgments are still in flight.
	for _, event := range events {
		if a := r.adapters[event.ChannelRef.Channel]; a != nil {
			if err := a.Acknowledge(ctx, event.ChannelRef); err != nil {
				slog.Warn("failed to acknowledge event", "channel", event.ChannelRef.Channel, "error", err)
			}
		}
	}

	// Look up or create the thread (all events in a batch share the same thread).
	thread, err := r.store.GetThreadByChannelRef(ctx, ref.Channel, ref.ThreadKey)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("looking up thread: %w", err)
		}

		thread = &domain.Thread{
			ChannelRef: events[0].ChannelRef,
			Status:     domain.ThreadActive,
		}
		if err := r.store.CreateThread(ctx, thread); err != nil {
			return fmt.Errorf("creating thread: %w", err)
		}
	}

	// Record messages and create tasks back-to-back without any network
	// calls in between.
	for _, event := range events {
		msg := &domain.Message{
			ThreadID: thread.ID,
			Role:     domain.RoleUser,
			Content:  event.Content,
		}
		if err := r.store.CreateMessage(ctx, msg); err != nil {
			return fmt.Errorf("creating message: %w", err)
		}

		task := &domain.Task{
			ThreadID: thread.ID,
			Status:   domain.TaskPending,
			Input:    event.Content,
			Metadata: event.Metadata,
		}
		if err := r.store.CreateTask(ctx, task); err != nil {
			return fmt.Errorf("creating task: %w", err)
		}
	}

	return nil
}
