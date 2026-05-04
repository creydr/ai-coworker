# AI Coworker

An autonomous AI agent that integrates with Slack and GitHub to execute software development tasks end-to-end. Mention it on a GitHub issue and it will discuss the problem, write code, and open a pull request. Tag it in Slack and it will answer questions or kick off tasks.

## Architecture

```
Slack / GitHub
      |
  Channel Adapters ── normalize events
      |
  Event Router ── match to threads, create tasks
      |
  Worker Pool ── claim tasks (FOR UPDATE SKIP LOCKED)
      |
  Intent Classifier ── code_task | question | discussion | review
      |
  ┌───────────────┐     ┌────────────┐
  │ Claude Code   │     │ LLM Direct │
  │ (sandbox)     │     │ (Q&A)      │
  └───────────────┘     └────────────┘
```

- **Channel adapters** receive events from Slack (Socket Mode) and GitHub (App webhooks) and normalize them into a common `IncomingEvent` format.
- **Event router** maps events to conversation threads stored in PostgreSQL and enqueues tasks.
- **Worker pool** runs configurable goroutines that claim pending tasks from the database.
- **Intent classifier** uses the LLM to categorize each task as a code task, question, discussion, or review.
- **Code tasks** run inside ephemeral Docker containers with Claude Code CLI (`--dangerously-skip-permissions`), with full git/GitHub access.
- **Non-code tasks** (questions, discussions) go directly to the LLM for a conversational response.

## Prerequisites

- Go 1.22+
- Docker
- PostgreSQL 16
- An [Anthropic API key](https://console.anthropic.com/)

## Quick Start

### 1. Start the database

```sh
make dev-db
```

This runs PostgreSQL 16 via Docker Compose. The schema is applied automatically on first startup.

### 2. Sandbox image

Pre-built multi-arch images are published to `quay.io/creydr/ai-coworker-sandbox` on every push to main. The default config already references this image.

To build locally instead:

```sh
make sandbox-image
```

### 3. Configure

All configuration can be provided via `config.yaml` or environment variables with the `AI_COWORKER__` prefix (double underscore). Use `__` to separate nested sections, single `_` within field names is preserved. Environment variables override the config file.

The config file (`config.yaml`) ships with safe defaults for local development:

```yaml
database:
  url: "postgres://ai_coworker:ai_coworker@localhost:5432/ai_coworker?sslmode=disable"

llm:
  provider: "claude"
  model: "claude-sonnet-4-20250514"

slack:
  enabled: false

github:
  enabled: false

sandbox:
  runtime: "docker"
  image: "quay.io/creydr/ai-coworker-sandbox:latest"
  timeout_seconds: 600
  cpu_limit: "2"
  memory_limit: "2Gi"

workers: 4
```

Secrets and deployment-specific values must be set via environment variables:

| Variable | Description |
|---|---|
| `AI_COWORKER__LLM__API_KEY` | Anthropic API key |
| `AI_COWORKER__GITHUB__ENABLED` | Set to `true` to enable the GitHub adapter |
| `AI_COWORKER__GITHUB__APP_ID` | GitHub App ID |
| `AI_COWORKER__GITHUB__PRIVATE_KEY` | GitHub App private key (PEM contents) |
| `AI_COWORKER__GITHUB__WEBHOOK_SECRET` | GitHub webhook secret |
| `AI_COWORKER__GITHUB__BOT_USERNAME` | GitHub App bot username (e.g. `creydr-ai[bot]`) |
| `AI_COWORKER__SLACK__ENABLED` | Set to `true` to enable the Slack adapter |
| `AI_COWORKER__SLACK__APP_TOKEN` | Slack app-level token (`xapp-...`) |
| `AI_COWORKER__SLACK__BOT_TOKEN` | Slack bot token (`xoxb-...`) |
| `AI_COWORKER__LLM__VERTEX__PROJECT_ID` | Google Cloud project ID (Vertex AI provider) |
| `AI_COWORKER__LLM__VERTEX__REGION` | Vertex AI region (defaults to `global`) |
| `AI_COWORKER__LLM__OPENAI__BASE_URL` | Base URL for OpenAI-compatible API |
| `AI_COWORKER__DATABASE__URL` | PostgreSQL connection string (overrides config file) |

#### LLM Providers

The `AI_COWORKER__LLM__PROVIDER` variable selects which LLM backend to use:

**Claude API (default):**

```sh
export AI_COWORKER__LLM__PROVIDER=claude
export AI_COWORKER__LLM__API_KEY=sk-ant-...
export AI_COWORKER__LLM__MODEL=claude-sonnet-4-20250514
```

**Vertex AI (Claude on Google Cloud):**

Uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials). Run `gcloud auth application-default login` first.

