package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/creydr/ai-coworker/internal/domain"
)

// migration represents a single schema migration with a version and SQL body.
type migration struct {
	version int
	sql     string
}

//go:embed migrations/001_initial.sql
var migration001SQL string

//go:embed migrations/002_task_metadata.sql
var migration002SQL string

//go:embed migrations/003_channel_ref_refactor.sql
var migration003SQL string

//go:embed migrations/004_adapter_state.sql
var migration004SQL string

// migrations is the ordered list of all schema migrations. New migrations
// must be appended with the next sequential version number.
var migrations = []migration{
	{version: 1, sql: migration001SQL},
	{version: 2, sql: migration002SQL},
	{version: 3, sql: migration003SQL},
	{version: 4, sql: migration004SQL},
}

// scannable is satisfied by both pgx.Row and pgx.Rows.
type scannable interface {
	Scan(dest ...any) error
}

// PostgresStore implements Store backed by PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore creates a new PostgresStore, establishing and verifying the
// connection pool.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

// Migrate applies all outstanding schema migrations. Each migration runs in
// its own transaction and its version is recorded in the schema_migrations
// table so it is never re-applied.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	// Ensure the version-tracking table exists. This statement is
	// deliberately idempotent so it is safe to run on every startup.
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	for _, m := range migrations {
		// Check whether this migration has already been applied.
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`,
			m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}

		// Run the migration inside a transaction.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction for migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("running migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}
	return nil
}

// Close releases the connection pool.
func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

// ---------- threads ----------

func scanThread(row scannable) (*domain.Thread, error) {
	var t domain.Thread
	var propertiesJSON []byte
	err := row.Scan(
		&t.ID,
		&t.ChannelRef.Channel,
		&t.ChannelRef.ThreadKey,
		&propertiesJSON,
		&t.Status,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.ChannelRef.Properties, err = decodeMetadata(propertiesJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding thread properties: %w", err)
	}
	return &t, nil
}

const threadColumns = `id, channel, thread_key, properties, status, created_at, updated_at`

func (s *PostgresStore) GetThread(ctx context.Context, id string) (*domain.Thread, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+threadColumns+` FROM threads WHERE id = $1`, id)
	t, err := scanThread(row)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("thread %s: %w", id, ErrNotFound)
	}
	return t, err
}

func (s *PostgresStore) GetThreadByChannelRef(ctx context.Context, channel, threadKey string) (*domain.Thread, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+threadColumns+` FROM threads WHERE channel = $1 AND thread_key = $2`,
		channel, threadKey)
	t, err := scanThread(row)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("thread %s/%s: %w", channel, threadKey, ErrNotFound)
	}
	return t, err
}

func (s *PostgresStore) CreateThread(ctx context.Context, t *domain.Thread) error {
	t.ID = uuid.New().String()
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now

	propertiesJSON, err := encodeMetadata(t.ChannelRef.Properties)
	if err != nil {
		return fmt.Errorf("encoding thread properties: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO threads (id, channel, thread_key, properties, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		t.ID,
		t.ChannelRef.Channel,
		t.ChannelRef.ThreadKey,
		propertiesJSON,
		t.Status,
		t.CreatedAt,
		t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting thread: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpdateThreadStatus(ctx context.Context, id string, status domain.ThreadStatus) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE threads SET status = $1, updated_at = $2 WHERE id = $3`,
		status, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("updating thread status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("thread %s: %w", id, ErrNotFound)
	}
	return nil
}

// ---------- messages ----------

