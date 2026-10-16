---
title: Delegate work
description: Hand off a bounded task without relying on the current conversation.
---

A useful handoff states the problem, evidence, constraints, and completion criteria. Keep it small enough to review as one result. These examples assume current item context; outside an item, add a registered `--repo NAME`.

## Create and delegate

```bash
wi new --desc-file audit.md --start --agent-mode headless \
  --title "Check dependency license changes"
```

Wi stores the description, activates the item, materializes a workspace, starts an unattached runtime, and sends the configured startup message asking the agent to read `wi show`.

Omit `--agent-mode headless` for an unattached TUI you can enter with `wi switch`. `--prompt TEXT` overrides only the startup message, not the description. Resume never sends it again.

## Capture now, start later

```bash
wi new --desc-file handoff.md --title "Fix stale runtime ownership"
wi start fix-stale-runtime-ownership
```

Existing-item `start` opens the runtime without sending a prompt. Ask the agent to read `wi show` in the TUI, or send that instruction through runtime control.

## Prompt-only questions

Descriptions are optional for a one-off question:

```bash
wi new --title "Investigate token refresh hangs" --start \
  --prompt "Why does token refresh sometimes hang?"
```

The prompt goes to the conversation; no description or title is inferred from it. The optional [tmux popup](../tmux/#create-from-a-prompt-popup) generates a title externally before invoking Wi.

## Corrections and separate follow-ups

```bash
wi agent control send --follow-up --item fix-stale-runtime-ownership \
  "Also test PID reuse and report the focused test command."
wi agent monitor --item fix-stale-runtime-ownership --limit 50
```

`--follow-up` waits for the current run to settle; `--file` accepts multiline feedback. Delivery confirms handoff, not completion.

Keep corrections within the original objective in this item. File independent discoveries as new backlog items, then finish and close the original rather than growing it indefinitely.

## Agent authority

A sandbox can use `WI_AGENT_DAEMON_SOCKET` to file safe managed-slot backlog work. That restricted endpoint rejects lifecycle, workspace, runtime, terminal, deletion, shutdown, and `--start` operations.
