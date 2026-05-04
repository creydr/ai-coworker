package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/store"
)

// WorkerPool manages a pool of goroutines that claim and process tasks.
type WorkerPool struct {
	store      store.Store
	router     *Router
	classifier *IntentClassifier
	codeExec   executor.Executor
	llmExec    executor.Executor
	numWorkers int
	wg         sync.WaitGroup
}

// NewWorkerPool creates a new WorkerPool with the given dependencies.
func NewWorkerPool(
	s store.Store,
	router *Router,
	classifier *IntentClassifier,
	codeExec executor.Executor,
	llmExec executor.Executor,
	numWorkers int,
) *WorkerPool {
	return &WorkerPool{
		store:      s,
		router:     router,
		classifier: classifier,
		codeExec:   codeExec,
		llmExec:    llmExec,
		numWorkers: numWorkers,
	}
}

// Start launches numWorkers goroutines that continuously claim and process tasks.
func (wp *WorkerPool) Start(ctx context.Context) {
	for i := 0; i < wp.numWorkers; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			wp.runWorker(ctx, workerID)
		}()
	}
}

// Wait blocks until all worker goroutines have exited.
func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}

func (wp *WorkerPool) runWorker(ctx context.Context, workerID string) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("stopping: context cancelled", "worker", workerID)
			return
		default:
		}

		task, err := wp.store.ClaimNextTask(ctx, workerID)
		if err != nil {
			slog.Error("error claiming task", "worker", workerID, "error", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if task == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		wp.processTask(ctx, workerID, task)
	}
}

func (wp *WorkerPool) processTask(ctx context.Context, workerID string, task *domain.Task) {
	// Load thread and messages.
	thread, err := wp.store.GetThread(ctx, task.ThreadID)
	if err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("loading thread %s: %w", task.ThreadID, err))
		return
	}

	messages, err := wp.store.GetMessages(ctx, task.ThreadID)
	if err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("loading messages for thread %s: %w", task.ThreadID, err))
		return
	}

	event := domain.IncomingEvent{
		Channel:    thread.ChannelRef.Channel,
		ChannelRef: thread.ChannelRef,
		ThreadID:   thread.ID,
		Content:    task.Input,
		Metadata:   task.Metadata,
	}

	// Classify intent.
	intent, err := wp.classifier.Classify(ctx, event, messages)
	if err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("classifying intent: %w", err))
		return
	}

	// Persist the classified intent on the task.
	task.Intent = intent
	if err := wp.store.UpdateTask(ctx, task); err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("updating task intent: %w", err))
		return
	}

	// Select the executor based on intent.
	var exec executor.Executor
	switch intent {
	case domain.IntentCodeTask, domain.IntentReview:
		exec = wp.codeExec
	default:
		exec = wp.llmExec
	}

	// Build executor context and execute.
	execCtx := &executor.Context{
		Thread:   thread,
		Messages: messages,
		Task:     task,
		Event:    &event,
	}

	slog.Info("executing task", "worker", workerID, "task", task.ID, "intent", intent, "thread", thread.ChannelRef.ThreadKey)
	result, err := exec.Execute(ctx, execCtx)
	if err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("executing task: %w", err))
		wp.sendResponse(ctx, thread.ChannelRef, fmt.Sprintf("Sorry, I encountered an error while processing your request: %v", err))
		return
	}
	slog.Info("task completed", "worker", workerID, "task", task.ID, "response_len", len(result.Response))

	// Mark task as completed.
	task.Status = domain.TaskCompleted
	task.Result = result.Response
	if err := wp.store.UpdateTask(ctx, task); err != nil {
		slog.Error("error updating task to completed", "worker", workerID, "task", task.ID, "error", err)
	}

	// Store the assistant response as a message.
	assistantMsg := &domain.Message{
		ThreadID: task.ThreadID,
		Role:     domain.RoleAssistant,
		Content:  result.Response,
	}
	if err := wp.store.CreateMessage(ctx, assistantMsg); err != nil {
		slog.Error("error storing assistant message", "worker", workerID, "task", task.ID, "error", err)
	}

	// Enrich the ChannelRef with task metadata for proper response routing.
	responseRef := thread.ChannelRef
	if responseRef.Properties == nil {
		responseRef.Properties = make(map[string]string)
	}
	if task.Metadata != nil {
		if ct, ok := task.Metadata["type"]; ok {
			responseRef.Properties["comment_type"] = ct
		}
		if cid, ok := task.Metadata["comment_id"]; ok {
			responseRef.Properties["comment_id"] = cid
		}
	}

	// Send the response via the adapter.
	wp.sendResponse(ctx, responseRef, result.Response)
}

func (wp *WorkerPool) failTask(ctx context.Context, workerID string, task *domain.Task, err error) {
	slog.Error("task failed", "worker", workerID, "task", task.ID, "error", err)
	task.Status = domain.TaskFailed
	task.Result = err.Error()
	if updateErr := wp.store.UpdateTask(ctx, task); updateErr != nil {
		slog.Error("error updating task to failed", "worker", workerID, "task", task.ID, "error", updateErr)
	}
}

func (wp *WorkerPool) sendResponse(ctx context.Context, ref domain.ChannelRef, message string) {
	a := wp.router.GetAdapter(ref.Channel)
	if a == nil {
		slog.Warn("no adapter registered for channel", "channel", ref.Channel)
		return
	}
	if err := a.SendResponse(ctx, ref, message); err != nil {
		slog.Error("error sending response", "channel", ref.Channel, "error", err)
	}
}
