package engine

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/store"
)

const (
	// errBackoff is the duration to wait before retrying after a claim error.
	errBackoff = 5 * time.Second
	// pollInterval is the duration to wait between polling for new tasks.
	pollInterval = 1 * time.Second
	// reviewDebounce is the delay before absorbing sibling review tasks,
	// giving remaining webhooks time to arrive and be persisted.
	reviewDebounce = 500 * time.Millisecond
)

// AdapterLookup resolves a channel name to its adapter.
type AdapterLookup interface {
	GetAdapter(name string) adapter.Adapter
}

// WorkerPool manages a pool of goroutines that claim and process tasks.
type WorkerPool struct {
	store      store.Store
	adapters   AdapterLookup
	classifier *IntentClassifier
	codeExec   executor.Executor
	llmExec    executor.Executor
	numWorkers int
	wg         sync.WaitGroup
}

// NewWorkerPool creates a new WorkerPool with the given dependencies.
func NewWorkerPool(
	s store.Store,
	adapters AdapterLookup,
	classifier *IntentClassifier,
	codeExec executor.Executor,
	llmExec executor.Executor,
	numWorkers int,
) *WorkerPool {
	return &WorkerPool{
		store:      s,
		adapters:   adapters,
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
			time.Sleep(errBackoff)
			continue
		}
		if task == nil {
			time.Sleep(pollInterval)
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

	// Absorb sibling review tasks into a single execution.
	var absorbedTasks []*domain.Task
	if intent == domain.IntentReview {
		absorbedTasks = wp.absorbReviewTasks(ctx, workerID, task)
	}

	// Select the executor based on intent.
	var exec executor.Executor
	switch intent {
	case domain.IntentCodeTask, domain.IntentReview, domain.IntentInfoLookup:
		exec = wp.codeExec
	default:
		exec = wp.llmExec
	}

	// Build executor context — use merged input if we absorbed tasks.
	if len(absorbedTasks) > 0 {
		event.Content = buildMergedReviewInput(task, absorbedTasks)
		task.Input = event.Content
	}

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
		for _, absorbed := range absorbedTasks {
			wp.failTask(ctx, workerID, absorbed, fmt.Errorf("batch execution failed: %w", err))
		}
		return
	}
	slog.Info("task completed", "worker", workerID, "task", task.ID, "response_len", len(result.Response))

	// Mark task as completed before sending the response to prevent
	// duplicate processing if the update fails.
	task.Status = domain.TaskCompleted
	task.Result = result.Response
	if err := wp.store.UpdateTask(ctx, task); err != nil {
		slog.Error("error updating task to completed", "worker", workerID, "task", task.ID, "error", err)
		return
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

	if len(absorbedTasks) > 0 {
		allTasks := append([]*domain.Task{task}, absorbedTasks...)
		if err := wp.routeBatchedResponses(ctx, thread, allTasks, result.Response); err != nil {
			slog.Error("error completing batched tasks", "worker", workerID, "task", task.ID, "error", err)
		}
	} else {
		wp.routeResponse(ctx, thread, task, result.Response)
	}
}

// routeResponse enriches the ChannelRef with task metadata and sends the
// response through the appropriate channel adapter.
func (wp *WorkerPool) routeResponse(ctx context.Context, thread *domain.Thread, task *domain.Task, response string) {
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

	wp.sendResponse(ctx, responseRef, response)
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
	a := wp.adapters.GetAdapter(ref.Channel)
	if a == nil {
		slog.Warn("no adapter registered for channel", "channel", ref.Channel)
		return
	}
	if err := a.SendResponse(ctx, ref, message); err != nil {
		slog.Error("error sending response", "channel", ref.Channel, "error", err)
	}
}

func (wp *WorkerPool) absorbReviewTasks(ctx context.Context, workerID string, primaryTask *domain.Task) []*domain.Task {
	// Wait briefly so sibling tasks from the same review have time to be
	// persisted. ClaimNextTask's advisory lock and NOT EXISTS filter prevent
	// other workers from claiming them during this window.
	select {
	case <-time.After(reviewDebounce):
	case <-ctx.Done():
		return nil
	}

	claimed, err := wp.store.ClaimPendingTasks(ctx, primaryTask.ThreadID, workerID)
	if err != nil {
		slog.Error("error absorbing pending tasks", "worker", workerID, "thread", primaryTask.ThreadID, "error", err)
		return nil
	}

	var reviewTasks []*domain.Task
	for _, t := range claimed {
		metaType := t.Metadata["type"]
		if metaType == "review" || metaType == "review_comment" {
			reviewTasks = append(reviewTasks, t)
		} else {
			t.Status = domain.TaskPending
			t.WorkerID = ""
			if err := wp.store.UpdateTask(ctx, t); err != nil {
				slog.Error("error releasing non-review task", "worker", workerID, "task", t.ID, "error", err)
			}
		}
	}

	if len(reviewTasks) > 0 {
		slog.Info("absorbed sibling review tasks", "worker", workerID,
			"primary_task", primaryTask.ID, "absorbed_count", len(reviewTasks))
	}
	return reviewTasks
}

func buildMergedReviewInput(primary *domain.Task, absorbed []*domain.Task) string {
	allTasks := append([]*domain.Task{primary}, absorbed...)
	var sb strings.Builder
	sb.WriteString("This review contains multiple comments. Please address all of them.\n")
	sb.WriteString("After making all changes, respond with one section per comment using this exact format:\n\n")
	sb.WriteString("--- COMMENT 1 ---\n")
	sb.WriteString("Your response for comment 1\n\n")
	sb.WriteString("--- COMMENT 2 ---\n")
	sb.WriteString("Your response for comment 2\n\n")
	sb.WriteString("Here are the review comments:\n\n")

	for i, t := range allTasks {
		fmt.Fprintf(&sb, "--- Comment %d", i+1)
		if t.Metadata["type"] == "review" {
			sb.WriteString(" (review body)")
		} else if path := t.Metadata["path"]; path != "" {
			fmt.Fprintf(&sb, " (on file: %s", path)
			if start, end := t.Metadata["start_line"], t.Metadata["line"]; start != "" && end != "" {
				fmt.Fprintf(&sb, ", lines %s-%s", start, end)
			} else if end := t.Metadata["line"]; end != "" {
				fmt.Fprintf(&sb, ", line %s", end)
			}
			sb.WriteString(")")
		}
		sb.WriteString(" ---\n")
		sb.WriteString(t.Input)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

var commentHeaderRe = regexp.MustCompile(`(?m)^---\s*COMMENT\s+(\d+)\s*---\s*$`)

func parseCommentResponses(output string) map[int]string {
	matches := commentHeaderRe.FindAllStringSubmatchIndex(output, -1)
	if len(matches) == 0 {
		return nil
	}

	responses := make(map[int]string, len(matches))
	for i, match := range matches {
		idx, err := strconv.Atoi(output[match[2]:match[3]])
		if err != nil {
			continue
		}
		contentStart := match[1]
		var contentEnd int
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		} else {
			contentEnd = len(output)
		}
		responses[idx] = strings.TrimSpace(output[contentStart:contentEnd])
	}
	return responses
}

func (wp *WorkerPool) routeBatchedResponses(ctx context.Context, thread *domain.Thread, allTasks []*domain.Task, fullResponse string) error {
	perComment := parseCommentResponses(fullResponse)

	for i, t := range allTasks {
		var response string
		if perComment != nil {
			if r, ok := perComment[i+1]; ok {
				response = r
			} else {
				response = fullResponse
			}
		} else {
			response = fullResponse
		}

		// Mark absorbed tasks as completed before routing the response
		// to prevent duplicate processing.
		if i > 0 {
			t.Status = domain.TaskCompleted
			t.Result = response
			if err := wp.store.UpdateTask(ctx, t); err != nil {
				return fmt.Errorf("completing absorbed task %s: %w", t.ID, err)
			}
		}

		wp.routeResponse(ctx, thread, t, response)
	}
	return nil
}
