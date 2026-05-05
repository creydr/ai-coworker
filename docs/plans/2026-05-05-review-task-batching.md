# Review Task Batching — Design Document

## Problem

When a GitHub PR review with N inline comments mentioning the bot is submitted, GitHub sends N+1 webhook events: 1 `pull_request_review` and N `pull_request_review_comment`. The current architecture creates one Task per webhook, each spawning its own sandbox. This wastes resources (N+1 containers instead of 1) and produces N+1 independent responses that don't coordinate with each other.

Example: a reviewer submits a review with 3 inline comments all mentioning the bot. The system creates 4 tasks (1 review body + 3 comments), each claiming a worker, spawning a sandbox, cloning the repo, and running Claude Code independently. Each sandbox only sees its own comment, not the full review context.

## Goals

- Batch review tasks from the same PR into a single sandbox execution.
- Produce per-comment responses so each inline comment gets its own tailored reply.
- Keep the store layer channel-agnostic — no GitHub-specific concepts in the persistence layer.
- Keep the worker layer decoupled from channel adapters — no GitHub imports.
- Graceful degradation: if batching fails or the LLM doesn't produce structured output, fall back to sending the full response to all comments.

## Non-Goals

- Batching across different reviewers on the same PR (future enhancement via `review_id`).
- Structured response format beyond simple text markers (no JSON/XML parsing).
- Changes to the adapter or router — they remain simple event-to-task pipelines.

## Design

### Current Flow

```
GitHub webhook (per comment)
  → Adapter: normalize to IncomingEvent
  → Router: get/create Thread, store Message, create Task (pending)
  → Worker: ClaimNextTask, classify intent, spawn sandbox, route response
```

Each webhook independently creates a Task. Workers independently claim and execute each Task in its own sandbox.

### New Flow

```
GitHub webhook (per comment)
  → Adapter: normalize to IncomingEvent (same as before)
  → Router: get/create Thread, store Message, create Task (same as before)
  → Worker: ClaimNextTask, classify intent as review
      → debounce 500ms (let remaining webhooks arrive)
      → ClaimPendingTasks(threadID) — absorb all pending tasks on same thread
      → filter: keep review-related tasks, release others back to pending
      → merge inputs into structured prompt with comment indices
      → spawn ONE sandbox with merged prompt
      → parse per-comment responses from output
      → route each response to its respective comment
      → mark absorbed tasks completed
```

### Store: Generic `ClaimPendingTasks`

A new `ClaimPendingTasks(ctx, threadID, workerID)` method on the Store interface atomically claims ALL pending tasks on a thread. It uses `FOR UPDATE SKIP LOCKED` like `ClaimNextTask` to avoid conflicts with other workers.

This method is intentionally generic — it knows nothing about reviews or GitHub. The worker decides which claimed tasks to absorb and which to release.

### Worker: Absorption Logic

When a worker classifies a task as `IntentReview`:

1. **Debounce** — sleep 500ms. GitHub webhooks for the same review arrive within milliseconds, but the router needs time to persist them as tasks. The debounce ensures most sibling tasks are in the database before we try to absorb them.

2. **Claim siblings** — call `ClaimPendingTasks` to atomically grab all pending tasks on the same thread.

3. **Filter** — keep tasks where `metadata["type"]` is `"review"` or `"review_comment"`. Release any other tasks (e.g. an unrelated issue comment that arrived simultaneously) back to `pending` status.

4. **Merge prompt** — assign each task (primary + absorbed) a stable index. Build a structured prompt listing all comments with their file paths and indices. Request the LLM to respond using `--- COMMENT N ---` markers.

5. **Execute** — run a single sandbox with the merged prompt.

6. **Parse** — split the output on `--- COMMENT N ---` markers to extract per-comment responses. If parsing fails, fall back to the full output for all comments.

7. **Route** — send each parsed response to its respective comment using the existing `routeResponse` mechanism, which reads `comment_id` and `comment_type` from task metadata.

8. **Complete** — mark all absorbed tasks as completed.

### Race Conditions

Two workers could claim sibling review tasks simultaneously before either debounces:

- Worker A claims Task 1 (review body), sleeps 500ms, then calls `ClaimPendingTasks`
- Worker B claims Task 2 (review comment) during that 500ms

When Worker A calls `ClaimPendingTasks`, Task 2 is already `in_progress` (not `pending`) and won't be returned. Worker B will independently process Task 2.

This is suboptimal (2 sandboxes instead of 1) but **not incorrect** — both comments still get responses. The 500ms debounce makes this unlikely in practice since GitHub webhooks arrive within milliseconds and the worker poll interval is 1 second.

### GitHub Adapter: `review_id` Metadata

Both `handlePRReview` and `handlePRReviewComment` will include a `review_id` field in the event metadata, derived from the GitHub review ID. This provides:

- **Observability** — log correlation across webhooks from the same review
- **Future enhancement** — batch per-reviewer instead of per-thread, supporting concurrent reviews from different people on the same PR

## Implementation Plan

1. Add `ClaimPendingTasks` to store interface and Postgres implementation
2. Add `review_id` to GitHub adapter metadata
3. Add absorption, merging, parsing, and routing logic to the worker
4. Update executor prompt for batched reviews
5. Update tests (mock stores, worker tests, store integration test)