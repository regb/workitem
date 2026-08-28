---
title: Review agent work
description: Inspect changes, verify results, and send feedback to the same durable conversation.
---

An idle agent is ready for input. It is not automatically finished, correct, or ready to merge.

## Read the agent's result

Enter an interactive item:

```bash
wi switch add-api-retries
```

For delegated work, inspect recent runtime activity:

```bash
wi agent status add-api-retries
wi agent monitor --item add-api-retries --limit 50
```

Runtime events are deliberately compact. Enter the TUI when you need the full conversation:

```bash
wi switch add-api-retries
```

## Inspect the checkout

First confirm the assigned path, branch, HEAD, and dirty state:

```bash
wi workspace status add-api-retries
```

Enter the item's TUI or change to the reported checkout path. Then use ordinary Git and project tools:

```bash
git status --short
git diff --stat
git diff
go test ./...
```

Choose the test command appropriate to the repository. Do not treat an agent's statement that tests passed as a replacement for reviewing the actual diff and command output.

## Ask for corrections

Keep feedback in the same work item so the checkout and conversation remain together:

```bash
wi agent control send --item add-api-retries \
  "The retry test still fails when the final attempt times out. Fix it, run the focused test, and report the exact command and result."
```

Use a file for longer feedback:

```bash
wi agent control send --item add-api-retries --file review.md
```

While an agent is busy, `control send` uses steering semantics. Add `--follow-up` when the message must wait until the current run settles:

```bash
wi agent control send --follow-up --item add-api-retries \
  "After the current work, summarize any compatibility risk."
```

## Decide the next lifecycle action

After review:

- Leave accepted active work as `working` until integration.
- Set it to `waiting` if review depends on CI, another person, or an external answer.
- Use `shelve` if you want to stop active work and return it to backlog.
- Send a follow-up if changes are required.

A stopped runtime does not erase the conversation. You can stop a settled agent and restart it later:

```bash
wi agent runtime stop add-api-retries
wi agent runtime status add-api-retries
wi resume add-api-retries
```

`resume` is for waiting items. Use `start` for backlog items and `switch` to re-enter an already working TUI item.

## Integrate only after verification

The source checkout must be completely clean before `wi merge`. Commit intended changes with normal Git commands, verify the result, then run:

```bash
wi merge --item add-api-retries main
wi archive add-api-retries
```

Merge does not push or archive. Archive does not merge. Keeping those decisions separate gives you a chance to inspect each result.
