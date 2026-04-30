# AI Coworker Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go service that acts as an autonomous AI coworker, reachable on Slack and GitHub, executing tasks end-to-end with sandboxed code execution.

**Architecture:** Event-driven with thread-based conversation model. Channel adapters normalize events, a worker pool processes them using LLM intent classification, and executors (Claude Code in containers, or direct LLM calls) handle the work. PostgreSQL stores conversation state and the task queue.

**Tech Stack:** Go 1.22+, pgx/v5 + sqlc, slack-go/slack (Socket Mode), google/go-github + cbrgm/githubevents, anthropic-sdk-go, moby/moby client, koanf, chi/v5.

---

### Task 1: Project Scaffolding

**Files:**
- Create: `go.mod`
- Create: `cmd/ai-coworker/main.go`
- Create: `internal/config/config.go`
- Create: `config.yaml`
- Create: `.gitignore`

**Step 1: Initialize Go module**

Run:
```bash
go mod init github.com/creydr/ai-coworker
```

**Step 2: Create .gitignore**

Create `.gitignore`:
```
ai-coworker
*.exe
.env
.idea/
vendor/
```

**Step 3: Create config struct and loader**

Create `internal/config/config.go`:
```go
package config

import (
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Database DatabaseConfig `koanf:"database"`
	LLM      LLMConfig      `koanf:"llm"`
	Slack    SlackConfig    `koanf:"slack"`
	GitHub   GitHubConfig   `koanf:"github"`
	Sandbox  SandboxConfig  `koanf:"sandbox"`
	Workers  int            `koanf:"workers"`
}

type DatabaseConfig struct {
	URL string `koanf:"url"`
}

type LLMConfig struct {
	Provider string `koanf:"provider"`
	APIKey   string `koanf:"api_key"`
	Model    string `koanf:"model"`
}

type SlackConfig struct {
	Enabled  bool   `koanf:"enabled"`
	AppToken string `koanf:"app_token"`
	BotToken string `koanf:"bot_token"`
}

type GitHubConfig struct {
	Enabled       bool   `koanf:"enabled"`
	AppID         int64  `koanf:"app_id"`
	PrivateKey    string `koanf:"private_key"`
	WebhookSecret string `koanf:"webhook_secret"`
	BotUsername   string `koanf:"bot_username"`
}

type SandboxConfig struct {
	Runtime        string `koanf:"runtime"`
	Image          string `koanf:"image"`
	TimeoutSeconds int    `koanf:"timeout_seconds"`
	CPULimit       string `koanf:"cpu_limit"`
	MemoryLimit    string `koanf:"memory_limit"`
	Namespace      string `koanf:"namespace"`
}

func Load(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, err
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	if cfg.Workers == 0 {
		cfg.Workers = 4
	}
	if cfg.Sandbox.TimeoutSeconds == 0 {
		cfg.Sandbox.TimeoutSeconds = 600
	}
	return &cfg, nil
}
```

**Step 4: Create default config.yaml**

Create `config.yaml`:
```yaml
database:
  url: "postgres://ai-coworker:password@localhost:5432/ai-coworker?sslmode=disable"

llm:
  provider: "claude"
  api_key: "${ANTHROPIC_API_KEY}"
  model: "claude-sonnet-4-6"

slack:
  enabled: false
  app_token: "${SLACK_APP_TOKEN}"
  bot_token: "${SLACK_BOT_TOKEN}"

github:
  enabled: false
  app_id: 0
  private_key: "${GITHUB_PRIVATE_KEY}"
  webhook_secret: "${GITHUB_WEBHOOK_SECRET}"
  bot_username: "ai-coworker"

sandbox:
  runtime: "docker"
  image: "ai-coworker-sandbox:latest"
  timeout_seconds: 600
  cpu_limit: "2"
  memory_limit: "4Gi"
  namespace: "ai-coworker"

workers: 4
```

**Step 5: Create minimal main.go**

Create `cmd/ai-coworker/main.go`:
```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/cstabler/ai-coworker/internal/config"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	fmt.Printf("ai-coworker starting with %d workers\n", cfg.Workers)
}
```

**Step 6: Install dependencies and verify compilation**

Run:
```bash
go get github.com/knadh/koanf/v2 github.com/knadh/koanf/parsers/yaml github.com/knadh/koanf/providers/file
go build ./cmd/ai-coworker/
```

**Step 7: Commit**

```bash
git add .gitignore go.mod go.sum cmd/ internal/config/ config.yaml
git commit -m "feat: project scaffolding with config loader"
```

---

### Task 2: Domain Types

**Files:**
- Create: `internal/domain/event.go`
- Create: `internal/domain/thread.go`
- Create: `internal/domain/task.go`
- Create: `internal/domain/message.go`

**Step 1: Create domain types**

Create `internal/domain/event.go`:
```go
package domain

type IncomingEvent struct {
	Channel    string
	ChannelRef ChannelRef
	ThreadID   string
	UserID     string
	Content    string
	Metadata   map[string]string
}

type ChannelRef struct {
	Channel   string
	ChannelID string
	ThreadTS  string
	Repo      string
	IssueNum  int
	CommentID int64
}
```

Create `internal/domain/thread.go`:
```go
package domain

import "time"

type Thread struct {
	ID         string
	ChannelRef ChannelRef
	Status     ThreadStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadResolved ThreadStatus = "resolved"
	ThreadExpired  ThreadStatus = "expired"
)
```

Create `internal/domain/task.go`:
```go
package domain

import "time"

type Task struct {
	ID        string
	ThreadID  string
	Intent    Intent
	Status    TaskStatus
	Input     string
	Result    string
	WorkerID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
)

type Intent string

const (
	IntentCodeTask    Intent = "code_task"
	IntentQuestion    Intent = "question"
	IntentDiscussion  Intent = "discussion"
	IntentReview      Intent = "review"
	IntentUnknown     Intent = "unknown"
)
```

