package executor

import (
	"context"

	"github.com/creydr/ai-coworker/internal/domain"
)

// Context bundles the state an Executor needs to process a single task.
type Context struct {
	// Thread is the conversation thread this task belongs to.
	Thread *domain.Thread
	// Messages is the ordered conversation history for the thread.
	Messages []domain.Message
	// Task is the claimed task being executed (status already in_progress).
	Task *domain.Task
	// Event is the incoming event that triggered this task. Its Content field
	// may differ from Task.Input when review tasks are batched (merged input).
	Event *domain.IncomingEvent
}

// Result holds the output of an Executor.Execute call.
type Result struct {
	// Response is the text sent back to the user via the originating adapter.
	Response string
	// Metadata carries executor-produced key-value pairs persisted on the task.
	Metadata map[string]string
}

// Executor processes a task and returns a result or error.
type Executor interface {
	Execute(ctx context.Context, execCtx *Context) (*Result, error)
}
