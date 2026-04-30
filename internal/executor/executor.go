package executor

import (
	"context"

	"github.com/creydr/ai-coworker/internal/domain"
)

type Context struct {
	Thread   *domain.Thread
	Messages []domain.Message
	Task     *domain.Task
	Event    *domain.IncomingEvent
}

type Result struct {
	Response string
	Metadata map[string]string
}

type Executor interface {
	Execute(ctx context.Context, execCtx *Context) (*Result, error)
}
