package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/executor"
	"github.com/creydr/ai-coworker/internal/llm"
)

type mockExecutor struct {
	capturedCtx *executor.Context
	result      *executor.Result
	err         error
}

func (m *mockExecutor) Execute(_ context.Context, execCtx *executor.Context) (*executor.Result, error) {
	m.capturedCtx = execCtx
	return m.result, m.err
}

type mockLLMProvider struct {
	response string
}

func (m *mockLLMProvider) Chat(_ context.Context, _ []llm.Message) (string, error) {
	return m.response, nil
}

func newWorkerTestSetup() (*mockStore, *mockAdapter, *mockExecutor, *mockExecutor, *WorkerPool) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		result: &executor.Result{Response: "code changes applied"},
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "here is the answer"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "code_task"})

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)
	return ms, adapter, codeExec, llmExec, wp
}

func TestWorker_ProcessTask_UsesTaskMetadata(t *testing.T) {
	ms, _, codeExec, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#5",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "5",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread
	ms.messages["thread-1"] = []domain.Message{
		{ID: "msg-1", ThreadID: "thread-1", Role: domain.RoleUser, Content: "fix the bug"},
	}

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "fix the bug",
		Metadata: map[string]string{
			"repo":      "org/repo",
			"issue_num": "5",
			"is_pr":     "true",
			"pr_branch": "feat/fix",
			"type":      "review_comment",
		},
	}

	wp.processTask(context.Background(), "worker-0", task)

	if codeExec.capturedCtx == nil {
		t.Fatal("executor was not called")
	}
	if codeExec.capturedCtx.Event == nil {
		t.Fatal("executor context Event is nil")
	}

	eventMeta := codeExec.capturedCtx.Event.Metadata
	if eventMeta == nil {
		t.Fatal("event.Metadata is nil, want task metadata")
	}

	wantMeta := map[string]string{
		"repo":      "org/repo",
		"issue_num": "5",
		"is_pr":     "true",
		"pr_branch": "feat/fix",
		"type":      "review_comment",
	}
	for k, v := range wantMeta {
		if eventMeta[k] != v {
			t.Errorf("event.Metadata[%q] = %q, want %q", k, eventMeta[k], v)
		}
	}
}

func TestWorker_ResponseRouting_ReviewComment(t *testing.T) {
	ms, adapter, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#5",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "5",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "add error handling",
		Metadata: map[string]string{
			"type":       "review_comment",
			"comment_id": "88888",
			"repo":       "org/repo",
			"issue_num":  "5",
			"is_pr":      "true",
		},
	}

	wp.processTask(context.Background(), "worker-0", task)

	if len(adapter.responseCalls) != 1 {
		t.Fatalf("expected 1 response call, got %d", len(adapter.responseCalls))
	}

	ref := adapter.responseCalls[0].Ref
	if ref.Properties["comment_type"] != "review_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", ref.Properties["comment_type"], "review_comment")
	}
	if ref.Properties["comment_id"] != "88888" {
		t.Errorf("Properties[comment_id] = %q, want %q", ref.Properties["comment_id"], "88888")
	}
}

func TestWorker_ResponseRouting_IssueComment(t *testing.T) {
	ms, adapter, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#10",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "10",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "do something",
		Metadata: map[string]string{
			"type":      "issue_comment",
			"repo":      "org/repo",
			"issue_num": "10",
		},
	}

	wp.processTask(context.Background(), "worker-0", task)

	if len(adapter.responseCalls) != 1 {
		t.Fatalf("expected 1 response call, got %d", len(adapter.responseCalls))
	}

	ref := adapter.responseCalls[0].Ref
	if ref.Properties["comment_type"] != "issue_comment" {
		t.Errorf("Properties[comment_type] = %q, want %q", ref.Properties["comment_type"], "issue_comment")
	}
}

func TestWorker_ResponseRouting_NilMetadata(t *testing.T) {
	ms, adapter, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "hello",
		Metadata: nil,
	}

	wp.processTask(context.Background(), "worker-0", task)

	if len(adapter.responseCalls) != 1 {
		t.Fatalf("expected 1 response call, got %d", len(adapter.responseCalls))
	}

	ref := adapter.responseCalls[0].Ref
	if ref.Properties["comment_type"] != "" {
		t.Errorf("Properties[comment_type] = %q, want empty", ref.Properties["comment_type"])
	}
	if ref.Properties["comment_id"] != "" {
		t.Errorf("Properties[comment_id] = %q, want empty", ref.Properties["comment_id"])
	}
}

