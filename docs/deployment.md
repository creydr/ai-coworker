# Deployment

This guide covers all deployment methods for AI Coworker.

## Local (Docker)

### Prerequisites

- Go 1.22+
- Docker (used for PostgreSQL and sandbox containers)
- An LLM API key (see [LLM Providers](#llm-providers))

### 1. Start the database

```sh
make dev-db
```

This runs PostgreSQL 16 via Docker Compose. The schema is applied automatically on first startup.

### 2. Sandbox image

Pre-built multi-arch images are published to `ghcr.io/creydr/ai-coworker-sandbox` on every push to main. The default config already references this image.

To build locally instead:

```sh
make sandbox-image
```

### 3. Configure

All configuration can be provided via `config.yaml` or environment variables with the `AI_COWORKER__` prefix (double underscore). Use `__` to separate nested sections; single `_` within field names is converted to camelCase (e.g. `AI_COWORKER__GITHUB__BOT_USERNAME` maps to `github.botUsername`). Environment variables override the config file.

The config file (`config.yaml`) ships with safe defaults for local development:

```yaml
database:
  url: "postgres://ai_coworker:ai_coworker@localhost:5432/ai_coworker?sslmode=disable"

llm:
  provider: "claude"
  model: "claude-sonnet-4-6"

slack:
  enabled: false

github:
  enabled: false

sandbox:
  runtime: "docker"
  image: "ghcr.io/creydr/ai-coworker-sandbox:latest"
  timeoutSeconds: 600
  cpuLimit: "2"
  memoryLimit: "2Gi"

workers: 4
```

### 4. Run

```sh
make run
```

Or with environment variables inline:

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... make run
```

### Environment Variables

Secrets and deployment-specific values must be set via environment variables:

| Variable | Description |
|---|---|
| `AI_COWORKER__LLM__API_KEY` | Anthropic API key |
| `AI_COWORKER__GITHUB__ENABLED` | Set to `true` to enable the GitHub adapter |
| `AI_COWORKER__GITHUB__APP_ID` | GitHub App ID |
| `AI_COWORKER__GITHUB__PRIVATE_KEY` | GitHub App private key (PEM contents) |
| `AI_COWORKER__GITHUB__WEBHOOK_SECRET` | GitHub webhook secret |
| `AI_COWORKER__GITHUB__BOT_USERNAME` | GitHub App bot username (e.g. `creydr-ai`) |
| `AI_COWORKER__GITHUB__ALLOWED_USERS` | Comma-separated list of GitHub usernames allowed to trigger the bot. Use `*` to allow all users. Empty/unset = nobody allowed (secure by default) |
| `AI_COWORKER__SLACK__ENABLED` | Set to `true` to enable the Slack adapter |
| `AI_COWORKER__SLACK__APP_TOKEN` | Slack app-level token (`xapp-...`) |
| `AI_COWORKER__SLACK__BOT_TOKEN` | Slack bot token (`xoxb-...`) |
| `AI_COWORKER__LLM__VERTEX__PROJECT_ID` | Google Cloud project ID (Vertex AI provider) |
| `AI_COWORKER__LLM__VERTEX__REGION` | Vertex AI region (defaults to `global`) |
| `AI_COWORKER__LLM__OPENAI__BASE_URL` | Base URL for OpenAI-compatible API |
| `AI_COWORKER__DATABASE__URL` | PostgreSQL connection string (overrides config file) |
| `AI_COWORKER__SANDBOX__RUNTIME` | Sandbox runtime: `docker` (default) or `kubernetes` |
| `AI_COWORKER__SANDBOX__NAMESPACE` | Kubernetes namespace for sandbox Jobs (required for `kubernetes` runtime) |
| `AI_COWORKER__SANDBOX__SERVICE_ACCOUNT` | ServiceAccount for sandbox Job pods (optional) |

### LLM Providers

The `AI_COWORKER__LLM__PROVIDER` variable selects which LLM backend to use:

**Claude API (default):**

```sh
export AI_COWORKER__LLM__PROVIDER=claude
export AI_COWORKER__LLM__API_KEY=sk-ant-...
export AI_COWORKER__LLM__MODEL=claude-sonnet-4-6
```

**Vertex AI (Claude on Google Cloud):**

Uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials). Run `gcloud auth application-default login` first.

```sh
export AI_COWORKER__LLM__PROVIDER=vertex
export AI_COWORKER__LLM__VERTEX__PROJECT_ID=my-gcp-project
export AI_COWORKER__LLM__VERTEX__REGION=global  # optional, defaults to "global"
export AI_COWORKER__LLM__MODEL=claude-sonnet-4-6
```

**OpenAI-compatible API (Red Hat MaaS, vLLM, etc.):**

Works with any service that exposes an OpenAI-compatible chat completions endpoint.

```sh
export AI_COWORKER__LLM__PROVIDER=openai
export AI_COWORKER__LLM__OPENAI__BASE_URL=https://my-maas-endpoint.example.com/v1
export AI_COWORKER__LLM__API_KEY=...  # if required by the endpoint
export AI_COWORKER__LLM__MODEL=granite-3.3-8b
```

## Slack App Setup

Create a Slack app at https://api.slack.com/apps and configure:

**Socket Mode:** Enable under *Socket Mode* settings. Create an app-level token with `connections:write` scope — this is the `APP_TOKEN` (`xapp-...`).

**Event Subscriptions:** Enable and subscribe to the `app_mention` bot event.

**Bot Token Scopes** (under *OAuth & Permissions*):

| Scope | Required for |
|-------|-------------|
| `app_mentions:read` | Receiving @-mentions |
| `chat:write` | Posting responses |
| `reactions:write` | Adding eyes reaction to acknowledge messages |
| `channels:history` | Reading thread context in public channels |
| `groups:history` | Reading thread context in private channels |
| `im:history` | Reading thread context in direct messages |
| `mpim:history` | Reading thread context in group direct messages |

Install the app to your workspace. The bot token (`xoxb-...`) is the `BOT_TOKEN`.

## Kubernetes

Kustomize manifests are provided in `deploy/kubernetes/`. They include a Deployment, Service, RBAC for sandbox Job creation, and config/secret templates. The service requires a PostgreSQL database — you can either provide your own or use the included overlay.

### Option A: Bring your own database

Point `AI_COWORKER__DATABASE__URL` (or the ConfigMap) at an existing PostgreSQL instance:

```sh
# Edit the secret with your API keys
vi deploy/kubernetes/base/secret.yaml

# Apply all resources
kubectl apply -k deploy/kubernetes/base/
```

### Option B: Deploy PostgreSQL alongside the service

An overlay at `deploy/kubernetes/overlays/with-postgres/` adds a PostgreSQL 16 Deployment using the `postgres` component. The default ConfigMap already points to this instance. This is suitable for development and testing — for production, use a managed database or operator.

```sh
# Edit the secret with your API keys
vi deploy/kubernetes/base/secret.yaml

# Apply with the PostgreSQL overlay
kubectl apply -k deploy/kubernetes/overlays/with-postgres/
```

### Kustomize Components

The manifests use [Kustomize components](https://kubectl.docs.kubernetes.io/guides/config_management/components/) for composability:

| Component | Description |
|-----------|-------------|
| `components/postgres` | PostgreSQL Deployment with emptyDir storage |
| `components/google-adc` | Google ADC volume mount for Vertex AI authentication |

Compose what you need in your own overlay — see `overlays/kind/` for an example that combines both.

The Deployment mounts `config.yaml` from a ConfigMap and injects secrets as environment variables. The sandbox runtime is pre-configured to `kubernetes` with Jobs created in the `ai-coworker` namespace.

### Customizing the image tag

```sh
cd deploy/kubernetes/base && kustomize edit set image ghcr.io/creydr/ai-coworker:v1.0.0
```

### Webhook delivery

For GitHub webhook delivery, expose the Service via an Ingress or LoadBalancer pointing to port 8080.

## Local E2E Testing with KinD

For end-to-end testing on a local Kubernetes cluster using [KinD](https://kind.sigs.k8s.io/), see the [KinD e2e testing section](../CONTRIBUTING.md#local-e2e-testing-with-kind) in CONTRIBUTING.md.