Create `internal/domain/message.go`:
```go
package domain

import "time"

type Message struct {
	ID        string
	ThreadID  string
	Role      Role
	Content   string
	CreatedAt time.Time
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)
```

**Step 2: Verify compilation**

Run:
```bash
go build ./internal/domain/
```

**Step 3: Commit**

```bash
git add internal/domain/
git commit -m "feat: add domain types for events, threads, tasks, messages"
```

---

### Task 3: PostgreSQL Store — Schema & Migrations

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/migrations/001_initial.sql`
- Create: `internal/store/postgres.go`
- Create: `docker-compose.yaml`

**Step 1: Create docker-compose.yaml for local dev**

Create `docker-compose.yaml`:
```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: ai-coworker
      POSTGRES_PASSWORD: password
      POSTGRES_DB: ai-coworker
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

volumes:
  pgdata:
```

**Step 2: Create the Store interface**

Create `internal/store/store.go`:
```go
package store

import (
	"context"

	"github.com/cstabler/ai-coworker/internal/domain"
)

type Store interface {
	// Threads
	GetThread(ctx context.Context, id string) (*domain.Thread, error)
	GetThreadByChannelRef(ctx context.Context, channel, channelID, threadTS string) (*domain.Thread, error)
	CreateThread(ctx context.Context, thread *domain.Thread) error
	UpdateThreadStatus(ctx context.Context, id string, status domain.ThreadStatus) error

	// Messages
	GetMessages(ctx context.Context, threadID string) ([]domain.Message, error)
	CreateMessage(ctx context.Context, msg *domain.Message) error

	// Tasks
	CreateTask(ctx context.Context, task *domain.Task) error
	ClaimNextTask(ctx context.Context, workerID string) (*domain.Task, error)
	UpdateTask(ctx context.Context, task *domain.Task) error

	// Lifecycle
	Migrate(ctx context.Context) error
	Close() error
}
```

**Step 3: Create the SQL migration**

Create `internal/store/migrations/001_initial.sql`:
```sql
CREATE TABLE IF NOT EXISTS threads (
    id         TEXT PRIMARY KEY,
    channel    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    thread_ts  TEXT NOT NULL DEFAULT '',
    repo       TEXT NOT NULL DEFAULT '',
    issue_num  INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(channel, channel_id, thread_ts)
);

CREATE TABLE IF NOT EXISTS messages (
    id         TEXT PRIMARY KEY,
    thread_id  TEXT NOT NULL REFERENCES threads(id),
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messages_thread_id ON messages(thread_id, created_at);

CREATE TABLE IF NOT EXISTS tasks (
    id         TEXT PRIMARY KEY,
    thread_id  TEXT NOT NULL REFERENCES threads(id),
    intent     TEXT NOT NULL DEFAULT 'unknown',
    status     TEXT NOT NULL DEFAULT 'pending',
    input      TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL DEFAULT '',
    worker_id  TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status, created_at);
```

**Step 4: Implement PostgreSQL store**

Create `internal/store/postgres.go`:
```go
package store

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_initial.sql
var migrationSQL string

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, migrationSQL)
	return err
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PostgresStore) GetThread(ctx context.Context, id string) (*domain.Thread, error) {
	row := s.pool.QueryRow(ctx,
		"SELECT id, channel, channel_id, thread_ts, repo, issue_num, status, created_at, updated_at FROM threads WHERE id = $1", id)
	return scanThread(row)
}

func (s *PostgresStore) GetThreadByChannelRef(ctx context.Context, channel, channelID, threadTS string) (*domain.Thread, error) {
	row := s.pool.QueryRow(ctx,
		"SELECT id, channel, channel_id, thread_ts, repo, issue_num, status, created_at, updated_at FROM threads WHERE channel = $1 AND channel_id = $2 AND thread_ts = $3", channel, channelID, threadTS)
	return scanThread(row)
}

func (s *PostgresStore) CreateThread(ctx context.Context, thread *domain.Thread) error {
	if thread.ID == "" {
		thread.ID = uuid.NewString()
	}
	now := time.Now()
	thread.CreatedAt = now
	thread.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		"INSERT INTO threads (id, channel, channel_id, thread_ts, repo, issue_num, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		thread.ID, thread.ChannelRef.Channel, thread.ChannelRef.ChannelID, thread.ChannelRef.ThreadTS,
		thread.ChannelRef.Repo, thread.ChannelRef.IssueNum, thread.Status, thread.CreatedAt, thread.UpdatedAt)
	return err
}

func (s *PostgresStore) UpdateThreadStatus(ctx context.Context, id string, status domain.ThreadStatus) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE threads SET status = $1, updated_at = NOW() WHERE id = $2", status, id)
	return err
}

func (s *PostgresStore) GetMessages(ctx context.Context, threadID string) ([]domain.Message, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT id, thread_id, role, content, created_at FROM messages WHERE thread_id = $1 ORDER BY created_at ASC", threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []domain.Message
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *PostgresStore) CreateMessage(ctx context.Context, msg *domain.Message) error {
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	msg.CreatedAt = time.Now()
	_, err := s.pool.Exec(ctx,
		"INSERT INTO messages (id, thread_id, role, content, created_at) VALUES ($1, $2, $3, $4, $5)",
		msg.ID, msg.ThreadID, msg.Role, msg.Content, msg.CreatedAt)
	return err
}

func (s *PostgresStore) CreateTask(ctx context.Context, task *domain.Task) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	now := time.Now()
	task.CreatedAt = now
	task.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		"INSERT INTO tasks (id, thread_id, intent, status, input, result, worker_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		task.ID, task.ThreadID, task.Intent, task.Status, task.Input, task.Result, task.WorkerID, task.CreatedAt, task.UpdatedAt)
	return err
}

