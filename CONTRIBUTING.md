# Contributing

## Architecture

```mermaid
flowchart TD
    A["Channel (Slack, GitHub, ...)"] --> B["Channel Adapters"]
    B -->|"[]IncomingEvent"| C["Event Router"]
    C --> D["Worker Pool"]
    D --> E{"Intent Classifier"}
    E -->|"code_task / review / info_lookup"| F["Claude Code\n(sandbox)"]
    E -->|"question / discussion"| G["LLM Direct\n(Q&A)"]
```

- **Channel adapters** receive events from external channels and normalize them into a common `IncomingEvent` format. The `adapter.Adapter` interface is pluggable — currently ships with GitHub (App webhooks), Slack (Socket Mode), and Google Docs (Drive push notifications + Comments API).
- **Event router** maps events to conversation threads stored in PostgreSQL and enqueues tasks. Processes events in batches.
- **Worker pool** runs configurable goroutines that claim pending tasks from the database. Review tasks are batched — sibling review comments from the same PR are absorbed and merged into a single sandbox execution.
- **Intent classifier** uses the LLM to categorize each task as a code task, review, info lookup, question, or discussion.
- **Code tasks, reviews, and info lookups** run inside ephemeral containers (Docker or Kubernetes Jobs) with Claude Code CLI (`--dangerously-skip-permissions`), with VCS provider tokens for authenticated git operations.
- **VCS providers** decouple repository operations from specific platforms. The `vcs.Provider` interface handles token creation, clone URLs, and credential setup — currently ships with GitHub, extensible to GitLab, Bitbucket, etc.
- **Non-code tasks** (questions, discussions) go directly to the LLM for a conversational response.

See [docs/architecture.md](docs/architecture.md) for a detailed overview of the system design, component responsibilities, and the thread-based conversation model.

## Project Structure

```
cmd/ai-coworker/          Entry point
internal/
  adapter/                Channel adapter interface
    github/               GitHub App webhook adapter (ref.go for typed helpers)
    googledocs/           Google Docs adapter (Drive push + Comments API)
    slack/                Slack Socket Mode adapter (ref.go for typed helpers)
  config/                 Configuration loading (koanf)
  domain/                 Core types (Event, Thread, Task, Message)
  engine/                 Router, worker pool, intent classifier
  executor/
    claudecode/           Sandbox executor (Docker/K8s + Claude Code)
    llmexec/              Direct LLM executor (Q&A)
  llm/                    LLM provider interface
    anthropic/            Anthropic Claude API (direct + Vertex AI)
    openai/               OpenAI-compatible API
  sandbox/                Sandbox runtime interface
    docker/               Docker container runtime
    kubernetes/           Kubernetes Job runtime
  store/                  Data store interface + PostgreSQL implementation
    migrations/           SQL migration files
  vcs/                    VCS provider abstraction
    github/               GitHub VCS provider (tokens, clone URLs)
sandbox/                  Dockerfile and entrypoint for sandbox image
deploy/kubernetes/        Kustomize manifests for Kubernetes deployment
  base/                   Base manifests (Deployment, Service, RBAC, config)
  components/postgres/    Kustomize component: PostgreSQL Deployment
  components/google-adc/  Kustomize component: Google ADC for Vertex AI
  overlays/with-postgres/ Overlay: base + postgres
  overlays/kind/          Overlay: base + postgres + google-adc (local testing)
hack/                     Developer scripts
  kind.sh                 KinD cluster lifecycle for local e2e testing
```

## Prerequisites

- Go 1.22+
- Docker (for PostgreSQL and sandbox containers)
- Node.js / npm (for `smee-client`, only needed for local GitHub webhook testing)

## Getting Started

```sh
git clone https://github.com/creydr/ai-coworker.git
cd ai-coworker
make dev-db    # start PostgreSQL 16 via Docker Compose
make build     # compile the binary
make test      # run unit tests
```

## Running Locally

### Minimal (database + LLM only)

```sh
make dev-db
AI_COWORKER__LLM__API_KEY=sk-ant-... make run
```

This starts the service with 4 workers but no channel adapters enabled. Useful for verifying the build and database connectivity.

