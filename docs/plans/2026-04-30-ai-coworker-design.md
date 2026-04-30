# AI Coworker — Design Document

## Problem

We want an AI agent that behaves like a real coworker: it can be reached on Slack and GitHub, it participates in discussions, and it executes tasks end-to-end (e.g. reads a GitHub issue, writes code, creates a PR, responds to review feedback). It should work autonomously once given direction.

## Goals

- Multi-channel interaction: GitHub Issues/PRs and Slack in v1, extensible to Email and others.
- End-to-end task execution: from understanding the request to delivering the result.
- Feedback loops: supports back-and-forth conversation, responds to PR reviews, follows up.
- Parallel workers: multiple conversations and tasks can run simultaneously.
- Model-agnostic: Claude as the primary LLM, but behind an interface so providers can be swapped.
- Portable: ships as a Go binary and container image, runs on K8s, Docker, or bare metal.

## Architecture

Event-driven with a thread-based conversation model and a persistent task queue.

```
[Channels]          [Core]                [Executors]

Slack Adapter ──┐                      ┌── Claude Code (code tasks, sandboxed container)
                ├── Event Router ────► │
GitHub Adapter ─┤   Task Queue         ├── LLM Agent (non-code tasks)
                ├── Worker Pool        │
(future: Email)─┘   State Store ───────┘── (future: custom executors)
                     (PostgreSQL)
```

### Flow

1. A channel adapter receives an event (mention, message, webhook).
2. It normalizes the event into a common `IncomingEvent` struct and publishes it.
3. The event router matches the event to an existing thread or creates a new one.
4. A worker picks up the task, loads full conversation history from PostgreSQL, and sends it to the LLM to determine intent.
5. The worker delegates to the appropriate executor — Claude Code for code tasks, direct LLM calls for reasoning/communication.
6. Results are sent back through the originating channel adapter.
7. The thread stays active, waiting for follow-up events (feedback loop).

### Thread-Based Conversation Model

There is no distinction between "new task" and "follow-up." Every incoming event is matched to a thread by channel-specific identifiers (Slack thread timestamp, GitHub issue number). If the thread exists, the event is a continuation with full history. If not, a new thread is created.

A thread stays active until explicitly resolved or it times out after a configurable idle period.

```
incoming event → load thread → reason about intent → execute → respond → wait
       ▲                                                                   │
       └───────────────────────────────────────────────────────────────────┘
```

### Feedback Loop Examples

**Slack:** User asks a question → AI responds in-thread → user replies → AI sees full thread history and responds again. Continues until the thread goes idle.

**GitHub PR review:** AI creates a PR → reviewer posts comments → GitHub webhook fires → AI loads thread, sees review feedback, runs Claude Code in a new container to address comments, pushes changes, and posts a summary comment. If the reviewer responds again, the loop continues.

## Components

### Channel Adapters

Each adapter implements a common interface for receiving events and sending responses. Adapters are responsible for:

- Listening for events (webhooks, Socket Mode, polling)
- Normalizing events into `IncomingEvent` structs
- Sending responses back to the originating channel
- Acknowledging receipt (e.g. emoji reaction on Slack)

**GitHub Adapter:** Listens via GitHub App webhook events. Triggers on `@ai-coworker` mentions in issues and PR comments. Responds by posting comments. For code tasks, provides repo clone URL and metadata (issue number, branch, etc.).

**Slack Adapter:** Connects via Slack Socket Mode (no public URL needed). Triggers on app mentions and DMs. Maintains thread awareness via Slack thread timestamps. Responds in-thread.

### Event Router & Workers

The router matches incoming events to threads and dispatches them to workers. Workers are stateless goroutines pulling from a PostgreSQL-backed task queue using `SELECT ... FOR UPDATE SKIP LOCKED` for distributed locking.

Each worker:

1. Loads full conversation history for the thread.
2. Sends conversation + event to the LLM to classify intent.
3. Routes to the appropriate executor.
4. Stores the response in the thread history.
5. Sends the response via the channel adapter.

### Executors

**Claude Code Executor:** For code tasks (bug fixes, feature implementation, code review). Spawns an ephemeral container with:

- The target repo cloned
- Claude Code installed, running with `--dangerously-skip-permissions`
- Network access scoped to necessary APIs (GitHub, package registries)
- Resource limits (CPU, memory, timeout)
- Automatic cleanup on task completion

The container is disposable — isolation ensures a rogue output cannot affect the host or other tasks.

**LLM Agent Executor:** For non-code tasks (answering questions, summarization, planning, discussion). Calls the LLM API directly through the provider interface, feeding full conversation history for context.

### LLM Provider Abstraction

A simple interface that wraps LLM API calls. Claude implementation ships first; other providers (Gemini, etc.) can be added by implementing the same interface.

```go
type LLMProvider interface {
    Chat(ctx context.Context, messages []Message, opts ...Option) (string, error)
}
```

### Sandbox Runtime

An interface for container lifecycle management. Two implementations:

- **Docker:** For local development and single-server deployment.
- **Kubernetes:** Creates Jobs/Pods for execution on K8s clusters.

### State Store (PostgreSQL)

Three core tables:

- **threads** — maps channel thread identifiers to a conversation ID.
- **messages** — ordered messages within a thread (user and AI), with timestamps.
- **tasks** — work queue with status (`pending`, `in_progress`, `completed`, `failed`), worker assignment, timestamps, and results.

## Project Structure

```
ai-coworker/
├── cmd/
│   └── ai-coworker/          # main entrypoint
├── internal/
│   ├── adapter/               # channel adapters
│   │   ├── adapter.go         # ChannelAdapter interface
│   │   ├── github/            # GitHub App webhooks + API
│   │   └── slack/             # Slack Socket Mode + API
│   ├── engine/                # core orchestration
│   │   ├── router.go          # event → thread matching → worker dispatch
│   │   ├── worker.go          # pulls tasks, reasons about intent, delegates
│   │   └── thread.go          # thread lifecycle management
│   ├── executor/              # task execution
│   │   ├── executor.go        # Executor interface
│   │   ├── claudecode/        # spawns container, runs Claude Code CLI
│   │   └── llm/               # direct LLM calls for non-code tasks
│   ├── llm/                   # LLM provider abstraction
│   │   ├── provider.go        # LLMProvider interface
│   │   └── claude/            # Claude/Anthropic implementation
│   ├── sandbox/               # container management
│   │   ├── sandbox.go         # Sandbox interface
│   │   ├── docker/            # Docker implementation
│   │   └── kubernetes/        # K8s Job/Pod implementation
│   └── store/                 # PostgreSQL persistence
│       ├── store.go           # data access interface
│       ├── threads.go
│       ├── messages.go
│       └── tasks.go
├── config.yaml                # runtime configuration
├── Dockerfile
├── go.mod
└── go.sum
```

## Configuration

Runtime configuration via `config.yaml`:

- LLM provider settings (API keys, model selection)
- Channel adapter settings (Slack tokens, GitHub App credentials)
- Sandbox settings (Docker socket path, K8s namespace, resource limits, timeouts)
- Worker pool size
- Database connection string
- Thread idle timeout

## Non-Goals for v1

- Email channel adapter
- Multi-model routing (using different models for different task types)
- Web UI / dashboard
- User access control / permissions beyond channel-level auth