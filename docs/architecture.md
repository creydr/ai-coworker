# Architecture

AI Coworker is an event-driven Go service with a thread-based conversation model and a persistent task queue. Pluggable channel adapters normalize incoming events into a common format (currently GitHub and Slack, extensible via the `adapter.Adapter` interface), a worker pool processes them using LLM-based intent classification, and executors (Claude Code in sandboxed containers or direct LLM calls) handle the actual work.

## Event Flow

```
Channel (Slack, GitHub, ...)
      |
  Channel Adapters ── normalize events into IncomingEvent
      |
  Event Router ── match to threads, enqueue tasks
      |
  Worker Pool ── claim tasks (FOR UPDATE SKIP LOCKED)
      |
  Intent Classifier ── code_task | question | discussion | review
      |
  +-----------------+     +--------------+
  | Claude Code     |     | LLM Direct   |
  | (sandbox)       |     | (Q&A)        |
  +-----------------+     +--------------+
      |
  Response ── sent back through the originating adapter
```

1. A channel adapter receives an event (webhook, Socket Mode message).
2. It normalizes the event into a common `IncomingEvent` and publishes it to the router.
3. The router matches the event to an existing thread via `(channel, thread_key)` or creates a new one.
4. A message is stored and a task is enqueued with status `pending`.
5. A worker claims the task, loads the full conversation history, and classifies the intent.
6. The worker delegates to the appropriate executor.
7. The result is stored as an assistant message and sent back through the originating adapter.
8. The thread stays active, waiting for follow-up events.

## Components

### Channel Adapters

Each adapter implements the `adapter.Adapter` interface (`internal/adapter/adapter.go`):

```go
type Adapter interface {
    Start(ctx context.Context, handler EventHandler) error
    SendResponse(ctx context.Context, ref domain.ChannelRef, message string) error
    Acknowledge(ctx context.Context, ref domain.ChannelRef) error
    Name() string
}
```

**GitHub Adapter** (`internal/adapter/github/`) — Listens for GitHub App webhook events on `POST /webhook/github` (port 8080). Handles three event types: issue comments, PR review comments, and PR reviews. Validates webhook signatures, authenticates via GitHub App installation tokens, and caches clients per installation ID. Reacts with :eyes: to acknowledge receipt.

**Slack Adapter** (`internal/adapter/slack/`) — Connects via Slack Socket Mode (outbound WebSocket, no public URL needed). Triggers on `app_mention` events. Responds in-thread and reacts with :eyes: to acknowledge.

Both adapters use typed helpers (`ref.go`) to construct and parse `ChannelRef` values, keeping adapter-specific routing data in the opaque `Properties` map while the domain layer stays adapter-agnostic.

### Event Router

`internal/engine/router.go`

The router is the entry point for all incoming events. It:

1. Acknowledges the event via the originating adapter (e.g. emoji reaction).
2. Looks up an existing thread by `(channel, thread_key)` or creates a new one.
3. Stores the user message.
4. Enqueues a `pending` task with the event metadata.

### Worker Pool

`internal/engine/worker.go`

The worker pool runs configurable goroutines (default: 4) that continuously claim pending tasks from PostgreSQL using `SELECT ... FOR UPDATE SKIP LOCKED` for distributed locking. Each worker:

1. Claims a task (atomically transitions `pending` → `in_progress`).
2. Loads the thread and full message history.
3. Classifies the intent via the `IntentClassifier`.
4. Routes to the code executor or LLM executor.
5. Stores the result and sends the response.

### Intent Classifier

`internal/engine/intent.go`

Uses the LLM to categorize each task into one of:

- **code_task** — code changes, bug fixes, feature implementation
- **question** — answerable without writing code
- **discussion** — planning, brainstorming
- **review** — PR review feedback that needs to be addressed

Short-circuits for review events: if the metadata contains `review_state` or `type=review_comment`, the classifier returns `review` without calling the LLM.

### Executors

**Claude Code Executor** (`internal/executor/claudecode/`) — For code tasks and reviews. Spawns an ephemeral container (Docker or Kubernetes Job, depending on the configured sandbox runtime) with:

- The target repo cloned (with optional branch checkout for PR work)
- Claude Code CLI running with `--dangerously-skip-permissions`
- GitHub token for authenticated git operations
- LLM provider credentials (Anthropic API key or Vertex AI ADC)
- Resource limits (CPU, memory, timeout)

The container is disposed after execution.

**LLM Executor** (`internal/executor/llmexec/`) — For questions and discussions. Calls the LLM provider directly with the full conversation history.

### LLM Providers

`internal/llm/provider.go`

