# Contributing

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
AI_COWORKER__GITHUB__BOT_USERNAME="your-bot[bot]" \
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

### LLM Providers

The service supports three LLM backends: Claude (direct API), Vertex AI (Claude on Google Cloud), and OpenAI-compatible APIs. See the [README](README.md#llm-providers) for provider-specific configuration.

### Using Vertex AI

```sh
AI_COWORKER__LLM__PROVIDER=vertex \
AI_COWORKER__LLM__VERTEX__PROJECT_ID=my-gcp-project \
AI_COWORKER__LLM__MODEL=claude-sonnet-4-6 \
make run
```

Vertex AI uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials). Run `gcloud auth application-default login` first.

## GitHub App Setup for Development

Create a **separate** GitHub App for local development (don't reuse your production app).

1. Go to **Settings > Developer settings > GitHub Apps > New GitHub App**.
2. Fill in the basics:
   - **Webhook URL:** a placeholder for now (you'll update this with your smee.io URL in the next section)
   - **Webhook secret:** generate a random string and save it to a file
3. Set repository permissions:
   - Contents: **Read & write**
   - Issues: **Read & write**
   - Pull requests: **Read & write**
   - Metadata: **Read-only**
4. Subscribe to events:
   - Issue comment
   - Pull request review
   - Pull request review comment
5. Create the app, then:
   - Note the **App ID** from the app's settings page
   - Generate a **private key** and download the `.pem` file
6. Install the app on a **test repository** (not your production repos).

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

### Linting

```sh
make lint
```

## Architecture

See [docs/architecture.md](docs/architecture.md) for an overview of the system design, component responsibilities, and the thread-based conversation model.

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
AI_COWORKER__GITHUB__WEBHOOK_SECRET="$(cat secret)" \
AI_COWORKER__GITHUB__BOT_USERNAME=my-bot \
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

| Target | Description |
|---|---|
| `make build` | Compile the binary to `./ai-coworker` |
| `make run` | Build and run the service |
| `make test` | Run unit tests |
| `make lint` | Run golangci-lint |
| `make dev-db` | Start PostgreSQL 16 via Docker Compose |
| `make sandbox-image` | Build the sandbox Docker image locally |
| `make docker` | Build the service Docker image |
| `make kind-create` | Create a KinD cluster for local testing |
| `make kind-load` | Build images and load into KinD |
| `make kind-deploy` | Deploy to KinD with secrets from env vars |
| `make kind-smee` | Forward GitHub webhooks via smee |
| `make kind-delete` | Tear down KinD cluster |
