package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
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
// It blocks until ctx is cancelled.
func (wp *WorkerPool) Start(ctx context.Context) {
	for i := 0; i < wp.numWorkers; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		go wp.runWorker(ctx, workerID)
	}
}

func (wp *WorkerPool) runWorker(ctx context.Context, workerID string) {
	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] stopping: context cancelled", workerID)
			return
		default:
		}

		task, err := wp.store.ClaimNextTask(ctx, workerID)
		if err != nil {
			if strings.Contains(err.Error(), "no rows") {
				// No pending tasks — back off briefly.
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("[%s] error claiming task: %v", workerID, err)
			time.Sleep(5 * time.Second)
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

	// Reconstruct an IncomingEvent from thread data for classification.
	event := domain.IncomingEvent{
		Channel:    thread.ChannelRef.Channel,
		ChannelRef: thread.ChannelRef,
		ThreadID:   thread.ID,
		Content:    task.Input,
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

	result, err := exec.Execute(ctx, execCtx)
	if err != nil {
		wp.failTask(ctx, workerID, task, fmt.Errorf("executing task: %w", err))
		wp.sendResponse(ctx, thread.ChannelRef, fmt.Sprintf("Sorry, I encountered an error while processing your request: %v", err))
		return
	}

	// Mark task as completed.
	task.Status = domain.TaskCompleted
	task.Result = result.Response
	if err := wp.store.UpdateTask(ctx, task); err != nil {
		log.Printf("[%s] error updating task %s to completed: %v", workerID, task.ID, err)
	}

	// Store the assistant response as a message.
	assistantMsg := &domain.Message{
		ThreadID: task.ThreadID,
		Role:     domain.RoleAssistant,
		Content:  result.Response,
	}
	if err := wp.store.CreateMessage(ctx, assistantMsg); err != nil {
		log.Printf("[%s] error storing assistant message for task %s: %v", workerID, task.ID, err)
	}

	// Send the response via the adapter.
	wp.sendResponse(ctx, thread.ChannelRef, result.Response)
}

func (wp *WorkerPool) failTask(ctx context.Context, workerID string, task *domain.Task, err error) {
	log.Printf("[%s] task %s failed: %v", workerID, task.ID, err)
	task.Status = domain.TaskFailed
	task.Result = err.Error()
	if updateErr := wp.store.UpdateTask(ctx, task); updateErr != nil {
		log.Printf("[%s] error updating task %s to failed: %v", workerID, task.ID, updateErr)
	}
}

func (wp *WorkerPool) sendResponse(ctx context.Context, ref domain.ChannelRef, message string) {
	a := wp.router.GetAdapter(ref.Channel)
	if a == nil {
		log.Printf("no adapter registered for channel %q", ref.Channel)
		return
	}
	if err := a.SendResponse(ctx, ref, message); err != nil {
		log.Printf("error sending response on %s: %v", ref.Channel, err)
	}
}