```go
type Provider interface {
    Chat(ctx context.Context, messages []Message) (string, error)
}
```

Three implementations:

- **Claude** (`internal/llm/claude/`) — Anthropic API with API key auth
- **Vertex AI** (`internal/llm/vertex/`) — Claude on Google Cloud with Application Default Credentials
- **OpenAI-compatible** (`internal/llm/openai/`) — Any service exposing the OpenAI chat completions endpoint

### Sandbox

`internal/sandbox/sandbox.go`

```go
type Runtime interface {
    Exec(ctx context.Context, req ExecRequest) (*ExecResult, error)
}
```

Two runtime implementations:

- **Docker** (`internal/sandbox/docker/`) — Creates ephemeral containers locally. Default for development.
- **Kubernetes** (`internal/sandbox/kubernetes/`) — Creates Kubernetes Jobs with the prompt delivered via a ConfigMap volume. Used when the service runs on a cluster.

Both runtimes use the same sandbox image (`sandbox/Dockerfile`), based on `node:22-bookworm` with `git`, `gh` (GitHub CLI), and `@anthropic-ai/claude-code` installed. The entrypoint script (`sandbox/entrypoint.sh`) handles git credential setup, Google Cloud ADC setup, repo cloning, and Claude Code execution.

### Store

`internal/store/postgres.go`

PostgreSQL-backed persistence with three core tables:

- **threads** — maps `(channel, thread_key)` pairs to a conversation. Adapter-specific routing data is stored in a JSONB `properties` column.
- **messages** — ordered messages within a thread (user and assistant), with timestamps.
- **tasks** — work queue with status (`pending`, `in_progress`, `completed`, `failed`), worker assignment, JSONB metadata, timestamps, and results.

Migrations are embedded as SQL files (`internal/store/migrations/`) and applied automatically on startup.

## Thread Model

There is no distinction between "new task" and "follow-up." Every incoming event is matched to a thread by its `(channel, thread_key)` pair. If the thread exists, the event is a continuation with full history. If not, a new thread is created.

```
incoming event -> load thread -> classify intent -> execute -> respond -> wait
       ^                                                                   |
       +-------------------------------------------------------------------+
```

Each adapter constructs a unique `ThreadKey` from its own identifiers:

- GitHub: `"org/repo#42"` (repo + issue/PR number)
- Slack: `"C123/1234.5678"` (channel ID + thread timestamp)

## Domain Types

The core types live in `internal/domain/`:

**ChannelRef** — adapter-agnostic reference to a conversation location. `Channel` identifies the adapter, `ThreadKey` is a unique identifier within that channel, and `Properties` is an opaque `map[string]string` for adapter-specific routing data.

**IncomingEvent** — normalized event from any adapter, containing the channel ref, user content, and optional metadata.

**Thread** — a conversation with a status (active, resolved, expired).

**Task** — a unit of work with a status lifecycle: `pending` -> `in_progress` -> `completed` / `failed`.

**Message** — a single message in a thread, either from the user or assistant.

## Project Structure

```
cmd/ai-coworker/              Entry point, wires all components
internal/
  adapter/                    Channel adapter interface
    github/                   GitHub App webhook adapter
      github.go               Webhook handling, event parsing
      ref.go                  Typed NewRef/ParseRef helpers
    slack/                    Slack Socket Mode adapter
      slack.go                Socket Mode connection, event handling
      ref.go                  Typed NewRef/ParseRef helpers
  config/                     Configuration loading (koanf, YAML + env vars)
  domain/                     Core types (Event, Thread, Task, Message, ChannelRef)
  engine/                     Core orchestration
    router.go                 Event -> thread matching -> task dispatch
    worker.go                 Worker pool, task claiming, response routing
    intent.go                 LLM-based intent classification
  executor/                   Task execution
    executor.go               Executor interface
    claudecode/               Sandbox executor (Docker + Claude Code CLI)
    llmexec/                  Direct LLM executor (Q&A, discussions)
  llm/                        LLM provider interface
    provider.go               Provider interface definition
    claude/                   Anthropic Claude API
    vertex/                   Vertex AI (Claude on Google Cloud)
    openai/                   OpenAI-compatible API
  sandbox/                    Sandbox runtime interface
    sandbox.go                Runtime interface definition
    docker/                   Docker container runtime
  store/                      Data store interface + PostgreSQL implementation
    store.go                  Store interface
    postgres.go               PostgreSQL implementation
    migrations/               Embedded SQL migration files
sandbox/                      Sandbox container image
  Dockerfile                  Node 22 + git + gh + Claude Code CLI
  entrypoint.sh               Git creds, ADC, repo clone, Claude Code exec
```
