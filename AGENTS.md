# AGENTS.md

## Always Keep in Mind

Act like a professional software developer and engineer. Adhere to architecture, naming conventions and coding standards in this codebase. If unsure, read similar files and get inspiration from the rest of the codebase. If introducing new features, make sure to cover them via unit tests and don't forget to take edge cases into account.

## Project Overview

Autonomous AI agent that executes software development tasks end-to-end. Written in Go, event-driven with a thread-based conversation model and persistent task queue backed by PostgreSQL. Channel adapters are pluggable — currently ships with GitHub (App webhooks) and Slack (Socket Mode), but the `adapter.Adapter` interface supports adding new channels.

Flow: adapter → router → worker pool → intent classifier → executor → response via adapter.

See [docs/architecture.md](docs/architecture.md) for the full architecture.

## Testing Strategy

Before committing, test locally following the table below:

| If changed | Target | Description |
|-----------|--------|-------------|
| `*.go` files | `make test` | Unit tests |
| Any files | `make lint` | Linting |
| `store/migrations/` | Integration tests | `TEST_DATABASE_URL=... go test -tags integration ./...` |
| Significant changes | Manual e2e | Run locally with GitHub adapter + smee |

## Project Structure

```
cmd/ai-coworker/        Entry point, wires all components
internal/
  adapter/              Adapter interface + EventHandler type
    github/             GitHub App webhooks, ChannelRef helpers (ref.go)
    slack/              Slack Socket Mode, thread context, ChannelRef helpers (ref.go)
  config/               Koanf-based config (YAML + env vars)
  domain/               Core types: IncomingEvent, Thread, Task, Message
  engine/               Router, WorkerPool, IntentClassifier
  executor/
    claudecode/         Sandbox executor (Docker/K8s + Claude Code CLI)
    llmexec/            Direct LLM executor
  llm/                  Provider interface
    anthropic/          Claude API + Vertex AI
    openai/             OpenAI-compatible API
  sandbox/              Runtime interface
    docker/             Docker container runtime
    kubernetes/         Kubernetes Job runtime
  store/                Store interface + PostgreSQL (migrations/)
  vcs/                  VCS provider abstraction (token creation, clone URLs)
    github/             GitHub VCS provider
sandbox/                Dockerfile + entrypoint for sandbox image
```

## Key Patterns

- **Thread-based conversations:** Events are matched to threads via `(channel, thread_key)`. All messages in a thread are loaded as conversation history for the LLM.
- **Task claiming:** Workers use `pg_try_advisory_xact_lock` + `FOR UPDATE` to atomically claim pending tasks from PostgreSQL.
- **Review task batching:** When a worker claims a review task, it absorbs sibling pending review tasks from the same thread (500ms debounce), merges them into a single prompt, and parses per-comment responses from the output.
- **Intent classification:** Five intents — `code_task`, `review`, `info_lookup`, `question`, `discussion`. Code tasks, reviews, and info lookups route to the sandbox executor; questions and discussions route to the LLM executor.
- **VCS providers:** The `vcs.Provider` interface decouples repo operations from specific platforms. The `vcs.Registry` resolves URLs to providers and creates scoped tokens. The Claude Code executor uses this to authenticate across multiple VCS platforms in a single sandbox session.
- **ChannelRef:** Each adapter has typed `NewRef()`/`ParseRef()` helpers in `ref.go` to serialize adapter-specific state into the generic `domain.ChannelRef`.
- **Batch event processing:** The `EventHandler` accepts `[]IncomingEvent`. The GitHub adapter batches a PR review body + inline comments into a single call.
- **Configuration:** Koanf loads `config.yaml` first, then overrides with env vars prefixed `AI_COWORKER__` (double underscore = nesting, single underscore preserved in field names).
- **LLM providers:** The `llm.Provider` interface abstracts Claude, Vertex AI, and OpenAI-compatible backends. Executors interact with providers, never with SDK clients directly.

## Code Conventions

- Format Go files with `gofmt` and organize imports with `goimports` (local prefix: `github.com/creydr/ai-coworker`).
- Only add comments when the *why* is non-obvious. Don't explain what the code does.

## Boundaries

### Always Do

- Run `make test` before considering any change complete
- Run `make lint` before commits
- Read `CONTRIBUTING.md` for development setup and workflow

### Ask First

- Security-related code changes (authentication, credentials, secrets handling)
- API or domain type changes (`internal/domain/`)
- Adding new dependencies
- Modifying CI/GitHub Actions workflows
- Changes to the sandbox entrypoint or Dockerfile

### Never Do

- Commit secrets, API keys, or credentials
- Delete files without explicit user approval
- Force push to main/master branch
- Skip tests or linting
- Post or comment on GitHub/Slack without user confirmation

## Important Documentation

Read these files to understand the project setup, conventions, and development workflow:

- [README.md](README.md) — setup, configuration, LLM providers, GitHub/Slack app setup
- [CONTRIBUTING.md](CONTRIBUTING.md) — local development, running with adapters, smee/ngrok setup
- [docs/architecture.md](docs/architecture.md) — component details, event flow, domain types

After implementing a feature or making significant changes, check whether these docs need updating.
