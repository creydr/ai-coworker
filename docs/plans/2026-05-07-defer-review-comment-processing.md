# Fix: Defer review comment processing until review is submitted

## Problem

When a user submits a PR review with multiple inline comments, GitHub fires
a `PullRequestReviewCommentEvent` for each comment **and** a
`PullRequestReviewEvent` — all simultaneously on submission. Our adapter
processed both event types independently, creating duplicate tasks. The
500ms debounce in `absorbReviewTasks` was meant to batch sibling tasks, but
concurrent workers could each claim a task before any transaction committed,
bypassing the `NOT EXISTS` guard (PostgreSQL READ COMMITTED isolation means
uncommitted status changes are invisible to concurrent transactions).

**Example from PR #18 on ai-coworker-test:**
Three workers each claimed a review task concurrently, resulting in 3
sandbox containers instead of 1. One comment received an eyes reaction but
no response because its task failed with a context deadline.

## Root cause

Three independent issues:

1. **Duplicate event sources**: Both `PullRequestReviewCommentEvent` and
   `PullRequestReviewEvent` created tasks for the same review comments.
   Even though they fire simultaneously, processing both doubles the work.

2. **Sequential task creation with interleaved HTTP calls**: The old
   `EventHandler` accepted a single event, so `handlePRReview` called it
   once per comment. Each call went through `router.HandleEvent` which did
   an HTTP roundtrip (eyes reaction) before inserting the task. With 5
   events, this meant ~1-2s between first and last task insert — workers
   could claim the first task before siblings existed, splitting one review
   into multiple sandbox executions.

3. **Concurrent claim race**: `ClaimNextTask`'s `NOT EXISTS` subquery
   checks for in_progress tasks on the same thread, but under READ
   COMMITTED isolation, concurrent transactions don't see each other's
   uncommitted updates. Multiple workers could evaluate `NOT EXISTS` as
   true simultaneously and each claim a different task from the same thread.

## Solution

### 1. Single entry point for review processing

- **Ignore `PullRequestReviewCommentEvent`** entirely — no acknowledgment,
  no task creation. This avoids reacting to comments in pending reviews
  that may never be submitted.
- **`handlePRReview`** is now the single entry point: on `submitted` action,
  it fetches all inline comments via `PullRequests.ListReviewComments` API
  and creates events for the review body + each comment.

### 2. Batch EventHandler

- Changed `EventHandler` from `func(ctx, event)` to `func(ctx, []event)`.
- `handlePRReview` collects all events into a slice and makes **one**
  handler call instead of N sequential calls.
- `router.HandleEvent` processes the batch: acknowledges all events first
  (HTTP roundtrips), then creates all messages and tasks back-to-back
  without network calls between inserts. All tasks become claimable as a
  group, so the debounce + absorption works correctly.

### 3. Advisory lock on thread_id (concurrency fix)

Added `pg_try_advisory_xact_lock(0, hashtext(thread_id))` to `ClaimNextTask`.
This transaction-scoped advisory lock serializes claim attempts per thread,
closing the race window where multiple workers evaluate `NOT EXISTS` before
any `UPDATE` commits.

## All three comment types remain supported

1. **Regular PR comment** (`IssueCommentEvent`) — `handleIssueComment`,
   unaffected by this change.
2. **Single file comment** ("Comment" button, no review) — GitHub fires
   both webhooks; `PullRequestReviewCommentEvent` is ignored, but the
   `PullRequestReviewEvent` (state `COMMENTED`, empty body) triggers
   `handlePRReview` which fetches the 1 comment via API.
3. **Batch review comments** ("Start a review") — all comments are fetched
   and processed together when `PullRequestReviewEvent` fires on submission.

## Files changed

| File | Change |
|------|--------|
| `internal/adapter/adapter.go` | Change `EventHandler` to accept `[]IncomingEvent` |
| `internal/adapter/github/github.go` | Remove `handlePRReviewComment`; ignore webhook; rewrite `handlePRReview` to collect events and call handler once |
| `internal/adapter/github/github_test.go` | Update all handler signatures, add tests for comment fetching with mock GitHub API server |
| `internal/adapter/slack/slack.go` | Wrap single event in slice for new handler signature |
| `internal/engine/router.go` | `HandleEvent` processes `[]IncomingEvent`: acknowledge all, then create all tasks |
| `internal/engine/router_test.go` | Update test calls for new signature |
| `internal/store/postgres.go` | Add `pg_try_advisory_xact_lock` to `ClaimNextTask` |
| `internal/engine/worker.go` | Update comment in `absorbReviewTasks` to reflect advisory lock |

## Verification

1. `go test ./...` — full suite passes
2. Manual: submit a PR review with 3+ inline comments on a bot PR, verify
   all comments get individual responses and only 1 sandbox container is
   created