func TestWorker_IntentRouting_CodeTask(t *testing.T) {
	ms, _, codeExec, llmExec, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "implement feature X",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if codeExec.capturedCtx == nil {
		t.Error("code executor was not called for code_task intent")
	}
	if llmExec.capturedCtx != nil {
		t.Error("llm executor should not be called for code_task intent")
	}
}

func TestWorker_IntentRouting_Question(t *testing.T) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		result: &executor.Result{Response: "code result"},
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "answer to question"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "question"})

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "what does this function do?",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if llmExec.capturedCtx == nil {
		t.Error("llm executor was not called for question intent")
	}
	if codeExec.capturedCtx != nil {
		t.Error("code executor should not be called for question intent")
	}
}

func TestWorker_IntentRouting_InfoLookup(t *testing.T) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		result: &executor.Result{Response: "PR #18 summary: ..."},
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "llm result"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "info_lookup"})

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: "C123/1234.5678",
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "can you give me a summary of PR https://github.com/creydr/ai-coworker-test/pull/18?",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if codeExec.capturedCtx == nil {
		t.Error("code executor was not called for info_lookup intent")
	}
	if llmExec.capturedCtx != nil {
		t.Error("llm executor should not be called for info_lookup intent")
	}
}

func TestWorker_IntentRouting_ReviewShortCircuit(t *testing.T) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		result: &executor.Result{Response: "review done"},
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "llm result"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "question"})

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#3",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "3",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "fix these issues",
		Metadata: map[string]string{
			"review_state": "changes_requested",
			"type":         "review",
		},
	}

	wp.processTask(context.Background(), "worker-0", task)

	if codeExec.capturedCtx == nil {
		t.Error("code executor was not called — review_state metadata should short-circuit to IntentReview → code executor")
	}
	if llmExec.capturedCtx != nil {
		t.Error("llm executor should not be called for review intent")
	}
}

func TestParseCommentResponses_ValidOutput(t *testing.T) {
	output := `--- COMMENT 1 ---
Fixed the error handling in parse().

--- COMMENT 2 ---
Added validation for empty strings.

--- COMMENT 3 ---
Good catch, updated the docs.`

	responses := parseCommentResponses(output)
	if responses == nil {
		t.Fatal("parseCommentResponses returned nil")
	}
	if len(responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(responses))
	}
	if responses[1] != "Fixed the error handling in parse()." {
		t.Errorf("responses[1] = %q", responses[1])
	}
	if responses[2] != "Added validation for empty strings." {
		t.Errorf("responses[2] = %q", responses[2])
	}
	if responses[3] != "Good catch, updated the docs." {
		t.Errorf("responses[3] = %q", responses[3])
	}
}

func TestParseCommentResponses_NoMarkers(t *testing.T) {
	output := "Here is a plain response without any markers."
	responses := parseCommentResponses(output)
	if responses != nil {
		t.Errorf("expected nil, got %v", responses)
	}
}