### With GitHub Adapter

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... \
AI_COWORKER__GITHUB__ENABLED=true \
AI_COWORKER__GITHUB__APP_ID=<your-app-id> \
AI_COWORKER__GITHUB__PRIVATE_KEY="$(cat path/to/private-key.pem)" \
AI_COWORKER__GITHUB__WEBHOOK_SECRET="$(cat path/to/webhook-secret)" \
AI_COWORKER__GITHUB__BOT_USERNAME="your-bot" \
make run
```

The GitHub adapter starts an HTTP server on port **8080** listening at `POST /webhook/github`. You need a [GitHub App](#github-app-setup-for-development) and a [webhook proxy](#exposing-webhooks-for-local-testing) to receive events locally.

### With Slack Adapter

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... \
AI_COWORKER__SLACK__ENABLED=true \
AI_COWORKER__SLACK__APP_TOKEN=xapp-... \
AI_COWORKER__SLACK__BOT_TOKEN=xoxb-... \
make run
```

The Slack adapter uses Socket Mode (outbound WebSocket), so no public URL is needed.

### With Google Docs Adapter

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... \
AI_COWORKER__GOOGLEDOCS__ENABLED=true \
AI_COWORKER__GOOGLEDOCS__SERVICE_ACCOUNT_KEY_PATH=/path/to/key.json \
make run
```

The Google Docs adapter starts an HTTP server on port **8082** listening for Drive push notifications. You need a [Google service account](README.md#google-docs-setup) and a webhook proxy (e.g. smee.io or ngrok) to receive push notifications locally.

### LLM Providers

The service supports three LLM backends: Claude (direct API), Vertex AI (Claude on Google Cloud), and OpenAI-compatible APIs. See the [deployment guide](docs/deployment.md#llm-providers) for provider-specific configuration.

### Using Vertex AI

```sh
AI_COWORKER__LLM__PROVIDER=vertex \
AI_COWORKER__LLM__VERTEX__PROJECT_ID=my-gcp-project \
AI_COWORKER__LLM__MODEL=claude-sonnet-4-6 \
make run
```

Vertex AI uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials). Run `gcloud auth application-default login` first, which will write credentials to `~/.config/gcloud/application_default_credentials.json`..

## App Setup for Development

### GitHub

Follow the [GitHub App Setup](README.md#github-app-setup) instructions in the README. For local development, create a **separate** GitHub App (don't reuse your production app) and install it on a **test repository**. Use a placeholder webhook URL for now — you'll update it with your [smee.io URL](#exposing-webhooks-for-local-testing) in the next section.

### Slack

Follow the [Slack App Setup](README.md#slack-app-setup) instructions in the README. No public URL is needed — Socket Mode uses an outbound WebSocket connection.

## Exposing Webhooks for Local Testing

GitHub sends webhook events to a public URL, so your local `:8080` must be reachable from the internet. [smee.io](https://smee.io) is the recommended approach for development.

### Using smee.io

1. Install the smee client:

   ```sh
   npm install -g smee-client
   ```

2. Go to [smee.io](https://smee.io) and click **Start a new channel**. Copy the webhook proxy URL (e.g. `https://smee.io/AbCdEfGh`).

3. In your GitHub App settings, set the **Webhook URL** to your smee channel URL (`https://smee.io/AbCdEfGh`).

4. Start the proxy, forwarding to your local webhook endpoint:

   ```sh
   smee --url https://smee.io/AbCdEfGh --target http://localhost:8080/webhook/github
   ```

