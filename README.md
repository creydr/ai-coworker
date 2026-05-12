# AI Coworker

An autonomous AI agent that executes software development tasks end-to-end through pluggable channel adapters. Ships with GitHub, Slack, and Google Docs support — mention it on a GitHub issue and it will discuss the problem, write code, and open a pull request; tag it in Slack and it will answer questions or kick off tasks; assign it an action item in a Google Doc and it will respond right in the comment thread. New channels can be added by implementing the `adapter.Adapter` interface.

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

1. **[Create the GitHub App](https://github.com/settings/apps/new?name=my-ai-coworker&url=https://github.com/creydr/ai-coworker&description=Autonomous+AI+agent+for+software+development+tasks&public=false&webhook_active=true&actions=read&contents=write&issues=write&pull_requests=write&metadata=read&events[]=issue_comment&events[]=pull_request_review&events[]=pull_request_review_comment)** — this link pre-fills the required permissions and events. Fill in:
   - **Webhook URL:** your server's public URL + `/webhook/github` (can be configured later)
   - **Webhook secret:** generate one via `openssl rand -hex 32`

   <details>
   <summary>Manual setup (GitHub Enterprise or custom configuration)</summary>

   1. Go to **Settings > Developer settings > GitHub Apps > New GitHub App**.
   2. Fill in the basics:
      - **Name:** your app name (e.g. `my-ai-coworker`)
      - **Homepage URL:** any URL
      - **Webhook URL:** your server's public URL + `/webhook/github`
      - **Webhook secret:** generate a random secret (e.g. via `openssl rand -hex 32`)
   3. Set permissions:
      - **Repository permissions:**
        - Actions: Read-only
        - Contents: Read & write
        - Issues: Read & write
        - Pull requests: Read & write
        - Metadata: Read-only
   4. Subscribe to events:
      - Issue comment
      - Pull request review
      - Pull request review comment
   5. Create the app.

   </details>

2. Generate a private key from the app settings page (**Private keys > Generate a private key**). Your browser will download a `.pem` file — store it securely.
3. Install the app on the repositories you want it to monitor.
4. Add the GitHub settings to your `config.yaml`:
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

The bot also integrates into Slack — ask it about PRs, issues, repositories or even ask it to provide a fix and it will respond in-thread:

https://github.com/creydr/ai-coworker/raw/main/docs/video/demo-slack-github-integration.mp4

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and create a new app.
2. Enable **Socket Mode** (Settings > Socket Mode) and generate an app-level token with the `connections:write` scope.
3. Under **Event Subscriptions**, enable events and subscribe to:
   - `app_mention`
4. Under **OAuth & Permissions**, add the bot token scopes:
   - `app_mentions:read`
   - `chat:write`
   - `reactions:write`
   - `channels:history`
   - `groups:history`
   - `im:history`
   - `mpim:history`
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

## Google Docs Setup

The bot can monitor Google Docs for comments and action items assigned to it, responding directly in the document's comment threads.

1. **Create a Google Cloud project** (or use an existing one) at [console.cloud.google.com](https://console.cloud.google.com).
2. **Enable the Google Drive API:** Go to **APIs & Services > Library**, search for "Google Drive API", and click **Enable**.
3. **Create a service account:** Go to **IAM & Admin > [Service Accounts](https://console.cloud.google.com/iam-admin/serviceaccounts)**, click **Create Service Account**, give it a name (e.g. `ai-coworker-bot`), and click **Done** (no roles needed).
4. **Generate a JSON key:** Click on the service account, go to the **Keys** tab, click **Add Key > Create new key**, select **JSON**, and click **Create**. Your browser will download a `.json` file — store it securely.
5. **Share your Google Docs** with the service account's email address (shown on the service account page and in the JSON key as `client_email`, e.g. `ai-coworker-bot@my-project.iam.gserviceaccount.com`). Grant **Commenter** access so the bot can read and reply to comments.
6. Add the Google Docs settings to your `config.yaml`:
   ```yaml
   googledocs:
     enabled: true
     serviceAccountKeyPath: "/path/to/service-account-key.json"
     listenAddr: ":8082"
     webhookUrl: "https://your-public-url.example.com"
     documentContentMaxSize: "100KB"
   ```

   - `listenAddr`: address for the local webhook server (default `:8082`)
   - `webhookUrl`: public HTTPS URL registered with Google Drive for push notifications. For local development, use the URL from [ngrok](https://ngrok.com) or a [smee.io](https://smee.io) channel.
   - `documentContentMaxSize`: max document context size sent to the LLM (`100KB`, `1MB`, or `0` to disable the limit)

The adapter uses Google Drive push notifications to detect changes, then polls comments only on modified documents. To receive push notifications locally, use a tool like [smee.io](https://smee.io) or [ngrok](https://ngrok.com) to expose the webhook endpoint.

**Interacting with the bot:**
- **Mention:** Include the service account email in a comment (e.g. `@bot-sa@project.iam.gserviceaccount.com please review this section`)
- **Action item:** Assign an action item to the service account email in a comment

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

Mention the bot in any channel it's been added to:

```
@AI Coworker What does the handleWebhook function do?
```

Responses are threaded automatically.

### Google Docs

Assign an action item or mention the bot's service account email in a comment on any shared Google Doc:

```
@bot-sa@project.iam.gserviceaccount.com Can you summarize the key decisions in this document?
```

The bot will:
1. Reply with "Looking into this..." to acknowledge
2. Read the full document content and comment history for context
3. Respond directly in the comment thread

## Skill Images

Skill images are OCI container images that package [Claude Code skills](https://docs.anthropic.com/en/docs/claude-code/skills) for use inside sandbox containers. They let you extend the agent's capabilities without rebuilding the sandbox image.

A skill image is a `scratch`-based container with skill directories under `/skills/{name}/`:

```dockerfile
FROM scratch
COPY . /skills/
```

Configure them globally in `config.yaml`:

```yaml
sandbox:
  skillImages:
    - "quay.io/myorg/my-skills:latest"
```

At runtime, skill files are mounted read-only into the sandbox at `/opt/skills-{n}/` and symlinked into `~/.claude/skills/` so Claude Code discovers them automatically. Both Docker (via bind-mount) and Kubernetes (via native OCI image volumes) runtimes are supported.

## Deployment

The default setup uses Docker for local development. Kubernetes deployment with Kustomize manifests is also available. See [docs/deployment.md](docs/deployment.md) for all deployment options, the full configuration reference, and LLM provider setup.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for architecture, project structure, local setup, testing, and the full Makefile reference.
