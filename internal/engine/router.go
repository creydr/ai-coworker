package engine

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

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

// HandleEvent processes an incoming event: acknowledges receipt, ensures a
// thread exists, records the user message, and creates a pending task.
func (r *Router) HandleEvent(ctx context.Context, event domain.IncomingEvent) error {
	// Acknowledge the event if an adapter is available.
	if a := r.adapters[event.Channel]; a != nil {
		if err := a.Acknowledge(ctx, event.ChannelRef); err != nil {
			log.Printf("failed to acknowledge event on %s: %v", event.Channel, err)
		}
	}

	// Normalise the ChannelRef so that GetThreadByChannelRef works for all
	// adapters. The store looks up threads by (channel, channel_id, thread_ts).
	// Slack populates ChannelID and ThreadTS directly. GitHub populates Repo
	// and IssueNum instead, so we map those into ChannelID and ThreadTS.
	ref := event.ChannelRef
	if ref.Channel == "github" {
		if ref.ChannelID == "" {
			ref.ChannelID = ref.Repo
		}
		if ref.ThreadTS == "" && ref.IssueNum != 0 {
			ref.ThreadTS = strconv.Itoa(ref.IssueNum)
		}
		event.ChannelRef = ref
	}

	// Look up or create the thread.
	thread, err := r.store.GetThreadByChannelRef(ctx, ref.Channel, ref.ChannelID, ref.ThreadTS)
	if err != nil {
		if !strings.Contains(err.Error(), "thread not found") {
			return fmt.Errorf("looking up thread: %w", err)
		}

		// Thread does not exist yet — create one.
		thread = &domain.Thread{
			ChannelRef: event.ChannelRef,
			Status:     domain.ThreadActive,
		}
		if err := r.store.CreateThread(ctx, thread); err != nil {
			return fmt.Errorf("creating thread: %w", err)
		}
	}

	// Record the user message.
	msg := &domain.Message{
		ThreadID: thread.ID,
		Role:     domain.RoleUser,
		Content:  event.Content,
	}
	if err := r.store.CreateMessage(ctx, msg); err != nil {
		return fmt.Errorf("creating message: %w", err)
	}

	// Create a pending task for the worker pool to pick up.
	task := &domain.Task{
		ThreadID: thread.ID,
		Status:   domain.TaskPending,
		Input:    event.Content,
		Metadata: event.Metadata,
	}
	if err := r.store.CreateTask(ctx, task); err != nil {
		return fmt.Errorf("creating task: %w", err)
	}

	return nil
}
