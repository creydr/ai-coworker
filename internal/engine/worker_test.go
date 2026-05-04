package engine

import (
	"context"
	"testing"

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

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1)
	return ms, adapter, codeExec, llmExec, wp
}

func TestWorker_ProcessTask_UsesTaskMetadata(t *testing.T) {
	ms, _, codeExec, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     "org/repo",
			IssueNum: 5,
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
			Channel:  "github",
			Repo:     "org/repo",
			IssueNum: 5,
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
	if ref.CommentType != "review_comment" {
		t.Errorf("CommentType = %q, want %q", ref.CommentType, "review_comment")
	}
	if ref.CommentID != 88888 {
		t.Errorf("CommentID = %d, want 88888", ref.CommentID)
	}
}

func TestWorker_ResponseRouting_IssueComment(t *testing.T) {
	ms, adapter, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     "org/repo",
			IssueNum: 10,
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
	if ref.CommentType != "issue_comment" {
		t.Errorf("CommentType = %q, want %q", ref.CommentType, "issue_comment")
	}
	if ref.CommentID != 0 {
		t.Errorf("CommentID = %d, want 0 (no reply-to for issue comments)", ref.CommentID)
	}
}

func TestWorker_ResponseRouting_NilMetadata(t *testing.T) {
	ms, adapter, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID: "thread-1",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     "org/repo",
			IssueNum: 1,
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
	if ref.CommentType != "" {
		t.Errorf("CommentType = %q, want empty", ref.CommentType)
	}
	if ref.CommentID != 0 {
		t.Errorf("CommentID = %d, want 0", ref.CommentID)
	}
}

func TestWorker_IntentRouting_CodeTask(t *testing.T) {
	ms, _, codeExec, llmExec, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID:         "thread-1",
		ChannelRef: domain.ChannelRef{Channel: "github", Repo: "org/repo", IssueNum: 1},
		Status:     domain.ThreadActive,
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

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1)

	thread := &domain.Thread{
		ID:         "thread-1",
		ChannelRef: domain.ChannelRef{Channel: "github", Repo: "org/repo", IssueNum: 1},
		Status:     domain.ThreadActive,
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

	wp := NewWorkerPool(ms, router, classifier, codeExec, llmExec, 1)

	thread := &domain.Thread{
		ID:         "thread-1",
		ChannelRef: domain.ChannelRef{Channel: "github", Repo: "org/repo", IssueNum: 3},
		Status:     domain.ThreadActive,
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

func TestWorker_TaskStatusUpdated(t *testing.T) {
	ms, _, _, _, wp := newWorkerTestSetup()

	thread := &domain.Thread{
		ID:         "thread-1",
		ChannelRef: domain.ChannelRef{Channel: "github", Repo: "org/repo", IssueNum: 1},
		Status:     domain.ThreadActive,
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