func (s *PostgresStore) ClaimNextTask(ctx context.Context, workerID string) (*domain.Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE tasks SET status = 'in_progress', worker_id = $1, updated_at = NOW()
		 WHERE id = (SELECT id FROM tasks WHERE status = 'pending' ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED)
		 RETURNING id, thread_id, intent, status, input, result, worker_id, created_at, updated_at`, workerID)

	var t domain.Task
	err := row.Scan(&t.ID, &t.ThreadID, &t.Intent, &t.Status, &t.Input, &t.Result, &t.WorkerID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *PostgresStore) UpdateTask(ctx context.Context, task *domain.Task) error {
	task.UpdatedAt = time.Now()
	_, err := s.pool.Exec(ctx,
		"UPDATE tasks SET intent = $1, status = $2, input = $3, result = $4, worker_id = $5, updated_at = $6 WHERE id = $7",
		task.Intent, task.Status, task.Input, task.Result, task.WorkerID, task.UpdatedAt, task.ID)
	return err
}

type scannable interface {
	Scan(dest ...any) error
}

func scanThread(row scannable) (*domain.Thread, error) {
	var t domain.Thread
	err := row.Scan(&t.ID, &t.ChannelRef.Channel, &t.ChannelRef.ChannelID, &t.ChannelRef.ThreadTS,
		&t.ChannelRef.Repo, &t.ChannelRef.IssueNum, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
```

**Step 5: Install dependencies and verify**

Run:
```bash
go get github.com/jackc/pgx/v5 github.com/google/uuid
go build ./internal/store/
```

**Step 6: Commit**

```bash
git add docker-compose.yaml internal/store/ go.mod go.sum
git commit -m "feat: PostgreSQL store with migrations, thread/message/task persistence"
```

---

### Task 4: LLM Provider Interface & Claude Implementation

**Files:**
- Create: `internal/llm/provider.go`
- Create: `internal/llm/claude/claude.go`

**Step 1: Create the provider interface**

Create `internal/llm/provider.go`:
```go
package llm

import "context"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type Provider interface {
	Chat(ctx context.Context, messages []Message) (string, error)
}
```

**Step 2: Create Claude provider implementation**

Create `internal/llm/claude/claude.go`:
```go
package claude

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/cstabler/ai-coworker/internal/llm"
)

type Provider struct {
	client *anthropic.Client
	model  string
}

func New(apiKey, model string) *Provider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Provider{client: client, model: model}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: 4096,
		Messages:  convertMessages(messages),
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("claude chat: %w", err)
	}

	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("claude chat: no text response")
}

func convertMessages(msgs []llm.Message) []anthropic.MessageParam {
	params := make([]anthropic.MessageParam, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			params[i] = anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content))
		case llm.RoleAssistant:
			params[i] = anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content))
		}
	}
	return params
}
```

**Step 3: Install SDK and verify**

Run:
```bash
go get github.com/anthropics/anthropic-sdk-go
go build ./internal/llm/...
```

**Step 4: Commit**

```bash
git add internal/llm/ go.mod go.sum
git commit -m "feat: LLM provider interface with Claude implementation"
```

---

### Task 5: Channel Adapter Interface

**Files:**
- Create: `internal/adapter/adapter.go`

**Step 1: Create adapter interface**

Create `internal/adapter/adapter.go`:
```go
package adapter

import (
	"context"

	"github.com/cstabler/ai-coworker/internal/domain"
)

type EventHandler func(ctx context.Context, event domain.IncomingEvent) error

type Adapter interface {
	Start(ctx context.Context, handler EventHandler) error
	SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error
	Acknowledge(ctx context.Context, ref domain.ChannelRef) error
	Name() string
}
```

**Step 2: Verify compilation**

Run:
```bash
go build ./internal/adapter/
```

**Step 3: Commit**

```bash
git add internal/adapter/
git commit -m "feat: channel adapter interface"
```

---

### Task 6: Slack Adapter

**Files:**
- Create: `internal/adapter/slack/slack.go`

**Step 1: Implement Slack adapter**

Create `internal/adapter/slack/slack.go`:
```go
package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cstabler/ai-coworker/internal/adapter"
	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/slack-go/slack/slackevents"
)

type Adapter struct {
	client       *slack.Client
	socketClient *socketmode.Client
	botUserID    string
}

func New(appToken, botToken string) *Adapter {
	client := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	socketClient := socketmode.New(client)
	return &Adapter{client: client, socketClient: socketClient}
}

func (a *Adapter) Name() string { return "slack" }

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	authResp, err := a.client.AuthTest()
	if err != nil {
		return fmt.Errorf("slack auth test: %w", err)
	}
	a.botUserID = authResp.UserID
	slog.Info("slack adapter connected", "bot_user_id", a.botUserID)

	go func() {
		for evt := range a.socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				a.socketClient.Ack(*evt.Request)
				a.handleEventsAPI(ctx, eventsAPIEvent, handler)
			}
		}
	}()

	return a.socketClient.RunContext(ctx)
}

func (a *Adapter) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent, handler adapter.EventHandler) {
	switch ev := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		content := strings.TrimSpace(strings.Replace(ev.Text, fmt.Sprintf("<@%s>", a.botUserID), "", 1))
		threadTS := ev.ThreadTimeStamp
		if threadTS == "" {
			threadTS = ev.TimeStamp
		}

		incoming := domain.IncomingEvent{
			Channel: "slack",
			ChannelRef: domain.ChannelRef{
				Channel:   "slack",
				ChannelID: ev.Channel,
				ThreadTS:  threadTS,
			},
			ThreadID: fmt.Sprintf("slack-%s-%s", ev.Channel, threadTS),
			UserID:   ev.User,
			Content:  content,
		}

		if err := handler(ctx, incoming); err != nil {
			slog.Error("handling slack event", "error", err)
		}
	}
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	_, _, err := a.client.PostMessageContext(ctx, ref.ChannelID,
		slack.MsgOptionText(message, false),
		slack.MsgOptionTS(ref.ThreadTS))
	return err
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	return a.client.AddReactionContext(ctx, "eyes", slack.ItemRef{
		Channel:   ref.ChannelID,
		Timestamp: ref.ThreadTS,
	})
}
```

**Step 2: Install dependency and verify**

Run:
```bash
go get github.com/slack-go/slack
go build ./internal/adapter/slack/
```

**Step 3: Commit**

```bash
git add internal/adapter/slack/ go.mod go.sum
git commit -m "feat: Slack adapter with Socket Mode"
```

---

### Task 7: GitHub Adapter

**Files:**
- Create: `internal/adapter/github/github.go`

**Step 1: Implement GitHub adapter**

Create `internal/adapter/github/github.go`:
```go
package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cstabler/ai-coworker/internal/adapter"
	"github.com/cstabler/ai-coworker/internal/domain"
	gh "github.com/google/go-github/v68/github"
)

type Adapter struct {
	client        *gh.Client
	webhookSecret []byte
	botUsername   string
	server       *http.Server
}

func New(webhookSecret, botUsername string) *Adapter {
	client := gh.NewClient(nil)
	return &Adapter{
		client:        client,
		webhookSecret: []byte(webhookSecret),
		botUsername:    botUsername,
	}
}

func (a *Adapter) WithClient(client *gh.Client) *Adapter {
	a.client = client
	return a
}

func (a *Adapter) Name() string { return "github" }

func (a *Adapter) Start(ctx context.Context, handler adapter.EventHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/github", func(w http.ResponseWriter, r *http.Request) {
		a.handleWebhook(r.Context(), w, r, handler)
	})

	a.server = &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		<-ctx.Done()
		a.server.Close()
	}()

	slog.Info("github adapter listening", "addr", ":8080")
	if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (a *Adapter) handleWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request, handler adapter.EventHandler) {
	payload, err := gh.ValidatePayload(r, a.webhookSecret)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event, err := gh.ParseWebHook(gh.WebHookType(r), payload)
	if err != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	switch e := event.(type) {
	case *gh.IssueCommentEvent:
		if e.GetAction() != "created" {
			return
		}
		if !strings.Contains(e.GetComment().GetBody(), fmt.Sprintf("@%s", a.botUsername)) {
			return
		}
		a.handleIssueComment(ctx, e, handler)

	case *gh.PullRequestReviewCommentEvent:
		if e.GetAction() != "created" {
			return
		}
		a.handlePRReviewComment(ctx, e, handler)

	case *gh.PullRequestReviewEvent:
		if e.GetAction() != "submitted" {
			return
		}
		a.handlePRReview(ctx, e, handler)
	}
}

func (a *Adapter) handleIssueComment(ctx context.Context, e *gh.IssueCommentEvent, handler adapter.EventHandler) {
	repo := e.GetRepo().GetFullName()
	issueNum := e.GetIssue().GetNumber()
	body := e.GetComment().GetBody()
	content := strings.TrimSpace(strings.Replace(body, fmt.Sprintf("@%s", a.botUsername), "", 1))

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:   "github",
			Repo:      repo,
			IssueNum:  issueNum,
			CommentID: e.GetComment().GetID(),
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repo, issueNum),
		UserID:   e.GetSender().GetLogin(),
		Content:  content,
		Metadata: map[string]string{
			"repo":      repo,
			"issue_num": fmt.Sprintf("%d", issueNum),
			"is_pr":     fmt.Sprintf("%t", e.GetIssue().IsPullRequest()),
		},
	}

	if err := handler(ctx, incoming); err != nil {
		slog.Error("handling github issue comment", "error", err)
	}
}

func (a *Adapter) handlePRReviewComment(ctx context.Context, e *gh.PullRequestReviewCommentEvent, handler adapter.EventHandler) {
	repo := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     repo,
			IssueNum: prNum,
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repo, prNum),
		UserID:   e.GetSender().GetLogin(),
		Content:  e.GetComment().GetBody(),
		Metadata: map[string]string{
			"repo":      repo,
			"issue_num": fmt.Sprintf("%d", prNum),
			"is_pr":     "true",
			"type":      "review_comment",
			"path":      e.GetComment().GetPath(),
		},
	}

	if err := handler(ctx, incoming); err != nil {
		slog.Error("handling github pr review comment", "error", err)
	}
}

func (a *Adapter) handlePRReview(ctx context.Context, e *gh.PullRequestReviewEvent, handler adapter.EventHandler) {
	repo := e.GetRepo().GetFullName()
	prNum := e.GetPullRequest().GetNumber()

	incoming := domain.IncomingEvent{
		Channel: "github",
		ChannelRef: domain.ChannelRef{
			Channel:  "github",
			Repo:     repo,
			IssueNum: prNum,
		},
		ThreadID: fmt.Sprintf("github-%s-%d", repo, prNum),
		UserID:   e.GetSender().GetLogin(),
		Content:  e.GetReview().GetBody(),
		Metadata: map[string]string{
			"repo":         repo,
			"issue_num":    fmt.Sprintf("%d", prNum),
			"is_pr":        "true",
			"type":         "review",
			"review_state": e.GetReview().GetState(),
		},
	}

	if err := handler(ctx, incoming); err != nil {
		slog.Error("handling github pr review", "error", err)
	}
}

func (a *Adapter) SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error {
	parts := strings.SplitN(ref.Repo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format: %s", ref.Repo)
	}
	comment := &gh.IssueComment{Body: gh.Ptr(message)}
	_, _, err := a.client.Issues.CreateComment(ctx, parts[0], parts[1], ref.IssueNum, comment)
	return err
}

func (a *Adapter) Acknowledge(ctx context.Context, ref domain.ChannelRef) error {
	parts := strings.SplitN(ref.Repo, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	_, _, err := a.client.Reactions.CreateIssueCommentReaction(ctx, parts[0], parts[1], ref.CommentID, "eyes")
	return err
}
```

**Step 2: Install dependency and verify**

Run:
```bash
go get github.com/google/go-github/v68
go build ./internal/adapter/github/
```

**Step 3: Commit**

```bash
git add internal/adapter/github/ go.mod go.sum
git commit -m "feat: GitHub adapter with webhook handling for issues and PR reviews"
```

---

### Task 8: Sandbox Interface & Docker Implementation

**Files:**
- Create: `internal/sandbox/sandbox.go`
- Create: `internal/sandbox/docker/docker.go`
- Create: `sandbox/Dockerfile`

**Step 1: Create sandbox interface**

Create `internal/sandbox/sandbox.go`:
```go
package sandbox

import "context"

type ExecRequest struct {
	Image      string
	CloneURL   string
	Branch     string
	Prompt     string
	EnvVars    map[string]string
	Timeout    int
	CPULimit   string
	MemLimit   string
}

type ExecResult struct {
	Output   string
	ExitCode int
	Error    string
}

type Runtime interface {
	Exec(ctx context.Context, req ExecRequest) (*ExecResult, error)
}
```

**Step 2: Create sandbox Dockerfile**

Create `sandbox/Dockerfile`:
```dockerfile
FROM node:22-bookworm

RUN apt-get update && apt-get install -y git gh && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

WORKDIR /workspace

ENTRYPOINT ["claude", "--dangerously-skip-permissions", "-p"]
```

**Step 3: Implement Docker sandbox runtime**

Create `internal/sandbox/docker/docker.go`:
```go
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/cstabler/ai-coworker/internal/sandbox"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type Runtime struct {
	client *client.Client
}

func New() (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Runtime{client: cli}, nil
}

func (r *Runtime) Exec(ctx context.Context, req sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	env := []string{}
	for k, v := range req.EnvVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	cmd := []string{"claude", "--dangerously-skip-permissions", "-p", req.Prompt}

	if req.CloneURL != "" {
		cloneCmd := fmt.Sprintf("git clone %s /workspace/repo && cd /workspace/repo", req.CloneURL)
		if req.Branch != "" {
			cloneCmd += fmt.Sprintf(" && git checkout %s", req.Branch)
		}
		cmd = []string{"sh", "-c", cloneCmd + fmt.Sprintf(` && claude --dangerously-skip-permissions -p "%s"`, req.Prompt)}
	}

	containerCfg := &container.Config{
		Image: req.Image,
		Cmd:   cmd,
		Env:   env,
	}

	hostCfg := &container.HostConfig{}
	if req.CPULimit != "" || req.MemLimit != "" {
		hostCfg.Resources = container.Resources{}
	}

	resp, err := r.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	containerID := resp.ID
	defer func() {
		rmCtx := context.Background()
		r.client.ContainerRemove(rmCtx, containerID, container.RemoveOptions{Force: true})
	}()

	slog.Info("starting sandbox container", "container_id", containerID[:12])

	if err := r.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	statusCh, errCh := r.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	var exitCode int
	select {
	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("wait container: %w", err)
		}
	case status := <-statusCh:
		exitCode = int(status.StatusCode)
	}

	logReader, err := r.client.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}
	defer logReader.Close()

	var stdout, stderr bytes.Buffer
	stdcopy.StdCopy(&stdout, &stderr, logReader)

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n--- stderr ---\n" + stderr.String()
	}

	return &sandbox.ExecResult{
		Output:   output,
		ExitCode: exitCode,
	}, nil
}

// Ensure Runtime implements sandbox.Runtime
var _ sandbox.Runtime = (*Runtime)(nil)
```

**Step 4: Install dependencies and verify**

Run:
```bash
go get github.com/docker/docker
go build ./internal/sandbox/...
```

**Step 5: Commit**

```bash
git add internal/sandbox/ sandbox/ go.mod go.sum
git commit -m "feat: sandbox interface with Docker runtime and Claude Code container image"
```

---

### Task 9: Executor Interface & Implementations

**Files:**
- Create: `internal/executor/executor.go`
- Create: `internal/executor/claudecode/claudecode.go`
- Create: `internal/executor/llmexec/llmexec.go`

**Step 1: Create executor interface**

Create `internal/executor/executor.go`:
```go
package executor

import (
	"context"

	"github.com/cstabler/ai-coworker/internal/domain"
)

type Context struct {
	Thread   *domain.Thread
	Messages []domain.Message
	Task     *domain.Task
	Event    *domain.IncomingEvent
}

type Result struct {
	Response string
	Metadata map[string]string
}

type Executor interface {
	Execute(ctx context.Context, execCtx *Context) (*Result, error)
}
```

**Step 2: Implement Claude Code executor**

Create `internal/executor/claudecode/claudecode.go`:
```go
package claudecode

import (
	"context"
	"fmt"
	"strings"

	"github.com/cstabler/ai-coworker/internal/executor"
	"github.com/cstabler/ai-coworker/internal/sandbox"
)

type Executor struct {
	runtime sandbox.Runtime
	image   string
	envVars map[string]string
}

func New(runtime sandbox.Runtime, image string, envVars map[string]string) *Executor {
	return &Executor{runtime: runtime, image: image, envVars: envVars}
}

func (e *Executor) Execute(ctx context.Context, execCtx *executor.Context) (*executor.Result, error) {
	prompt := buildPrompt(execCtx)

	cloneURL := ""
	branch := ""
	if repo, ok := execCtx.Event.Metadata["repo"]; ok {
		cloneURL = fmt.Sprintf("https://github.com/%s.git", repo)
	}

	result, err := e.runtime.Exec(ctx, sandbox.ExecRequest{
		Image:    e.image,
		CloneURL: cloneURL,
		Branch:   branch,
		Prompt:   prompt,
		EnvVars:  e.envVars,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox exec: %w", err)
	}

	if result.ExitCode != 0 {
		return &executor.Result{
			Response: fmt.Sprintf("Task failed (exit code %d):\n```\n%s\n```", result.ExitCode, result.Output),
		}, nil
	}

	return &executor.Result{
		Response: result.Output,
	}, nil
}

func buildPrompt(execCtx *executor.Context) string {
	var sb strings.Builder
	sb.WriteString("You are an AI coworker. ")

	if repo, ok := execCtx.Event.Metadata["repo"]; ok {
		sb.WriteString(fmt.Sprintf("You are working on the repository %s. ", repo))
	}
	if issueNum, ok := execCtx.Event.Metadata["issue_num"]; ok {
		sb.WriteString(fmt.Sprintf("This is about issue/PR #%s. ", issueNum))
	}

	sb.WriteString("\n\nConversation history:\n")
	for _, msg := range execCtx.Messages {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	sb.WriteString(fmt.Sprintf("\nLatest request: %s\n", execCtx.Task.Input))
	sb.WriteString("\nComplete the task. If it's a code change, create a branch and a pull request.")

	return sb.String()
}
```

**Step 3: Implement LLM executor for non-code tasks**

Create `internal/executor/llmexec/llmexec.go`:
```go
package llmexec

import (
	"context"
	"fmt"

	"github.com/cstabler/ai-coworker/internal/executor"
	"github.com/cstabler/ai-coworker/internal/llm"
)

type Executor struct {
	provider llm.Provider
}

func New(provider llm.Provider) *Executor {
	return &Executor{provider: provider}
}

func (e *Executor) Execute(ctx context.Context, execCtx *executor.Context) (*executor.Result, error) {
	messages := make([]llm.Message, 0, len(execCtx.Messages)+2)

	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "You are an AI coworker. You help with questions, discussions, and planning. Be concise and helpful.",
	})

	for _, msg := range execCtx.Messages {
		role := llm.RoleUser
		if msg.Role == "assistant" {
			role = llm.RoleAssistant
		}
		messages = append(messages, llm.Message{Role: role, Content: msg.Content})
	}

	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: execCtx.Task.Input,
	})

	response, err := e.provider.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("llm chat: %w", err)
	}

	return &executor.Result{Response: response}, nil
}
```

**Step 4: Verify compilation**

Run:
```bash
go build ./internal/executor/...
```

**Step 5: Commit**

```bash
git add internal/executor/
git commit -m "feat: executor interface with Claude Code (sandboxed) and LLM implementations"
```

---

### Task 10: Engine — Router & Worker Pool

**Files:**
- Create: `internal/engine/router.go`
- Create: `internal/engine/worker.go`
- Create: `internal/engine/intent.go`

**Step 1: Create intent classifier**

Create `internal/engine/intent.go`:
```go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/cstabler/ai-coworker/internal/llm"
)

type IntentClassifier struct {
	provider llm.Provider
}

func NewIntentClassifier(provider llm.Provider) *IntentClassifier {
	return &IntentClassifier{provider: provider}
}

func (c *IntentClassifier) Classify(ctx context.Context, event domain.IncomingEvent, history []domain.Message) (domain.Intent, error) {
	prompt := fmt.Sprintf(`Classify the following message into exactly one of these categories:
- code_task: the user wants code written, a bug fixed, a feature implemented, or a PR created
- review: the user is providing code review feedback that needs to be addressed
- question: the user is asking a question that can be answered without writing code
- discussion: the user wants to discuss or plan something

Message: %s

Respond with ONLY the category name, nothing else.`, event.Content)

	if _, ok := event.Metadata["review_state"]; ok {
		return domain.IntentReview, nil
	}
	if event.Metadata["type"] == "review_comment" {
		return domain.IntentReview, nil
	}

	resp, err := c.provider.Chat(ctx, []llm.Message{
		{Role: llm.RoleUser, Content: prompt},
	})
	if err != nil {
		return domain.IntentUnknown, err
	}

	resp = strings.TrimSpace(strings.ToLower(resp))
	switch {
	case strings.Contains(resp, "code_task"):
		return domain.IntentCodeTask, nil
	case strings.Contains(resp, "review"):
		return domain.IntentReview, nil
	case strings.Contains(resp, "question"):
		return domain.IntentQuestion, nil
	case strings.Contains(resp, "discussion"):
		return domain.IntentDiscussion, nil
	default:
		return domain.IntentUnknown, nil
	}
}
```

**Step 2: Create the event router**

Create `internal/engine/router.go`:
```go
package engine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cstabler/ai-coworker/internal/adapter"
	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/cstabler/ai-coworker/internal/store"
	"github.com/jackc/pgx/v5"
)

type Router struct {
	store    store.Store
	adapters map[string]adapter.Adapter
}

func NewRouter(s store.Store) *Router {
	return &Router{store: s, adapters: make(map[string]adapter.Adapter)}
}

func (r *Router) RegisterAdapter(a adapter.Adapter) {
	r.adapters[a.Name()] = a
}

func (r *Router) HandleEvent(ctx context.Context, event domain.IncomingEvent) error {
	if a, ok := r.adapters[event.Channel]; ok {
		a.Acknowledge(ctx, event.ChannelRef)
	}

	thread, err := r.store.GetThreadByChannelRef(ctx, event.ChannelRef.Channel, event.ChannelRef.ChannelID, event.ChannelRef.ThreadTS)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if thread == nil {
		thread = &domain.Thread{
			ChannelRef: event.ChannelRef,
			Status:     domain.ThreadActive,
		}
		if err := r.store.CreateThread(ctx, thread); err != nil {
			return err
		}
		slog.Info("created new thread", "thread_id", thread.ID, "channel", event.Channel)
	}

	if err := r.store.CreateMessage(ctx, &domain.Message{
		ThreadID: thread.ID,
		Role:     domain.RoleUser,
		Content:  event.Content,
	}); err != nil {
		return err
	}

	task := &domain.Task{
		ThreadID: thread.ID,
		Status:   domain.TaskPending,
		Input:    event.Content,
	}
	return r.store.CreateTask(ctx, task)
}

func (r *Router) GetAdapter(name string) adapter.Adapter {
	return r.adapters[name]
}
```

**Step 3: Create the worker pool**

Create `internal/engine/worker.go`:
```go
package engine

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/cstabler/ai-coworker/internal/executor"
	"github.com/cstabler/ai-coworker/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type WorkerPool struct {
	store      store.Store
	router     *Router
	classifier *IntentClassifier
	codeExec   executor.Executor
	llmExec    executor.Executor
	numWorkers int
}

func NewWorkerPool(s store.Store, router *Router, classifier *IntentClassifier, codeExec, llmExec executor.Executor, numWorkers int) *WorkerPool {
	return &WorkerPool{
		store:      s,
		router:     router,
		classifier: classifier,
		codeExec:   codeExec,
		llmExec:    llmExec,
		numWorkers: numWorkers,
	}
}

func (wp *WorkerPool) Start(ctx context.Context) {
	for i := range wp.numWorkers {
		workerID := uuid.NewString()[:8]
		slog.Info("starting worker", "worker_id", workerID, "index", i)
		go wp.runWorker(ctx, workerID)
	}
}

func (wp *WorkerPool) runWorker(ctx context.Context, workerID string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			task, err := wp.store.ClaimNextTask(ctx, workerID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					time.Sleep(time.Second)
					continue
				}
				slog.Error("claiming task", "worker_id", workerID, "error", err)
				time.Sleep(5 * time.Second)
				continue
			}

			slog.Info("processing task", "worker_id", workerID, "task_id", task.ID, "thread_id", task.ThreadID)
			wp.processTask(ctx, workerID, task)
		}
	}
}

func (wp *WorkerPool) processTask(ctx context.Context, workerID string, task *domain.Task) {
	thread, err := wp.store.GetThread(ctx, task.ThreadID)
	if err != nil {
		wp.failTask(ctx, task, err)
		return
	}

	messages, err := wp.store.GetMessages(ctx, task.ThreadID)
	if err != nil {
		wp.failTask(ctx, task, err)
		return
	}

	event := domain.IncomingEvent{
		Channel:    thread.ChannelRef.Channel,
		ChannelRef: thread.ChannelRef,
		Content:    task.Input,
	}

	intent, err := wp.classifier.Classify(ctx, event, messages)
	if err != nil {
		slog.Warn("intent classification failed, defaulting to discussion", "error", err)
		intent = domain.IntentDiscussion
	}

	task.Intent = intent
	wp.store.UpdateTask(ctx, task)

	slog.Info("classified intent", "task_id", task.ID, "intent", intent)

	var exec executor.Executor
	switch intent {
	case domain.IntentCodeTask, domain.IntentReview:
		exec = wp.codeExec
	default:
		exec = wp.llmExec
	}

	execCtx := &executor.Context{
		Thread:   thread,
		Messages: messages,
		Task:     task,
		Event:    &event,
	}

	result, err := exec.Execute(ctx, execCtx)
	if err != nil {
		wp.failTask(ctx, task, err)
		wp.sendResponse(ctx, thread, fmt.Sprintf("Sorry, I encountered an error: %v", err))
		return
	}

	task.Status = domain.TaskCompleted
	task.Result = result.Response
	wp.store.UpdateTask(ctx, task)

	wp.store.CreateMessage(ctx, &domain.Message{
		ThreadID: thread.ID,
		Role:     domain.RoleAssistant,
		Content:  result.Response,
	})

	wp.sendResponse(ctx, thread, result.Response)
}

func (wp *WorkerPool) failTask(ctx context.Context, task *domain.Task, err error) {
	slog.Error("task failed", "task_id", task.ID, "error", err)
	task.Status = domain.TaskFailed
	task.Result = err.Error()
	wp.store.UpdateTask(ctx, task)
}

func (wp *WorkerPool) sendResponse(ctx context.Context, thread *domain.Thread, message string) {
	a := wp.router.GetAdapter(thread.ChannelRef.Channel)
	if a == nil {
		slog.Error("no adapter for channel", "channel", thread.ChannelRef.Channel)
		return
	}
	if err := a.SendResponse(ctx, thread.ChannelRef, message); err != nil {
		slog.Error("sending response", "error", err)
	}
}
```

**Step 4: Add missing import and verify**

The worker.go uses `fmt` — add it to the import block. Then run:
```bash
go build ./internal/engine/
```

**Step 5: Commit**

```bash
git add internal/engine/
git commit -m "feat: engine with event router, intent classifier, and worker pool"
```

---

### Task 11: Wire Everything in main.go

**Files:**
- Modify: `cmd/ai-coworker/main.go`

**Step 1: Rewrite main.go to wire all components**

Replace `cmd/ai-coworker/main.go` with:
```go
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cstabler/ai-coworker/internal/adapter/github"
	slackadapter "github.com/cstabler/ai-coworker/internal/adapter/slack"
	"github.com/cstabler/ai-coworker/internal/config"
	"github.com/cstabler/ai-coworker/internal/engine"
	"github.com/cstabler/ai-coworker/internal/executor/claudecode"
	"github.com/cstabler/ai-coworker/internal/executor/llmexec"
	"github.com/cstabler/ai-coworker/internal/llm/claude"
	"github.com/cstabler/ai-coworker/internal/sandbox/docker"
	"github.com/cstabler/ai-coworker/internal/store"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := store.NewPostgresStore(ctx, cfg.Database.URL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("database migrated")

	llmProvider := claude.New(cfg.LLM.APIKey, cfg.LLM.Model)

	router := engine.NewRouter(db)

	if cfg.Slack.Enabled {
		slackAdapter := slackadapter.New(cfg.Slack.AppToken, cfg.Slack.BotToken)
		router.RegisterAdapter(slackAdapter)
		go func() {
			if err := slackAdapter.Start(ctx, router.HandleEvent); err != nil {
				slog.Error("slack adapter failed", "error", err)
			}
		}()
		slog.Info("slack adapter enabled")
	}

	if cfg.GitHub.Enabled {
		ghAdapter := github.New(cfg.GitHub.WebhookSecret, cfg.GitHub.BotUsername)
		router.RegisterAdapter(ghAdapter)
		go func() {
			if err := ghAdapter.Start(ctx, router.HandleEvent); err != nil {
				slog.Error("github adapter failed", "error", err)
			}
		}()
		slog.Info("github adapter enabled")
	}

	sandboxRuntime, err := docker.New()
	if err != nil {
		slog.Error("failed to create sandbox runtime", "error", err)
		os.Exit(1)
	}

	codeExec := claudecode.New(sandboxRuntime, cfg.Sandbox.Image, map[string]string{
		"ANTHROPIC_API_KEY": cfg.LLM.APIKey,
	})
	llmExec := llmexec.New(llmProvider)

	classifier := engine.NewIntentClassifier(llmProvider)
	pool := engine.NewWorkerPool(db, router, classifier, codeExec, llmExec, cfg.Workers)
	pool.Start(ctx)

	slog.Info("ai-coworker started", "workers", cfg.Workers)
	<-ctx.Done()
	slog.Info("shutting down")
}
```

**Step 2: Verify full compilation**

Run:
```bash
go build ./cmd/ai-coworker/
```

**Step 3: Commit**

```bash
git add cmd/ai-coworker/main.go
git commit -m "feat: wire all components in main entrypoint"
```

---

### Task 12: Dockerfile & Build

**Files:**
- Create: `Dockerfile`
- Create: `Makefile`

**Step 1: Create multi-stage Dockerfile**

Create `Dockerfile`:
```dockerfile
FROM golang:1.22-bookworm AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o ai-coworker ./cmd/ai-coworker/

FROM gcr.io/distroless/static-debian12
COPY --from=builder /build/ai-coworker /ai-coworker
COPY --from=builder /build/config.yaml /config.yaml
ENTRYPOINT ["/ai-coworker"]
```

**Step 2: Create Makefile**

Create `Makefile`:
```makefile
.PHONY: build run test docker sandbox-image

build:
	go build -o ai-coworker ./cmd/ai-coworker/

run: build
	./ai-coworker

test:
	go test ./...

docker:
	docker build -t ai-coworker:latest .

sandbox-image:
	docker build -t ai-coworker-sandbox:latest -f sandbox/Dockerfile sandbox/

dev-db:
	docker compose up -d postgres

lint:
	go vet ./...
```

**Step 3: Verify docker build**

Run:
```bash
go build ./cmd/ai-coworker/
```

**Step 4: Commit**

```bash
git add Dockerfile Makefile
git commit -m "feat: Dockerfile and Makefile for build and dev workflow"
```

---

### Task 13: Integration Test Setup

**Files:**
- Create: `internal/store/postgres_test.go`
- Create: `internal/engine/router_test.go`

**Step 1: Write store integration test**

Create `internal/store/postgres_test.go`:
```go
package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/cstabler/ai-coworker/internal/domain"
	"github.com/cstabler/ai-coworker/internal/store"
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

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{
			Channel:   "slack",
			ChannelID: "C123",
			ThreadTS:  "1234567890.123456",
		},
		Status: domain.ThreadActive,
	}

	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if thread.ID == "" {
		t.Fatal("expected thread ID to be set")
	}

	got, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if got.Status != domain.ThreadActive {
		t.Fatalf("expected active, got %s", got.Status)
	}

	found, err := s.GetThreadByChannelRef(ctx, "slack", "C123", "1234567890.123456")
	if err != nil {
		t.Fatalf("get by channel ref: %v", err)
	}
	if found.ID != thread.ID {
		t.Fatalf("expected %s, got %s", thread.ID, found.ID)
	}
}

func TestTaskClaimAndUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	thread := &domain.Thread{
		ChannelRef: domain.ChannelRef{Channel: "test", ChannelID: "T1", ThreadTS: "claim-test"},
		Status:     domain.ThreadActive,
	}
	s.CreateThread(ctx, thread)

	task := &domain.Task{
		ThreadID: thread.ID,
		Status:   domain.TaskPending,
		Input:    "fix the bug",
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	claimed, err := s.ClaimNextTask(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if claimed.Input != "fix the bug" {
		t.Fatalf("expected 'fix the bug', got '%s'", claimed.Input)
	}
	if claimed.Status != domain.TaskInProgress {
		t.Fatalf("expected in_progress, got %s", claimed.Status)
	}

	claimed.Status = domain.TaskCompleted
	claimed.Result = "done"
	if err := s.UpdateTask(ctx, claimed); err != nil {
		t.Fatalf("update task: %v", err)
	}
}
```

**Step 2: Verify test compilation**

Run:
```bash
go test ./internal/store/ -run "^$" -count=0
```

**Step 3: Commit**

```bash
git add internal/store/postgres_test.go
git commit -m "test: add PostgreSQL store integration tests"
```

---

## Execution Order & Dependencies

```
Task 1: Scaffolding
  └── Task 2: Domain Types
        └── Task 3: PostgreSQL Store
              ├── Task 4: LLM Provider
              ├── Task 5: Adapter Interface
              │     ├── Task 6: Slack Adapter
              │     └── Task 7: GitHub Adapter
              └── Task 8: Sandbox
                    └── Task 9: Executors
                          └── Task 10: Engine
                                └── Task 11: Wire main.go
                                      └── Task 12: Dockerfile
                                            └── Task 13: Tests
```

Tasks 4-8 can run in parallel once Task 3 is done.