5. Start the service with the GitHub adapter enabled (see [above](#with-github-adapter)). Webhook events from GitHub will now be proxied to your local instance.

Keep the smee client running in a separate terminal while testing.

### Alternative: ngrok

You can also use [ngrok](https://ngrok.com) to expose your local port:

```sh
ngrok http 8080
```

Then set the ngrok forwarding URL (e.g. `https://abc123.ngrok-free.app/webhook/github`) as the webhook URL in your GitHub App settings.

## Testing

### Unit Tests

```sh
make test
```

### Integration Tests

Integration tests require a running PostgreSQL instance and use the `integration` build tag:

```sh
make dev-db
TEST_DATABASE_URL="postgres://ai-coworker:password@localhost:5432/ai-coworker?sslmode=disable" \
  go test -tags integration ./...
```

### System Tests

System tests verify the full request lifecycle: webhook POST → GitHub adapter → router → store → worker → intent classification (Ollama) → executor → response back via a fake GitHub API server. They use the `systemtest` build tag.

```sh
make test-systemtest
```

This single command handles all setup automatically:

1. Starts PostgreSQL via Docker Compose (`postgres-systemtest` on port 5433)
2. Starts a local Docker registry on port 5001
3. Builds and pushes the test sandbox image to the local registry
4. Downloads Ollama to `./bin/` if not already present
5. Starts the Ollama server if not already running
6. Pulls the configured model (default: `qwen3:1.7b`)
7. Builds the `ai-coworker` binary, starts it as a subprocess, and runs the tests

All configuration has sensible defaults in the Makefile. Override with environment variables if needed:

| Variable | Default | Description |
|---|---|---|
| `SYSTEMTEST_DATABASE_URL` | `postgres://ai_coworker:test@localhost:5433/ai_coworker_systemtest?sslmode=disable` | PostgreSQL connection string |
| `SYSTEMTEST_OLLAMA_URL` | `http://localhost:11434/v1` | Ollama API endpoint |
| `SYSTEMTEST_MODEL` | `qwen3:1.7b` | LLM model for intent classification |
| `SYSTEMTEST_REGISTRY` | `localhost:5001` | Docker registry for test sandbox image |
| `SYSTEMTEST_SANDBOX_IMAGE` | `$(SYSTEMTEST_REGISTRY)/ai-coworker-systemtest-sandbox:latest` | Full sandbox image reference |

### Linting

```sh
make lint
```

## Local E2E Testing with KinD

For end-to-end testing on Kubernetes, use the `hack/kind.sh` script to spin up a [KinD](https://kind.sigs.k8s.io/) cluster with the full stack deployed.

### Prerequisites

- [KinD](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) (`kind`)
- [smee-client](https://github.com/probot/smee-client) (`npm install -g smee-client`)
- Google Cloud ADC configured if using Vertex AI — run `gcloud auth application-default login`, which writes credentials to `~/.config/gcloud/application_default_credentials.json`. The deploy script auto-detects this file and mounts it into the pod.

### Workflow

```sh
make kind-create                          # create KinD cluster
make kind-load                            # build images and load into cluster
AI_COWORKER__LLM__PROVIDER=vertex \
AI_COWORKER__LLM__VERTEX__PROJECT_ID=... \
AI_COWORKER__LLM__MODEL=claude-sonnet-4-6 \
AI_COWORKER__GITHUB__ENABLED=true \
AI_COWORKER__GITHUB__APP_ID=... \
AI_COWORKER__GITHUB__PRIVATE_KEY="$(cat key.pem)" \
AI_COWORKER__GITHUB__WEBHOOK_SECRET="$(cat webhook-secret)" \
AI_COWORKER__GITHUB__BOT_USERNAME=my-app \
  make kind-deploy                        # deploy and inject secrets from env vars
SMEE_URL=https://smee.io/... make kind-smee  # forward GitHub webhooks
make kind-delete                          # tear down
```

The `kind-deploy` target collects all `AI_COWORKER__*` env vars into a K8s secret (multiline values like PEM keys are preserved). If Google ADC credentials are found locally, they are automatically mounted into the pod.

Commands can be combined via the script: `./hack/kind.sh --create --load --deploy`. Flags always execute in the correct lifecycle order regardless of argument order.

### What gets deployed

The KinD overlay (`deploy/kubernetes/overlays/kind/`) composes the base manifests with two Kustomize components:

- `components/postgres` — PostgreSQL Deployment with emptyDir storage
- `components/google-adc` — Google ADC secret and volume mount for Vertex AI

## Makefile Reference

### Variables

| Variable | Default | Description |
|---|---|---|
| `REGISTRY` | `ghcr.io/creydr` | Container image registry and namespace |
| `IMAGE` | `$(REGISTRY)/ai-coworker:latest` | Service image |
| `SANDBOX_IMAGE` | `$(REGISTRY)/ai-coworker-sandbox:latest` | Sandbox image |

Override `REGISTRY` to use your own registry:

```sh
REGISTRY=ghcr.io/myorg make docker sandbox-image kind-load
```

### Targets

| Target | Description |
|---|---|
| `make build` | Compile the binary to `./ai-coworker` |
| `make run` | Build and run the service |
| `make test` | Run unit tests |
| `make lint` | Run golangci-lint |
| `make test-systemtest` | Run system tests (sets up all dependencies automatically) |
| `make dev-db` | Start PostgreSQL 16 via Docker Compose |
| `make systemtest-db` | Start PostgreSQL 17 for system tests via Docker Compose |
| `make sandbox-image` | Build the sandbox Docker image locally |
| `make docker` | Build the service Docker image |
| `make kind-create` | Create a KinD cluster for local testing |
| `make kind-load` | Build images and load into KinD |
| `make kind-deploy` | Deploy to KinD with secrets from env vars |
| `make kind-smee` | Forward GitHub webhooks via smee |
| `make kind-delete` | Tear down KinD cluster |
