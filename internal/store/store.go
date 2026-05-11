package store

import (
	"context"
	"errors"

	"github.com/creydr/ai-coworker/internal/domain"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Store defines the persistence interface for the AI coworker.
type Store interface {
	// GetThread retrieves a thread by its ID.
	GetThread(ctx context.Context, id string) (*domain.Thread, error)

	// GetThreadByChannelRef finds a thread by its channel and thread key.
	GetThreadByChannelRef(ctx context.Context, channel, threadKey string) (*domain.Thread, error)

	// CreateThread persists a new thread and populates its ID.
	CreateThread(ctx context.Context, t *domain.Thread) error

	// UpdateThreadStatus changes the status of an existing thread.
	UpdateThreadStatus(ctx context.Context, id string, status domain.ThreadStatus) error

	// GetMessages returns all messages for a thread ordered by created_at.
	GetMessages(ctx context.Context, threadID string) ([]domain.Message, error)

	// CreateMessage persists a new message and populates its ID.
	CreateMessage(ctx context.Context, m *domain.Message) error

	// CreateTask persists a new task and populates its ID.
	CreateTask(ctx context.Context, t *domain.Task) error

	// ClaimNextTask atomically picks the oldest pending task and assigns it to workerID.
	ClaimNextTask(ctx context.Context, workerID string) (*domain.Task, error)

	// ClaimPendingTasks atomically claims all pending tasks for the given thread
	// and assigns them to workerID.
	ClaimPendingTasks(ctx context.Context, threadID, workerID string) ([]*domain.Task, error)

	// UpdateTask saves changes to an existing task.
	UpdateTask(ctx context.Context, t *domain.Task) error

	// GetAdapterState retrieves a state value for the given adapter and key.
	// Returns ErrNotFound if no entry exists.
	GetAdapterState(ctx context.Context, adapter, key string) (string, error)

	// SetAdapterState upserts a state value for the given adapter and key.
	SetAdapterState(ctx context.Context, adapter, key, value string) error

	// Migrate runs all database migrations.
	Migrate(ctx context.Context) error

	// Close releases all resources held by the store.
	Close() error
}
