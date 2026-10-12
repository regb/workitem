---
title: Review agent work
description: Verify a task's result, request corrections, and finish it.
---

An idle agent is waiting for input, not necessarily finished or correct. Review against the task's completion criteria.

## Read the result

```bash
wi agent status add-api-retries
wi agent monitor --item add-api-retries --limit 50
wi switch add-api-retries
```

Runtime events are compact activity facts. The TUI provides the full conversation.

## Inspect the checkout

```bash
wi workspace status add-api-retries
```

In the reported checkout, use Git and the project's checks:

```bash
git status --short
git diff
go test ./...
```

Choose checks appropriate to the repository. Review the actual diff and output, not just the agent's claim that tests passed.

## Request corrections

```bash
wi agent control send --item add-api-retries \
  "Fix the retry test's final-timeout case, rerun it, and report the result."
```

Use `--file review.md` for longer feedback. Add `--follow-up` to wait for a busy run to settle; otherwise input uses steering semantics.

Keep corrections within this task. File independent improvements as separate items instead of expanding its objective. Use `waiting` for an external dependency, or `shelve` to return work to backlog; see [Lifecycle](../../concepts/lifecycle/).

## Finish

Commit intended changes and ensure the checkout is clean:

```bash
wi merge --item add-api-retries
wi archive add-api-retries
```

Merge uses the registered integration branch but does not push or archive. Archive preserves the description and conversation while removing the item from active work. A completed item is history, not a permanent place to send new tasks.