```sh
export AI_COWORKER__LLM__PROVIDER=vertex
export AI_COWORKER__LLM__VERTEX__PROJECT_ID=my-gcp-project
export AI_COWORKER__LLM__VERTEX__REGION=global  # optional, defaults to "global"
export AI_COWORKER__LLM__MODEL=claude-sonnet-4-20250514
```

**OpenAI-compatible API (Red Hat MaaS, vLLM, etc.):**

Works with any service that exposes an OpenAI-compatible chat completions endpoint.

```sh
export AI_COWORKER__LLM__PROVIDER=openai
export AI_COWORKER__LLM__OPENAI__BASE_URL=https://my-maas-endpoint.example.com/v1
export AI_COWORKER__LLM__API_KEY=...  # if required by the endpoint
export AI_COWORKER__LLM__MODEL=granite-3.3-8b
```

### 4. Run

```sh
make run
```

Or with environment variables inline:

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... make run
```

## GitHub App Setup

1. Go to **Settings > Developer settings > GitHub Apps > New GitHub App**.
2. Fill in the basics:
   - **Name:** your app name (e.g. `my-ai-coworker`)
   - **Homepage URL:** any URL
   - **Webhook URL:** your server's public URL + `/webhook/github` (can be configured later)
   - **Webhook secret:** generate a random secret
3. Set permissions:
   - **Repository permissions:**
     - Contents: Read & write
     - Issues: Read & write
     - Pull requests: Read & write
     - Metadata: Read-only
4. Subscribe to events:
   - Issue comment
   - Pull request review
   - Pull request review comment
5. Create the app, then generate a private key from the app settings page.
6. Install the app on the repositories you want it to monitor.
7. Set the environment variables:
   ```sh
   export AI_COWORKER__GITHUB__ENABLED=true
   export AI_COWORKER__GITHUB__APP_ID=<your-app-id>
   export AI_COWORKER__GITHUB__PRIVATE_KEY="$(cat path/to/private-key.pem)"
   export AI_COWORKER__GITHUB__WEBHOOK_SECRET=<your-webhook-secret>
   export AI_COWORKER__GITHUB__BOT_USERNAME=<your-app-name>[bot]
   ```

The webhook server listens on port 8080. For local development, use a tool like [smee.io](https://smee.io) or [ngrok](https://ngrok.com) to expose it.

## Slack App Setup

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and create a new app.
2. Enable **Socket Mode** (Settings > Socket Mode) and generate an app-level token with the `connections:write` scope.
3. Under **Event Subscriptions**, enable events and subscribe to:
   - `app_mention`
4. Under **OAuth & Permissions**, add the bot token scopes:
   - `app_mentions:read`
   - `chat:write`
   - `reactions:write`
5. Install the app to your workspace and copy the bot token.
6. Set the environment variables:
   ```sh
   export AI_COWORKER__SLACK__ENABLED=true
   export AI_COWORKER__SLACK__APP_TOKEN=xapp-...
   export AI_COWORKER__SLACK__BOT_TOKEN=xoxb-...
   ```

## Usage

### GitHub

Mention the bot by name in any issue or pull request comment on an installed repository:

```
@creydr-ai Please fix the broken tests in the CI pipeline
```

The bot will:
1. React with :eyes: to acknowledge
2. Analyze the request and classify the intent
3. For code tasks: clone the repo in a sandbox, run Claude Code, and open a PR
4. For questions: respond directly in the thread

### Slack

Mention the bot in any channel it's been added to:

```
@AI Coworker What does the handleWebhook function do?
```

Responses are threaded automatically.

## Development

```sh
make build          # Build the binary
make test           # Run unit tests
make lint           # Run go vet
make dev-db         # Start PostgreSQL
make sandbox-image  # Build sandbox Docker image
make docker         # Build the service Docker image
```

### Running integration tests

Integration tests require a running PostgreSQL instance and use the `integration` build tag:

```sh
make dev-db
TEST_DATABASE_URL="postgres://ai-coworker:password@localhost:5432/ai-coworker?sslmode=disable" \
  go test -tags integration ./...
```

## Project Structure

```
cmd/ai-coworker/          Entry point
internal/
  adapter/                Channel adapter interface
    github/               GitHub App webhook adapter
    slack/                Slack Socket Mode adapter
  config/                 Configuration loading (koanf)
  domain/                 Core types (Event, Thread, Task, Message)
  engine/                 Router, worker pool, intent classifier
  executor/
    claudecode/           Sandbox executor (Docker + Claude Code)
    llmexec/              Direct LLM executor (Q&A)
  llm/                    LLM provider interface
    claude/               Anthropic Claude implementation
  sandbox/                Sandbox runtime interface
    docker/               Docker container runtime
  store/                  Data store interface + PostgreSQL implementation
sandbox/                  Dockerfile and entrypoint for sandbox image
```
