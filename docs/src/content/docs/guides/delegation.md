---
title: Delegate work
description: Create self-contained tasks and send prompts to agents.
---

Delegation works best when the receiving agent does not need the conversation that produced the task.

## Create and start in one command

```bash
wi new \
  --agent-mode rpc \
  --prompt "Inspect dependency updates, run the test suite, and report any behavior changes. Do not modify files." \
  "Audit dependency updates"
```

This command:

1. stores the prompt as the item description
2. moves the item to `working`
3. materializes its managed workspace
4. starts an unattached runtime
5. sends the prompt to Pi

TUI is the default mode when `--agent-mode` is omitted. The command starts the normal tmux runtime but does not switch or attach your current terminal.

## Write a stronger handoff

For a substantial task, use a file:

```bash
wi new --desc-file handoff.md "Fix stale runtime ownership"
```

A useful handoff contains:

- why the work matters
- exact observed behavior or reproduction commands
- relevant files and constraints
- a concrete next step
- observable completion criteria

Then start it normally:

```bash
wi start fix-stale-runtime-ownership
```

## Send follow-up work

```bash
wi agent control send --follow-up --item fix-stale-runtime-ownership \
  "Also test PID reuse and report the focused test command."
```

Use `--file` for a multiline message. Delivery and task completion are different events. Monitor the runtime or enter its TUI to inspect the result:

```bash
wi agent monitor --item fix-stale-runtime-ownership --limit 50
wi switch fix-stale-runtime-ownership
```

## Keep authority narrow

An agent sandbox can receive `WI_AGENT_DAEMON_SOCKET`, which points to a restricted daemon endpoint. That endpoint permits safe managed-slot backlog creation but rejects lifecycle, workspace, runtime, terminal, deletion, shutdown, `--home`, and `--prompt` operations.

This lets an agent file follow-up work without granting control over the user's active environment.
