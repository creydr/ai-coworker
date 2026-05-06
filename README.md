# AI Coworker

An autonomous AI agent that executes software development tasks end-to-end through pluggable channel adapters. Ships with GitHub and Slack support — mention it on a GitHub issue and it will discuss the problem, write code, and open a pull request; tag it in Slack and it will answer questions or kick off tasks. New channels can be added by implementing the `adapter.Adapter` interface.

https://github.com/user-attachments/assets/e59fb872-1747-41f7-94ff-21a8a600e898

## Quick Start

### Prerequisites

- Go 1.22+
- Docker (used for PostgreSQL and sandbox containers)
- An LLM API key (see [LLM Providers](docs/deployment.md#llm-providers))

### 1. Start the database

```sh
make dev-db
```

### 2. Configure and run

```sh
AI_COWORKER__LLM__API_KEY=sk-ant-... make run
```

This starts the service with the default config (`config.yaml`). Configuration can be provided via the config file or environment variables with the `AI_COWORKER__` prefix (double underscore). Environment variables override the config file.

To use the GitHub handler, you'll need a [GitHub App](#github-app-setup). For the full configuration reference and alternative LLM providers (Vertex AI, OpenAI-compatible), see [docs/deployment.md](docs/deployment.md).

## GitHub App Setup

1. Go to **Settings > Developer settings > GitHub Apps > New GitHub App**.
2. Fill in the basics:
   - **Name:** your app name (e.g. `my-ai-coworker`)
   - **Homepage URL:** any URL
   - **Webhook URL:** your server's public URL + `/webhook/github` (can be configured later)
   - **Webhook secret:** generate a random secret (e.g. via `openssl rand -hex 32`)
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
5. Create the app, then generate a private key from the app settings page (under **Private keys > Generate a private key**). Your browser will download a `.pem` file — store it securely.
6. Install the app on the repositories you want it to monitor.
7. Add the GitHub settings to your `config.yaml`:
   ```yaml
   github:
     enabled: true
     appId: 12345
     botUsername: "my-ai-coworker"
     allowedUsers:
       - "user1"
       - "user2"
   ```

   Set secrets via environment variables:
   ```sh
   export AI_COWORKER__GITHUB__PRIVATE_KEY="$(cat path/to/private-key.pem)"
   export AI_COWORKER__GITHUB__WEBHOOK_SECRET=<your-webhook-secret>
   ```

   `allowedUsers` controls which GitHub users can trigger the bot:
   - **List of usernames** (e.g. `["user1", "user2"]`): only those users can interact with the bot
   - **`["*"]`**: all users are allowed
   - **Empty / unset**: nobody is allowed (secure by default)

All settings can also be provided purely via environment variables — see [docs/deployment.md](docs/deployment.md#environment-variables) for the full reference.

The webhook server listens on port 8080. For local development, use a tool like [smee.io](https://smee.io) or [ngrok](https://ngrok.com) to expose it.

## Slack App Setup

> **Note:** Slack support is experimental and not yet fully tested.

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and create a new app.
2. Enable **Socket Mode** (Settings > Socket Mode) and generate an app-level token with the `connections:write` scope.
3. Under **Event Subscriptions**, enable events and subscribe to:
   - `app_mention`
4. Under **OAuth & Permissions**, add the bot token scopes:
   - `app_mentions:read`
   - `chat:write`
   - `reactions:write`
5. Install the app to your workspace and copy the bot token.
6. Add the Slack settings to your `config.yaml`:
   ```yaml
   slack:
     enabled: true
   ```

   Set secrets via environment variables:
   ```sh
   export AI_COWORKER__SLACK__APP_TOKEN=xapp-...
   export AI_COWORKER__SLACK__BOT_TOKEN=xoxb-...
   ```

All settings can also be provided purely via environment variables — see [docs/deployment.md](docs/deployment.md#environment-variables) for the full reference.

## Usage

### GitHub

Mention the bot by name in any issue or pull request comment on a repository where the GitHub App is installed:

```
@creydr-ai Please fix the broken tests in the CI pipeline
```

The bot will:
1. React with :eyes: to acknowledge
2. Analyze the request and classify the intent
3. For code tasks: clone the repo in a sandbox, run Claude Code, and open a PR
4. For questions: respond directly in the thread

### Slack

> **Note:** Slack support is experimental and not yet fully tested.

Mention the bot in any channel it's been added to:

```
@AI Coworker What does the handleWebhook function do?
```

Responses are threaded automatically.

## Deployment

The default setup uses Docker for local development. Kubernetes deployment with Kustomize manifests is also available. See [docs/deployment.md](docs/deployment.md) for all deployment options, the full configuration reference, and LLM provider setup.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for architecture, project structure, local setup, testing, and the full Makefile reference.
