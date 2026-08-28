---
title: Quick start
description: Create a work item, give its agent a task, and complete the work.
---

Run these commands inside a Git repository. There are two useful ways to begin: work interactively in Pi, or delegate a prompt without entering the agent session.

## Path 1: work interactively

Write enough context for a future agent to act without your current terminal history:

```bash
cat > /tmp/task.md <<'TASK'
Add retry handling to the API client.

Done when:
- transient network failures use bounded exponential backoff
- tests cover success after a retry and final failure
TASK

wi new --desc-file /tmp/task.md "Add API retries"
```

`wi new` prints the item ID and active slug. It creates a backlog item but no worktree, tmux session, conversation, or process.

Start the slug printed by `wi new`:

```bash
wi start add-api-retries
```

This moves the item to `working`, materializes a managed worktree, starts the primary Pi TUI, and enters its tmux session.

:::caution[Starting does not submit the description]
`wi start` opens the agent runtime, but it does not turn `DESCRIPTION.md` into a prompt. At the Pi prompt, ask the agent to read the durable context and do the work:

```text
Read this work item's context with `wi show`. Implement the task, run the relevant tests, and report what changed.
```
:::

You can now work with Pi normally. Leave the tmux session when you want to do something else. The item, checkout, and conversation remain available.

## Path 2: delegate without attaching

To store and submit the initial prompt in one operation:

```bash
wi new \
  --agent-mode rpc \
  --prompt "Add bounded retry handling to the API client, test success after a retry and final failure, then report what changed." \
  "Add API retries"
```

This creates the item, moves it to `working`, starts a headless runtime, and sends the prompt to Pi. Omit `--agent-mode rpc` to start an unattached TUI runtime that you can enter later.

Watch the delegated item:

```bash
wi list
wi agent status add-api-retries
wi agent monitor --item add-api-retries --limit 50
```

Control delivery means the runtime accepted the prompt. It does not mean the task is complete.

## Inspect and follow up

```bash
wi show add-api-retries
wi agent status add-api-retries
wi workspace status add-api-retries
wi events add-api-retries
```

When the agent is idle, review its changes before deciding that the task is done. Send another message if needed:

```bash
wi agent control send --item add-api-retries \
  "Fix the failing retry test, rerun it, and report the command and result."
```

See [Review agent work](../../guides/review-and-follow-up/) for the full review loop.

## Pause or finish

Use the operation that matches your intent:

```bash
wi state set waiting add-api-retries  # blocked on an external answer
wi resume add-api-retries             # return waiting work to working
wi shelve add-api-retries             # clean up and return it to backlog
```

When the checkout is clean and the branch is ready:

```bash
wi merge --item add-api-retries main
wi archive add-api-retries
```

Read [Lifecycle](../../concepts/lifecycle/) before using primitive state changes for cleanup. `wi state set` changes durable state only. `shelve` and `archive` coordinate cleanup.

## Where to go next

- Interactive users should read [Everyday workflow](../../guides/everyday-workflow/) and [Use tmux](../../guides/tmux/).
- For background work, read [Delegate work](../../guides/delegation/).
- Before accepting changes, read [Review agent work](../../guides/review-and-follow-up/).
- For scripts and diagnostics, use the [Command map](../../reference/commands/) and [Data and diagnostics](../../reference/data-and-diagnostics/).
