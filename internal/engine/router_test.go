package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/creydr/ai-coworker/internal/adapter"
	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/store"
)

type mockStore struct {
	threads  map[string]*domain.Thread
	messages map[string][]domain.Message
	tasks    []*domain.Task

	getThreadFunc          func(ctx context.Context, id string) (*domain.Thread, error)
	getThreadByChannelFunc func(ctx context.Context, channel, threadKey string) (*domain.Thread, error)
	createThreadFunc       func(ctx context.Context, t *domain.Thread) error
	createMessageFunc      func(ctx context.Context, m *domain.Message) error
	createTaskFunc         func(ctx context.Context, t *domain.Task) error
	claimNextTaskFunc      func(ctx context.Context, workerID string) (*domain.Task, error)
	claimPendingTasksFunc  func(ctx context.Context, threadID, workerID string) ([]*domain.Task, error)
	updateTaskFunc         func(ctx context.Context, t *domain.Task) error
}

var _ store.Store = (*mockStore)(nil)

func newMockStore() *mockStore {
	return &mockStore{
		threads:  make(map[string]*domain.Thread),
		messages: make(map[string][]domain.Message),
	}
}

func (m *mockStore) GetThread(ctx context.Context, id string) (*domain.Thread, error) {
	if m.getThreadFunc != nil {
		return m.getThreadFunc(ctx, id)
	}
	if t, ok := m.threads[id]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("thread %s: %w", id, store.ErrNotFound)
}

func (m *mockStore) GetThreadByChannelRef(ctx context.Context, channel, threadKey string) (*domain.Thread, error) {
	if m.getThreadByChannelFunc != nil {
		return m.getThreadByChannelFunc(ctx, channel, threadKey)
	}
	return nil, fmt.Errorf("thread %s/%s: %w", channel, threadKey, store.ErrNotFound)
}

func (m *mockStore) CreateThread(ctx context.Context, t *domain.Thread) error {
	if m.createThreadFunc != nil {
		return m.createThreadFunc(ctx, t)
	}
	t.ID = fmt.Sprintf("thread-%d", len(m.threads)+1)
	m.threads[t.ID] = t
	return nil
}

func (m *mockStore) UpdateThreadStatus(ctx context.Context, id string, status domain.ThreadStatus) error {
	return nil
}

func (m *mockStore) GetMessages(ctx context.Context, threadID string) ([]domain.Message, error) {
	return m.messages[threadID], nil
}

func (m *mockStore) CreateMessage(ctx context.Context, msg *domain.Message) error {
	if m.createMessageFunc != nil {
		return m.createMessageFunc(ctx, msg)
	}
	msg.ID = fmt.Sprintf("msg-%d", len(m.messages[msg.ThreadID])+1)
	m.messages[msg.ThreadID] = append(m.messages[msg.ThreadID], *msg)
	return nil
}

func (m *mockStore) CreateTask(ctx context.Context, t *domain.Task) error {
	if m.createTaskFunc != nil {
		return m.createTaskFunc(ctx, t)
	}
	t.ID = fmt.Sprintf("task-%d", len(m.tasks)+1)
	m.tasks = append(m.tasks, t)
	return nil
}

func (m *mockStore) ClaimNextTask(ctx context.Context, workerID string) (*domain.Task, error) {
	if m.claimNextTaskFunc != nil {
		return m.claimNextTaskFunc(ctx, workerID)
	}
	return nil, nil
}

func (m *mockStore) ClaimPendingTasks(ctx context.Context, threadID, workerID string) ([]*domain.Task, error) {
	if m.claimPendingTasksFunc != nil {
		return m.claimPendingTasksFunc(ctx, threadID, workerID)
	}
	return nil, nil
}

func (m *mockStore) UpdateTask(ctx context.Context, t *domain.Task) error {
	if m.updateTaskFunc != nil {
		return m.updateTaskFunc(ctx, t)
	}
	return nil
}

func (m *mockStore) Migrate(ctx context.Context) error { return nil }
func (m *mockStore) Close() error                      { return nil }

type mockAdapter struct {
	name             string
	acknowledgeCalls []domain.ChannelRef
	responseCalls    []sendResponseCall
}

