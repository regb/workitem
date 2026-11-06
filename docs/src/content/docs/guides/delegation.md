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

Wi stores the description, activates the item, materializes a workspace, starts an unattached agent, and sends the configured startup message asking the agent to read `wi show`. The daemon does the starting as a job, so it finishes even if the command is interrupted.

Omit `--agent-mode headless` for an unattached TUI you can enter with `wi switch`. `--prompt TEXT` overrides only the startup message, not the description. Resume never sends it again.

## Capture now, start later

```bash
wi new --desc-file handoff.md --title "Fix stale runtime ownership"
wi start fix-stale-runtime-ownership
```

Existing-item `start` starts the agent without sending a prompt. Ask the agent to read `wi show` in the TUI, or send that instruction with `wi agent control send`.

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
wi agent monitor --content --item fix-stale-runtime-ownership
```

`--follow-up` waits for the current run to settle; `--file` accepts multiline feedback. Delivery confirms handoff, not completion.

Keep corrections within the original objective in this item. File independent discoveries as new backlog items, then finish and close the original rather than growing it indefinitely.

## Agent authority

A sandbox can point `WI_AGENT_DAEMON_SOCKET` at the daemon's restricted agent endpoint to file safe managed-slot backlog work. The CLI checks that the socket really is that endpoint and never starts a daemon for it. The endpoint allows reading items, repositories and agent profiles, creating backlog items, and adding notes and attachments; it rejects lifecycle, workspace, agent, terminal, deletion, shutdown, and `--start` operations. See [Endpoints](../../reference/api/#endpoints).
