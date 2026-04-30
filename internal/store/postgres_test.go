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

func TestThreadLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	channelID := "chan-" + t.Name()
	threadTS := "ts-" + t.Name()

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ChannelID: channelID,
			ThreadTS:  threadTS,
			Repo:      "org/repo",
			IssueNum:  42,
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
	if got.ChannelRef.ChannelID != channelID {
		t.Errorf("ChannelID mismatch: got %q, want %q", got.ChannelRef.ChannelID, channelID)
	}
	if got.ChannelRef.ThreadTS != threadTS {
		t.Errorf("ThreadTS mismatch: got %q, want %q", got.ChannelRef.ThreadTS, threadTS)
	}
	if got.ChannelRef.Repo != "org/repo" {
		t.Errorf("Repo mismatch: got %q, want %q", got.ChannelRef.Repo, "org/repo")
	}
	if got.ChannelRef.IssueNum != 42 {
		t.Errorf("IssueNum mismatch: got %d, want %d", got.ChannelRef.IssueNum, 42)
	}

	// Get by channel ref
	gotByRef, err := s.GetThreadByChannelRef(ctx, "slack", channelID, threadTS)
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

func TestMessageLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create a thread to hold messages.
	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ChannelID: "msg-chan-" + t.Name(),
			ThreadTS:  "msg-ts-" + t.Name(),
		},
		Status: domain.ThreadActive,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Insert two messages with a small delay so their created_at differs.
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

	// Retrieve messages and verify order and content.
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

	// Verify chronological ordering.
	if !msgs[1].CreatedAt.After(msgs[0].CreatedAt) {
		t.Errorf("expected msg2.CreatedAt (%v) to be after msg1.CreatedAt (%v)",
			msgs[1].CreatedAt, msgs[0].CreatedAt)
	}
}

func TestTaskClaimAndUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create a thread for the task.
	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			ChannelID: "task-chan-" + t.Name(),
			ThreadTS:  "task-ts-" + t.Name(),
		},
		Status: domain.ThreadActive,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Create a pending task.
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

	// Claim the task.
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

	// No more pending tasks should be available.
	none, err := s.ClaimNextTask(ctx, "another-worker")
	if err != nil {
		t.Fatalf("ClaimNextTask (second call): %v", err)
	}
	if none != nil {
		t.Errorf("expected nil from second ClaimNextTask, got task %q", none.ID)
	}

	// Update the task to completed.
	claimed.Status = domain.TaskCompleted
	claimed.Result = "feature X implemented successfully"
	if err := s.UpdateTask(ctx, claimed); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// Verify the update persisted: claim should still return nil (no pending tasks).
	afterUpdate, err := s.ClaimNextTask(ctx, "yet-another-worker")
	if err != nil {
		t.Fatalf("ClaimNextTask after update: %v", err)
	}
	if afterUpdate != nil {
		t.Errorf("expected nil after completing task, got task %q", afterUpdate.ID)
	}
}