type sendResponseCall struct {
	Ref     domain.ChannelRef
	Message string
}

func (a *mockAdapter) Start(_ context.Context, _ adapter.EventHandler) error {
	return nil
}

func (a *mockAdapter) SendResponse(_ context.Context, ref domain.ChannelRef, message string) error {
	a.responseCalls = append(a.responseCalls, sendResponseCall{Ref: ref, Message: message})
	return nil
}

func (a *mockAdapter) Acknowledge(_ context.Context, ref domain.ChannelRef) error {
	a.acknowledgeCalls = append(a.acknowledgeCalls, ref)
	return nil
}

func (a *mockAdapter) Name() string { return a.name }

func TestRouter_HandleEvent_PassesMetadata(t *testing.T) {
	ms := newMockStore()
	r := NewRouter(ms)
	adapter := &mockAdapter{name: "github"}
	r.RegisterAdapter(adapter)

	event := domain.IncomingEvent{
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#42",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "42",
			},
		},
		Content: "fix this bug",
		Metadata: map[string]string{
			"repo":      "org/repo",
			"issue_num": "42",
			"is_pr":     "true",
			"pr_branch": "feat/fix",
			"type":      "review_comment",
		},
	}

	if err := r.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if len(ms.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(ms.tasks))
	}

	task := ms.tasks[0]
	if task.Metadata == nil {
		t.Fatal("task.Metadata is nil, want event metadata")
	}

	wantMeta := map[string]string{
		"repo":      "org/repo",
		"issue_num": "42",
		"is_pr":     "true",
		"pr_branch": "feat/fix",
		"type":      "review_comment",
	}
	for k, v := range wantMeta {
		if task.Metadata[k] != v {
			t.Errorf("task.Metadata[%q] = %q, want %q", k, task.Metadata[k], v)
		}
	}
}

func TestRouter_HandleEvent_NilMetadata(t *testing.T) {
	ms := newMockStore()
	r := NewRouter(ms)

	event := domain.IncomingEvent{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: "C123/1234.5678",
			Properties: map[string]string{
				"channel_id": "C123",
				"thread_ts":  "1234.5678",
			},
		},
		Content:  "hello",
		Metadata: nil,
	}

	if err := r.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if len(ms.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(ms.tasks))
	}

	if ms.tasks[0].Metadata != nil {
		t.Errorf("task.Metadata = %v, want nil", ms.tasks[0].Metadata)
	}
}

func TestRouter_HandleEvent_Acknowledge(t *testing.T) {
	ms := newMockStore()
	r := NewRouter(ms)
	a := &mockAdapter{name: "github"}
	r.RegisterAdapter(a)

	event := domain.IncomingEvent{
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#1",
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Content: "do it",
	}

	if err := r.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if len(a.acknowledgeCalls) != 1 {
		t.Fatalf("expected 1 acknowledge call, got %d", len(a.acknowledgeCalls))
	}
}

func TestRouter_HandleEvent_ExistingThread(t *testing.T) {
	ms := newMockStore()
	existingThread := &domain.Thread{
		ID: "existing-thread",
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: "C123/1234.5678",
			Properties: map[string]string{
				"channel_id": "C123",
				"thread_ts":  "1234.5678",
			},
		},
		Status: domain.ThreadActive,
	}
	ms.threads["existing-thread"] = existingThread
	ms.getThreadByChannelFunc = func(_ context.Context, channel, threadKey string) (*domain.Thread, error) {
		if channel == "slack" && threadKey == "C123/1234.5678" {
			return existingThread, nil
		}
		return nil, fmt.Errorf("thread not found")
	}

	r := NewRouter(ms)

	event := domain.IncomingEvent{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: "C123/1234.5678",
			Properties: map[string]string{
				"channel_id": "C123",
				"thread_ts":  "1234.5678",
			},
		},
		Content: "follow-up message",
	}

	if err := r.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if len(ms.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(ms.tasks))
	}
	if ms.tasks[0].ThreadID != "existing-thread" {
		t.Errorf("task.ThreadID = %q, want %q", ms.tasks[0].ThreadID, "existing-thread")
	}
	if len(ms.threads) != 1 {
		t.Errorf("expected no new thread created, got %d threads", len(ms.threads))
	}
}
