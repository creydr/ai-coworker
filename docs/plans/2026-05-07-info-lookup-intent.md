# Add `info_lookup` intent for questions requiring external data

## Problem

When a user asks the AI Coworker on Slack about a GitHub PR (e.g., "can you give me a summary of PR #18?"), the intent classifier categorizes it as `question`. Questions are routed to the LLM executor, which is a plain LLM chat with no tool access — so it correctly replies "I can't access GitHub." The code executor (sandbox with `gh` CLI) can look things up, but spinning up a container for every simple question is wasteful.

## Solution

Add a new `info_lookup` intent for questions that require external data access (repos, PRs, issues, URLs). These get routed to the code executor (sandbox), while simple questions stay on the fast LLM path.

## Changes

### 1. `internal/domain/task.go` — Add new intent constant

Add `IntentInfoLookup Intent = "info_lookup"` alongside the existing constants.

### 2. `internal/engine/intent.go` — Update classifier prompt and parser

**Classify prompt**: Add `info_lookup` category:
```
- info_lookup: the user is asking about something that requires looking up external information (e.g. a GitHub PR, issue, repository, URL, or project state)
```

Reword `question` to clarify it's for things answerable without external tools:
```
- question: the user is asking a question that can be answered from general knowledge or the current conversation context
```

**parseIntent**: Add `"info_lookup"` case returning `domain.IntentInfoLookup`.

### 3. `internal/engine/worker.go` — Route `info_lookup` to code executor

Update the switch to include `IntentInfoLookup`:
```go
case domain.IntentCodeTask, domain.IntentReview, domain.IntentInfoLookup:
    exec = wp.codeExec
```

### 4. `internal/engine/intent_test.go` — Add parse test cases

Add test entries for `info_lookup`: exact match, case variations, whitespace.

### 5. `internal/engine/worker_test.go` — Add routing test

Add `TestWorker_IntentRouting_InfoLookup` following the existing test patterns.
Configure classifier to return `"info_lookup"`, verify `codeExec` is called and `llmExec` is not.

## Files changed

| File | Action |
|------|--------|
| `internal/domain/task.go` | Add `IntentInfoLookup` constant |
| `internal/engine/intent.go` | Update classifier prompt and `parseIntent` switch |
| `internal/engine/intent_test.go` | Add `info_lookup` parse test cases |
| `internal/engine/worker.go` | Add `IntentInfoLookup` to code executor routing |
| `internal/engine/worker_test.go` | Add routing test for `info_lookup` |

## Verification

1. `go test ./internal/engine/...` — engine tests pass
2. `go test ./...` — full suite passes
3. Manual: ask the bot on Slack "can you give me a summary of PR #18?" and verify it gets classified as `info_lookup`, spins up a sandbox, and returns actual PR details