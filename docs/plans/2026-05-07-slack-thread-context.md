# Fetch Slack thread history for full conversation context

## Problem

When someone posts non-mentioned messages in a Slack thread and then mentions the bot (e.g., "@AI Coworker ^"), the bot can't see the non-mentioned messages. It only knows about messages that went through its pipeline (mentions + its own responses). The LLM gets no context for "^" and responds with a useless "👍".

## Solution

Fetch the full thread history via Slack's `conversations.replies` API when the bot receives a mention in an existing thread. All messages (including the bot's own responses) are prepended to the event content as context, giving the classifier and executor the full conversation picture.

The thread context is prepended to the task input only — it's not stored as separate message rows in the DB. This avoids any duplication or schema changes.

## Changes

- `internal/adapter/slack/slack.go`: Added `fetchThreadContext` (calls Slack API) and `formatThreadContext` (formats messages as `[Thread context:]` block). The `Start` handler prepends thread context when the message is a thread reply.
- `internal/adapter/slack/slack_test.go`: Tests for `formatThreadContext` covering inclusion of all messages, exclusion of current message, empty threads, chronological order, and empty text skipping.