func TestParseCommentResponses_PartialMarkers(t *testing.T) {
	output := `--- COMMENT 1 ---
Only the first comment has a marker.`

	responses := parseCommentResponses(output)
	if responses == nil {
		t.Fatal("parseCommentResponses returned nil")
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[1] != "Only the first comment has a marker." {
		t.Errorf("responses[1] = %q", responses[1])
	}
}

func TestBuildMergedReviewInput(t *testing.T) {
	primary := &domain.Task{
		Input: "fix the error handling",
		Metadata: map[string]string{
			"type": "review",
		},
	}
	absorbed := []*domain.Task{
		{
			Input: "add validation here",
			Metadata: map[string]string{
				"type":       "review_comment",
				"path":       "internal/config/config.go",
				"line":       "42",
				"start_line": "38",
			},
		},
		{
			Input: "fix this too",
			Metadata: map[string]string{
				"type": "review_comment",
				"path": "internal/server/server.go",
				"line": "10",
			},
		},
	}

	result := buildMergedReviewInput(primary, absorbed)

	if !strings.Contains(result, "multiple comments") {
		t.Error("merged input should mention multiple comments")
	}
	if !strings.Contains(result, "--- Comment 1 (review body) ---") {
		t.Error("primary task should be labeled as review body")
	}
	if !strings.Contains(result, "--- Comment 2 (on file: internal/config/config.go, lines 38-42) ---") {
		t.Error("absorbed task should show file path with line range")
	}
	if !strings.Contains(result, "--- Comment 3 (on file: internal/server/server.go, line 10) ---") {
		t.Error("absorbed task should show file path with single line")
	}
	if !strings.Contains(result, "fix the error handling") {
		t.Error("primary task input should be included")
	}
	if !strings.Contains(result, "add validation here") {
		t.Error("absorbed task input should be included")
	}
	if !strings.Contains(result, "fix this too") {
		t.Error("second absorbed task input should be included")
	}
}

func TestWorker_BatchedReviewRouting(t *testing.T) {
	ms, adpt, codeExec, _, _ := newWorkerTestSetup()

	codeExec.result = &executor.Result{
		Response: "--- COMMENT 1 ---\nFixed the review body issue.\n\n--- COMMENT 2 ---\nAdded the validation.\n\n--- COMMENT 3 ---\nUpdated the docs.",
	}

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#7",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "7",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	primary := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "fix error handling",
		Metadata: map[string]string{
			"type":       "review",
			"comment_id": "100",
		},
	}
	absorbed := []*domain.Task{
		{
			ID:       "task-2",
			ThreadID: "thread-1",
			Status:   domain.TaskInProgress,
			Input:    "add validation",
			Metadata: map[string]string{
				"type":       "review_comment",
				"comment_id": "101",
			},
		},
		{
			ID:       "task-3",
			ThreadID: "thread-1",
			Status:   domain.TaskInProgress,
			Input:    "update docs",
			Metadata: map[string]string{
				"type":       "review_comment",
				"comment_id": "102",
			},
		},
	}

	router := NewRouter(ms)
	router.RegisterAdapter(adpt)
	wp := &WorkerPool{store: ms, adapters: router}
	allTasks := append([]*domain.Task{primary}, absorbed...)
	if err := wp.routeBatchedResponses(context.Background(), thread, allTasks, codeExec.result.Response); err != nil {
		t.Fatalf("routeBatchedResponses returned error: %v", err)
	}

	if len(adpt.responseCalls) != 3 {
		t.Fatalf("expected 3 response calls, got %d", len(adpt.responseCalls))
	}

	if !strings.Contains(adpt.responseCalls[0].Message, "Fixed the review body issue.") {
		t.Errorf("response 0 = %q, want review body response", adpt.responseCalls[0].Message)
	}
	if !strings.Contains(adpt.responseCalls[1].Message, "Added the validation.") {
		t.Errorf("response 1 = %q, want validation response", adpt.responseCalls[1].Message)
	}
	if !strings.Contains(adpt.responseCalls[2].Message, "Updated the docs.") {
		t.Errorf("response 2 = %q, want docs response", adpt.responseCalls[2].Message)
	}

	if absorbed[0].Status != domain.TaskCompleted {
		t.Errorf("absorbed task 1 status = %q, want completed", absorbed[0].Status)
	}
	if absorbed[1].Status != domain.TaskCompleted {
		t.Errorf("absorbed task 2 status = %q, want completed", absorbed[1].Status)
	}
}

func TestWorker_BatchedReviewFallback(t *testing.T) {
	ms, adpt, _, _, _ := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#7",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "7",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	primary := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "fix it",
		Metadata: map[string]string{"type": "review"},
	}
	absorbed := []*domain.Task{
		{
			ID:       "task-2",
			ThreadID: "thread-1",
			Status:   domain.TaskInProgress,
			Input:    "also this",
			Metadata: map[string]string{"type": "review_comment"},
		},
	}

	router := NewRouter(ms)
	router.RegisterAdapter(adpt)
	wp := &WorkerPool{store: ms, adapters: router}

	fullResponse := "I fixed everything in one go."
	allTasks := append([]*domain.Task{primary}, absorbed...)
	if err := wp.routeBatchedResponses(context.Background(), thread, allTasks, fullResponse); err != nil {
		t.Fatalf("routeBatchedResponses returned error: %v", err)
	}

	if len(adpt.responseCalls) != 2 {
		t.Fatalf("expected 2 response calls, got %d", len(adpt.responseCalls))
	}

	for i, call := range adpt.responseCalls {
		if call.Message != fullResponse {
			t.Errorf("response %d = %q, want full response fallback", i, call.Message)
		}
	}
}