func (s *PostgresStore) GetMessages(ctx context.Context, threadID string) ([]domain.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, thread_id, role, content, created_at
		 FROM messages
		 WHERE thread_id = $1
		 ORDER BY created_at ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("querying messages: %w", err)
	}
	defer rows.Close()

	var msgs []domain.Message
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *PostgresStore) CreateMessage(ctx context.Context, m *domain.Message) error {
	m.ID = uuid.New().String()
	m.CreatedAt = time.Now().UTC()

	_, err := s.pool.Exec(ctx,
		`INSERT INTO messages (id, thread_id, role, content, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		m.ID, m.ThreadID, m.Role, m.Content, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting message: %w", err)
	}
	return nil
}

// ---------- tasks ----------

const taskColumns = `id, thread_id, intent, status, input, result, worker_id, metadata, created_at, updated_at`

func scanTask(row scannable) (*domain.Task, error) {
	var t domain.Task
	var metadataJSON []byte
	err := row.Scan(
		&t.ID, &t.ThreadID, &t.Intent, &t.Status,
		&t.Input, &t.Result, &t.WorkerID, &metadataJSON,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.Metadata, err = decodeMetadata(metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding task metadata: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) CreateTask(ctx context.Context, t *domain.Task) error {
	t.ID = uuid.New().String()
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now

	metadataJSON, err := encodeMetadata(t.Metadata)
	if err != nil {
		return fmt.Errorf("encoding task metadata: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO tasks (id, thread_id, intent, status, input, result, worker_id, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		t.ID, t.ThreadID, t.Intent, t.Status, t.Input, t.Result, t.WorkerID, metadataJSON, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting task: %w", err)
	}
	return nil
}

func (s *PostgresStore) ClaimNextTask(ctx context.Context, workerID string) (*domain.Task, error) {
	// Two mechanisms prevent multiple workers from claiming tasks on the
	// same thread concurrently:
	//
	// 1. NOT EXISTS: skip tasks whose thread already has a committed
	//    in_progress task (handles the steady-state case).
	// 2. pg_try_advisory_xact_lock: acquire a transaction-scoped lock on
	//    the thread_id hash. This closes the race window where multiple
	//    workers evaluate NOT EXISTS before any UPDATE has committed —
	//    only one transaction at a time can claim from a given thread.
	row := s.pool.QueryRow(ctx,
		`UPDATE tasks
		 SET status = 'in_progress', worker_id = $1, updated_at = $2
		 WHERE id = (
		     SELECT id FROM tasks
		     WHERE status = 'pending'
		       AND NOT EXISTS (
		           SELECT 1 FROM tasks t2
		           WHERE t2.thread_id = tasks.thread_id
		             AND t2.status = 'in_progress'
		       )
		       AND pg_try_advisory_xact_lock(0, hashtext(thread_id))
		     ORDER BY created_at
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+taskColumns,
		workerID, time.Now().UTC())

	t, err := scanTask(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claiming task: %w", err)
	}
	return t, nil
}

func (s *PostgresStore) ClaimPendingTasks(ctx context.Context, threadID, workerID string) ([]*domain.Task, error) {
	now := time.Now().UTC()
	rows, err := s.pool.Query(ctx,
		`UPDATE tasks
		 SET status = 'in_progress', worker_id = $1, updated_at = $2
		 WHERE id IN (
		     SELECT id FROM tasks
		     WHERE thread_id = $3
		       AND status = 'pending'
		     ORDER BY created_at
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+taskColumns,
		workerID, now, threadID)
	if err != nil {
		return nil, fmt.Errorf("claiming pending tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning claimed task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *PostgresStore) UpdateTask(ctx context.Context, t *domain.Task) error {
	t.UpdatedAt = time.Now().UTC()

	metadataJSON, err := encodeMetadata(t.Metadata)
	if err != nil {
		return fmt.Errorf("encoding task metadata: %w", err)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE tasks
		 SET intent = $1, status = $2, input = $3, result = $4, worker_id = $5, metadata = $6, updated_at = $7
		 WHERE id = $8`,
		t.Intent, t.Status, t.Input, t.Result, t.WorkerID, metadataJSON, t.UpdatedAt, t.ID)
	if err != nil {
		return fmt.Errorf("updating task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s: %w", t.ID, ErrNotFound)
	}
	return nil
}

// ---------- adapter state ----------

func (s *PostgresStore) GetAdapterState(ctx context.Context, adapter, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM adapter_state WHERE adapter = $1 AND key = $2`,
		adapter, key).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("adapter state %s/%s: %w", adapter, key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("getting adapter state: %w", err)
	}
	return value, nil
}

func (s *PostgresStore) SetAdapterState(ctx context.Context, adapter, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO adapter_state (adapter, key, value)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (adapter, key) DO UPDATE SET value = EXCLUDED.value`,
		adapter, key, value)
	if err != nil {
		return fmt.Errorf("setting adapter state: %w", err)
	}
	return nil
}

func encodeMetadata(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func decodeMetadata(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}
