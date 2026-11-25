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

## When an agent creates items

Agents create items in two ways, and the words you use tell them which:

- **File** ("file an item for this", "log it for later"): the agent proposes the item with `wi new --propose`. Nothing is created yet: the proposal waits for you, in the conversation and in `wi proposal list`. Filing it (`wi proposal file ID`, optionally with your own title, description or labels) creates it as yours; `wi proposal dismiss ID` declines it. The agent is not interrupted either way.
- **Delegate** ("hand this off", "split this into items"): the agent creates the item itself with `wi new`, to follow up on as part of its own work.

An item an agent creates is its item's, not yours: the agent delegated it, and follows it up. It is left out of your list and attention queue; `wi list --delegated-by ITEM` shows what an item delegated. An item may have at most eight unfinished delegations.

- **Take one over**: `wi own --item ITEM` makes a delegated item yours, detached from the agent; it then appears in your list.
- **When the delegating item closes**, its unfinished delegations come back to you, marked unread.

Each item's history records who did what: you (`user:operator`), an item's agent (`item:<id>`), or wi on its own, and what caused it, such as a schedule (`wi events --item ITEM`).

## Agent authority

An agent's `wi` commands act as its item, not as you. Each time an agent starts, the daemon gives it a credential, in the file `WI_ITEM_CREDENTIAL_FILE` names in its environment, and every call the agent and its tools make presents it. The credential stops working when that agent stops or is replaced; `wi` then fails rather than acting as you. Your own commands, including those in an item's shell windows, act as you.

An agent can still do what you allow every agent to do; which items each agent may act on is not limited yet. The credential is not isolation either: an agent that runs as your Unix user can read your own credential. A sandbox mounts the daemon's socket and the agent's credential file but not yours, and sets `WI_API_SOCKET` to where the socket is mounted. See [Endpoint and credentials](../../reference/api/#endpoint-and-credentials).