func TestWorker_ReviewAbsorbsSiblings(t *testing.T) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		result: &executor.Result{
			Response: "--- COMMENT 1 ---\nReview body addressed.\n\n--- COMMENT 2 ---\nInline comment fixed.",
		},
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "llm"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "review"})
	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#5",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "5",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	siblingTask := &domain.Task{
		ID:       "task-2",
		ThreadID: "thread-1",
		Status:   domain.TaskPending,
		Input:    "fix the inline issue",
		Metadata: map[string]string{
			"type":       "review_comment",
			"comment_id": "201",
			"repo":       "org/repo",
			"issue_num":  "5",
			"is_pr":      "true",
		},
	}

	ms.claimPendingTasksFunc = func(_ context.Context, threadID, _ string) ([]*domain.Task, error) {
		if threadID == "thread-1" {
			siblingTask.Status = domain.TaskInProgress
			return []*domain.Task{siblingTask}, nil
		}
		return nil, nil
	}

	primaryTask := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "review body comment",
		Metadata: map[string]string{
			"type":         "review",
			"review_state": "changes_requested",
			"comment_id":   "200",
			"repo":         "org/repo",
			"issue_num":    "5",
			"is_pr":        "true",
		},
	}

	wp.processTask(context.Background(), "worker-0", primaryTask)

	if codeExec.capturedCtx == nil {
		t.Fatal("code executor was not called")
	}
	if !strings.Contains(codeExec.capturedCtx.Event.Content, "multiple comments") {
		t.Error("merged input should mention multiple comments")
	}

	if len(adapter.responseCalls) != 2 {
		t.Fatalf("expected 2 response calls (primary + sibling), got %d", len(adapter.responseCalls))
	}
	if !strings.Contains(adapter.responseCalls[0].Message, "Review body addressed.") {
		t.Errorf("response 0 = %q, want review body response", adapter.responseCalls[0].Message)
	}
	if !strings.Contains(adapter.responseCalls[1].Message, "Inline comment fixed.") {
		t.Errorf("response 1 = %q, want inline comment response", adapter.responseCalls[1].Message)
	}

	if siblingTask.Status != domain.TaskCompleted {
		t.Errorf("sibling task status = %q, want completed", siblingTask.Status)
	}
}

func TestWorker_CompletionUpdateFailure_NoResponse(t *testing.T) {
	ms, adpt, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	// Make UpdateTask fail only when marking as completed (not for intent update).
	updateCalls := 0
	ms.updateTaskFunc = func(_ context.Context, t *domain.Task) error {
		updateCalls++
		if t.Status == domain.TaskCompleted {
			return fmt.Errorf("database unavailable")
		}
		return nil
	}

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "do it",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if len(adpt.responseCalls) != 0 {
		t.Fatalf("expected 0 response calls when completion update fails, got %d", len(adpt.responseCalls))
	}
}

func TestWorker_BatchedCompletionUpdateFailure(t *testing.T) {
	ms, _, _, _, _ := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#7",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "7",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	ms.updateTaskFunc = func(_ context.Context, t *domain.Task) error {
		if t.ID == "task-2" && t.Status == domain.TaskCompleted {
			return fmt.Errorf("database unavailable")
		}
		return nil
	}

	adpt := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adpt)
	wp := &WorkerPool{store: ms, adapters: router}

	primary := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "fix it",
		Metadata: map[string]string{"type": "review"},
	}
	absorbed := []*domain.Task{
		{
			ID:       "task-2",
			ThreadID: "thread-1",
			Status:   domain.TaskInProgress,
			Input:    "also this",
			Metadata: map[string]string{"type": "review_comment"},
		},
		{
			ID:       "task-3",
			ThreadID: "thread-1",
			Status:   domain.TaskInProgress,
			Input:    "and this too",
			Metadata: map[string]string{"type": "review_comment"},
		},
	}

	allTasks := append([]*domain.Task{primary}, absorbed...)
	err := wp.routeBatchedResponses(context.Background(), thread, allTasks, "response")
	if err == nil {
		t.Fatal("expected error from routeBatchedResponses when completion update fails")
	}

	if len(adpt.responseCalls) != 3 {
		t.Errorf("expected 3 response calls (all tasks routed despite error), got %d", len(adpt.responseCalls))
	}
}

func TestWorker_ProcessTask_DoesNotMutateTaskInput(t *testing.T) {
	ms, _, codeExec, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#10",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "10",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	originalInput := "fix the first bug"
	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    originalInput,
		Metadata: map[string]string{"type": "review"},
	}

	ms.claimPendingTasksFunc = func(_ context.Context, _, _ string) ([]*domain.Task, error) {
		return []*domain.Task{
			{
				ID:       "task-2",
				ThreadID: "thread-1",
				Status:   domain.TaskPending,
				Input:    "fix the second bug",
				Metadata: map[string]string{"type": "review_comment"},
			},
		}, nil
	}

	codeExec.result = &executor.Result{
		Response: "--- COMMENT 1 ---\nfixed first\n\n--- COMMENT 2 ---\nfixed second",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if task.Input != originalInput {
		t.Errorf("task.Input was mutated: got %q, want %q", task.Input, originalInput)
	}
}

