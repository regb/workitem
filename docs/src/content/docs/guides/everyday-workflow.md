---
title: Everyday workflow
description: Move work from capture through review without losing context.
---

This guide uses the composed commands intended for normal use.

## Capture work first

Create backlog items as soon as the task is clear enough to hand off:

```bash
wi new --label backend --desc-file task.md "Harden token refresh"
```

The description should survive independently of your current terminal and chat history.

Use `--deep` when the item should consume configured deep-work capacity:

```bash
wi new --deep --desc-file design.md "Redesign token storage"
```

## Start deliberately

```bash
wi start harden-token-refresh
```

Starting changes backlog to working, checks capacity, materializes the workspace, and starts the selected runtime. The default TUI mode enters tmux. Use `--agent-mode rpc` for a headless run.

For a small task where the title is enough context, create and start it in one command:

```bash
wi start --new "Fix typo"
```

The explicit `--new` flag prevents ordinary `start` from creating an item by accident. Use `wi new` when you need a description, labels, a custom title, or other creation options.

## Supervise active work

```bash
wi list
wi switch harden-token-refresh
wi next
```

Use `wi show` to recover durable context and `wi events` to inspect what happened. Agent and worktree observations are separate columns in `wi list`.

## Handle pauses honestly

If work is blocked but still active:

```bash
wi state set waiting harden-token-refresh
wi resume harden-token-refresh
```

If you want it out of the active working set:

```bash
wi shelve harden-token-refresh
```

Shelving returns the item to backlog and releases resources where safe. It does not erase the branch, description, or conversation.

## Review the result

An idle agent is waiting for input, not automatically finished. Inspect the reported checkout with normal Git tools, run the relevant tests yourself, and send corrections through the same work item. See [Review agent work](../review-and-follow-up/) for the complete loop.

## Integrate and retain history

When the checkout is clean and the result is ready:

```bash
wi merge --item harden-token-refresh main
wi archive harden-token-refresh
```

Merge and archive are separate. Integration does not imply that the case file should disappear, and archive does not silently merge code.

Delete only when you intend to remove the complete archived record:

```bash
wi delete --yes --item ITEM_ID
```
