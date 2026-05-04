//go:build integration

package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/creydr/ai-coworker/internal/domain"
	"github.com/creydr/ai-coworker/internal/store"
)

func testStore(t *testing.T) *store.PostgresStore {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := store.NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIntegrationThreadLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	threadKey := "C123/" + t.Name()

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: threadKey,
			Properties: map[string]string{
				"channel_id": "C123",
				"thread_ts":  t.Name(),
			},
		},
		Status: domain.ThreadActive,
	}

	// Create
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if thread.ID == "" {
		t.Fatal("expected ID to be populated after CreateThread")
	}
	if thread.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be populated")
	}
	if thread.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be populated")
	}

	// Get by ID
	got, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.ID != thread.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, thread.ID)
	}
	if got.Status != domain.ThreadActive {
		t.Errorf("Status mismatch: got %q, want %q", got.Status, domain.ThreadActive)
	}
	if got.ChannelRef.Channel != "slack" {
		t.Errorf("Channel mismatch: got %q, want %q", got.ChannelRef.Channel, "slack")
	}
	if got.ChannelRef.ThreadKey != threadKey {
		t.Errorf("ThreadKey mismatch: got %q, want %q", got.ChannelRef.ThreadKey, threadKey)
	}
	if got.ChannelRef.Properties["channel_id"] != "C123" {
		t.Errorf("Properties[channel_id] mismatch: got %q, want %q", got.ChannelRef.Properties["channel_id"], "C123")
	}
	if got.ChannelRef.Properties["thread_ts"] != t.Name() {
		t.Errorf("Properties[thread_ts] mismatch: got %q, want %q", got.ChannelRef.Properties["thread_ts"], t.Name())
	}

	// Get by channel ref
	gotByRef, err := s.GetThreadByChannelRef(ctx, "slack", threadKey)
	if err != nil {
		t.Fatalf("GetThreadByChannelRef: %v", err)
	}
	if gotByRef.ID != thread.ID {
		t.Errorf("GetThreadByChannelRef returned wrong thread: got %q, want %q", gotByRef.ID, thread.ID)
	}

	// Update status
	if err := s.UpdateThreadStatus(ctx, thread.ID, domain.ThreadResolved); err != nil {
		t.Fatalf("UpdateThreadStatus: %v", err)
	}
	updated, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread after update: %v", err)
	}
	if updated.Status != domain.ThreadResolved {
		t.Errorf("Status after update: got %q, want %q", updated.Status, domain.ThreadResolved)
	}
	if !updated.UpdatedAt.After(got.UpdatedAt) || updated.UpdatedAt.Equal(got.UpdatedAt) {
		t.Error("expected UpdatedAt to advance after status update")
	}
}

func TestIntegrationMessageLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ThreadKey: "msg-chan/" + t.Name(),
			Properties: map[string]string{
				"channel_id": "msg-chan",
				"thread_ts":  t.Name(),
			},
		},
		Status: domain.ThreadActive,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	msg1 := &domain.Message{
		ThreadID: thread.ID,
		Role:     domain.RoleUser,
		Content:  "Hello from user",
	}
	if err := s.CreateMessage(ctx, msg1); err != nil {
		t.Fatalf("CreateMessage (msg1): %v", err)
	}
	if msg1.ID == "" {
		t.Fatal("expected msg1 ID to be populated")
	}

	time.Sleep(10 * time.Millisecond)

	msg2 := &domain.Message{
		ThreadID: thread.ID,
		Role:     domain.RoleAssistant,
		Content:  "Hello from assistant",
	}
	if err := s.CreateMessage(ctx, msg2); err != nil {
		t.Fatalf("CreateMessage (msg2): %v", err)
	}

	msgs, err := s.GetMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	if msgs[0].ID != msg1.ID {
		t.Errorf("first message ID: got %q, want %q", msgs[0].ID, msg1.ID)
	}
	if msgs[0].Role != domain.RoleUser {
		t.Errorf("first message role: got %q, want %q", msgs[0].Role, domain.RoleUser)
	}
	if msgs[0].Content != "Hello from user" {
		t.Errorf("first message content: got %q, want %q", msgs[0].Content, "Hello from user")
	}

	if msgs[1].ID != msg2.ID {
		t.Errorf("second message ID: got %q, want %q", msgs[1].ID, msg2.ID)
	}
	if msgs[1].Role != domain.RoleAssistant {
		t.Errorf("second message role: got %q, want %q", msgs[1].Role, domain.RoleAssistant)
	}
	if msgs[1].Content != "Hello from assistant" {
		t.Errorf("second message content: got %q, want %q", msgs[1].Content, "Hello from assistant")
	}

	if !msgs[1].CreatedAt.After(msgs[0].CreatedAt) {
		t.Errorf("expected msg2.CreatedAt (%v) to be after msg1.CreatedAt (%v)",
			msgs[1].CreatedAt, msgs[0].CreatedAt)
	}
}

func TestIntegrationTaskClaimAndUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ThreadKey: "org/repo#" + t.Name(),
			Properties: map[string]string{
				"repo":      "org/repo",
				"issue_num": "1",
			},
		},
		Status: domain.ThreadActive,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	task := &domain.Task{
		ThreadID: thread.ID,
		Intent:   domain.IntentCodeTask,
		Status:   domain.TaskPending,
		Input:    "implement feature X",
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected task ID to be populated")
	}
	if task.Status != domain.TaskPending {
		t.Errorf("initial status: got %q, want %q", task.Status, domain.TaskPending)
	}

	workerID := "worker-" + t.Name()
	claimed, err := s.ClaimNextTask(ctx, workerID)
	if err != nil {
		t.Fatalf("ClaimNextTask: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimNextTask returned nil, expected a task")
	}
	if claimed.ID != task.ID {
		t.Errorf("claimed task ID: got %q, want %q", claimed.ID, task.ID)
	}
	if claimed.Status != domain.TaskInProgress {
		t.Errorf("claimed status: got %q, want %q", claimed.Status, domain.TaskInProgress)
	}
	if claimed.WorkerID != workerID {
		t.Errorf("claimed worker_id: got %q, want %q", claimed.WorkerID, workerID)
	}
	if claimed.Intent != domain.IntentCodeTask {
		t.Errorf("claimed intent: got %q, want %q", claimed.Intent, domain.IntentCodeTask)
	}
	if claimed.Input != "implement feature X" {
		t.Errorf("claimed input: got %q, want %q", claimed.Input, "implement feature X")
	}

	none, err := s.ClaimNextTask(ctx, "another-worker")
	if err != nil {
		t.Fatalf("ClaimNextTask (second call): %v", err)
	}
	if none != nil {
		t.Errorf("expected nil from second ClaimNextTask, got task %q", none.ID)
	}

	claimed.Status = domain.TaskCompleted
	claimed.Result = "feature X implemented successfully"
	if err := s.UpdateTask(ctx, claimed); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	afterUpdate, err := s.ClaimNextTask(ctx, "yet-another-worker")
	if err != nil {
		t.Fatalf("ClaimNextTask after update: %v", err)
	}
	if afterUpdate != nil {
		t.Errorf("expected nil after completing task, got task %q", afterUpdate.ID)
	}
}