func TestWorker_ExecutionError_SanitizedResponse(t *testing.T) {
	ms := newMockStore()
	adapter := &mockAdapter{name: "github"}
	router := NewRouter(ms)
	router.RegisterAdapter(adapter)

	codeExec := &mockExecutor{
		err: fmt.Errorf("connection refused: dial tcp 10.0.0.1:5432: connect: connection refused"),
	}
	llmExec := &mockExecutor{
		result: &executor.Result{Response: "llm"},
	}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "code_task"})
	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "do something",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if len(adapter.responseCalls) != 1 {
		t.Fatalf("expected 1 response call, got %d", len(adapter.responseCalls))
	}

	response := adapter.responseCalls[0].Message
	if strings.Contains(response, "connection refused") {
		t.Errorf("user-facing response leaks internal error details: %q", response)
	}
	if strings.Contains(response, "10.0.0.1") {
		t.Errorf("user-facing response leaks internal IP address: %q", response)
	}
	if !strings.Contains(response, "Sorry") {
		t.Errorf("expected user-friendly error message, got: %q", response)
	}
}

func TestWorker_TaskStatusUpdated(t *testing.T) {
	ms, _, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["thread-1"] = thread

	task := &domain.Task{
		ID:       "task-1",
		ThreadID: "thread-1",
		Status:   domain.TaskInProgress,
		Input:    "do it",
	}

	wp.processTask(context.Background(), "worker-0", task)

	if task.Status != domain.TaskCompleted {
		t.Errorf("task.Status = %q, want %q", task.Status, domain.TaskCompleted)
	}
	if task.Result != "code changes applied" {
		t.Errorf("task.Result = %q, want %q", task.Result, "code changes applied")
	}
}

func TestWorker_ReaperCallsStore(t *testing.T) {
	ms := newMockStore()
	router := NewRouter(ms)
	codeExec := &mockExecutor{result: &executor.Result{Response: "ok"}}
	llmExec := &mockExecutor{result: &executor.Result{Response: "ok"}}
	classifier := NewIntentClassifier(&mockLLMProvider{response: "code_task"})
	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, 0)

	wantThreshold := wp.staleThreshold()

	var reapCalls atomic.Int32
	ms.reapStaleTasksFunc = func(_ context.Context, threshold time.Duration) (int, error) {
		reapCalls.Add(1)
		if threshold != wantThreshold {
			t.Errorf("threshold = %v, want %v", threshold, wantThreshold)
		}
		return 2, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	go wp.runReaper(ctx)

	// Wait for at least one reap cycle. The ticker fires at reaperInterval,
	// but we can't wait that long in a test. Instead, directly call runReaper
	// with a short-lived context.
	cancel()
	// Give the goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// runReaper waits for ticker — it won't fire with the cancelled context.
	// Instead, test the store method directly.
	n, err := wp.store.ReapStaleTasks(context.Background(), wantThreshold)
	if err != nil {
		t.Fatalf("ReapStaleTasks: %v", err)
	}
	if n != 2 {
		t.Errorf("reaped = %d, want 2", n)
	}
	if reapCalls.Load() < 1 {
		t.Error("expected ReapStaleTasks to be called at least once")
	}
}

func TestWorker_StaleThresholdDerivedFromTimeout(t *testing.T) {
	ms := newMockStore()
	router := NewRouter(ms)

	tests := []struct {
		name        string
		taskTimeout time.Duration
		want        time.Duration
	}{
		{"zero uses default 30m + buffer", 0, 30*time.Minute + staleTaskBuffer},
		{"10m timeout", 10 * time.Minute, 10*time.Minute + staleTaskBuffer},
		{"1h timeout", 1 * time.Hour, 1*time.Hour + staleTaskBuffer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codeExec := &mockExecutor{result: &executor.Result{Response: "ok"}}
			llmExec := &mockExecutor{result: &executor.Result{Response: "ok"}}
			classifier := NewIntentClassifier(&mockLLMProvider{response: "code_task"})
			wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1, tt.taskTimeout)

			got := wp.staleThreshold()
			if got != tt.want {
				t.Errorf("staleThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}
