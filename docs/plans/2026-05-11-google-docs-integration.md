# Google Docs Adapter — Design Document

## Context

AI Coworker currently supports Slack and GitHub as communication channels. Teams also collaborate heavily in Google Docs — writing specs, filing bugs in shared documents, and assigning action items. Adding a Google Docs adapter lets users interact with the bot directly where they already work, through document comments and action items. This follows the same adapter pattern as the existing integrations.

## Interaction Model

Users interact with the bot in two ways:

1. **Comment mentions** — @mention the bot's service account email in a document comment. The bot replies in the same comment thread.
2. **Action items** — Assign an action item to the bot's service account email. The bot picks up the assignment, executes the task, and replies in the comment thread. It can optionally mark the action item as resolved once complete.

## Authentication

A Google Cloud service account authenticates via JSON key file (or Workload Identity in Kubernetes). The service account gets its own email address (e.g., `ai-coworker@project.iam.gserviceaccount.com`). Users share documents with this email to enable the bot on those documents — unsharing disables it.

Required APIs: Drive API, Docs API.

## Event Detection

A two-layer hybrid approach:

### Layer 1: Drive Push Notifications

On startup, the adapter calls `changes.watch` on the Drive API to register a webhook. Google pushes notifications when any file accessible to the service account changes. The notification includes the file ID but not what changed.

The webhook endpoint is mounted on the same HTTP server the GitHub adapter uses, on a separate path (e.g., `/webhooks/googledocs`).

### Layer 2: Comment Polling on Change

On each push notification, the adapter fetches comments for that specific document via the Drive Comments API, filtered to comments modified since the last check. It filters for:

- Comments that @mention the bot's service account email
- Action items assigned to the bot's service account email
- Only unresolved comments (resolved ones are ignored)

### Deduplication & State

The adapter tracks the last-seen comment timestamp per document in the existing database, using a new `adapter_state` table (`adapter TEXT, key TEXT, value TEXT`). This survives server restarts. After a restart, the adapter picks up where it left off — any comments posted during downtime are processed as a batch.

### Local Development

Use smee.io to forward webhook notifications to localhost:

```sh
smee -u https://smee.io/<your-channel> --target http://localhost:8080/webhooks/googledocs
```

## Threading

Each comment thread in a Google Doc maps to one conversation thread in ai-coworker:

```
ChannelRef:
  Channel:    "googledocs"
  ThreadKey:  "{document_id}#{comment_id}"
  Properties:
    document_id: string
    comment_id:  string
```

## Document Context

The adapter exports the full document as plain text (via Drive API export) and appends all comment threads. This combined content is included in the `IncomingEvent.Metadata` so the executor has the full picture of the document and ongoing discussions.

A configurable size cap (default 100KB, ~50 pages) prevents context blowup on very large documents. The cap can be set to `0` to disable truncation.

## Response & Acknowledgment

- **Acknowledge**: When the adapter picks up a comment, it replies to the comment thread with a short acknowledgment (e.g., "Looking into this...").
- **SendResponse**: When the executor finishes, the adapter posts the result as a reply in the same comment thread via the Drive Comments API. For action items, it additionally marks the action item as resolved.

## Execution

Same model as the Slack adapter: the bot looks for GitHub URLs or repo references in the comment or document content, clones the repo, and runs Claude Code in the sandbox. The intent classification and executor selection (sandbox vs. LLM-only) works identically to other adapters.

## Configuration

```yaml
googledocs:
  enabled: true
  serviceAccountKeyPath: "/path/to/service-account-key.json"
  webhookPath: "/webhooks/googledocs"
  pollIntervalSeconds: 30
  documentContentMaxSize: "100KB"  # "0" to disable cap
```

## Code Layout

```
internal/adapter/googledocs/
├── googledocs.go    # Adapter implementation (Start, SendResponse, Acknowledge)
├── ref.go           # ChannelRef encoding/decoding
└── comments.go      # Drive/Docs API interactions (fetch comments, post replies)
```

The adapter implements the `Adapter` interface from `internal/adapter/adapter.go` and is registered in `cmd/ai-coworker/main.go` alongside the existing adapters.

## Dependencies

- `google.golang.org/api/drive/v3` — Drive API client (push notifications, comments, file export)
- `google.golang.org/api/docs/v1` — Docs API client (document content)

## Database Changes

New table `adapter_state`:

| Column  | Type | Purpose                                      |
|---------|------|----------------------------------------------|
| adapter | TEXT | Adapter name (e.g., "googledocs")            |
| key     | TEXT | State key (e.g., document ID)                |
| value   | TEXT | State value (e.g., last-seen comment timestamp) |

Primary key: `(adapter, key)`.
