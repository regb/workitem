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

Existing-item `start` starts the agent without sending a prompt. Ask the agent to read `wi show` in the TUI, or send that instruction with `wi send`.

## Prompt-only questions

Descriptions are optional for a one-off question:

```bash
wi new --title "Investigate token refresh hangs" --start \
  --prompt "Why does token refresh sometimes hang?"
```

The prompt goes to the conversation; no description or title is inferred from it. The optional [tmux popup](../tmux/#create-from-a-prompt-popup) generates a title externally before invoking Wi.

## Corrections and separate follow-ups

```bash
wi send --follow-up --item fix-stale-runtime-ownership \
  "Also test PID reuse and report the focused test command."
wi agent monitor --content --item fix-stale-runtime-ownership
```

`--follow-up` waits for the current run to settle; `--file` accepts multiline feedback. Delivery confirms handoff, not completion.

Keep corrections within the original objective in this item. File independent discoveries as new backlog items, then finish and close the original rather than growing it indefinitely.

## When an agent creates items

Agents create items in two ways, and the words you use tell them which:

- **File** ("file an item for this", "log it for later"): the agent proposes the item with `wi new --propose`. Nothing is created yet: the proposal waits for you as a card in that item's conversation, and the item is marked unread so it comes to your attention; `wi proposal list` lists every pending one. Filing it (`wi proposal file ID`, optionally with your own title, description or labels) creates it as yours; `wi proposal dismiss ID` declines it. The agent is not interrupted either way.
- **Delegate** ("hand this off", "split this into items"): the agent creates the item itself with `wi new`, to follow up on as part of its own work.

An item an agent creates is its item's, not yours: the agent delegated it, and follows it up. It is left out of your list and attention queue while the items that own it are active, unless its agent has a problem or waits on a question; `wi list --delegated-by ITEM` shows what an item delegated. The item that delegated it manages it, and so does whatever owns that item. A follow-up message on a blocker is sent as whoever set the blocker, so one an agent set reaches the item as that agent's message. An item may have at most eight unfinished delegations.

- **Take one over**: `wi own --item ITEM` makes a delegated item yours, detached from the agent; it then appears in your list.
- **When the delegating item closes**, its unfinished delegations come back to you, marked unread.

Agents talk to each other with `wi send`:

```bash
wi send parser-fix --wait "Do the checks pass?"
```

The message reaches the other agent as the dashboard would send it, starting the agent when it is not running. With `--wait` the command waits for the turn it started to settle and prints the agent's last reply, so an agent asking another gets the answer as the command's result. A message from another item's agent arrives in an envelope wi writes, naming the sending item and how it relates (the item that delegated this one, one this one delegated, or another); every agent is told to trust the envelope and to weigh what is inside, not to take it as your instructions. The conversation shows the message without the envelope, marked as from that item.

Each item's history records who did what: you (`user:operator`), an item's agent (`item:<id>`), or wi on its own, and what caused it, such as a schedule (`wi events --item ITEM`).

## Agent authority

An agent's `wi` commands act as its item, not as you. Each time an agent starts, the daemon gives it a credential, in the file `WI_ITEM_CREDENTIAL_FILE` names in its environment, and every call the agent and its tools make presents it. The credential stops working when that agent stops or is replaced; `wi` then fails rather than acting as you. Your own commands, including those in an item's shell windows, act as you.

What an agent may do to items other than its own and its delegations is what `wi access` grants, up to a level: view, message, control (drive its agent and checkout), integrate (approve and merge) or manage (close and reopen). Each installation starts with one entry, every item's agent may manage every item, which keeps what agents could always do; revoke it and grant what you mean:

```bash
wi access                                   # what applies
wi access revoke default-every-item
wi access grant --every-item --everything --level view
wi access grant --item orchestrator --on parser-fix --level integrate
```

A refused agent is told which level it lacks and the command to grant it. Deleting items, taking ownership, filing proposals, changing access, repositories, profiles and schedules, and stopping the daemon are yours only.

The credential is not isolation either: an agent that runs as your Unix user can read your own credential. A sandbox mounts the daemon's socket and the agent's credential file but not yours, and sets `WI_API_SOCKET` to where the socket is mounted. See [Endpoint and credentials](../../reference/api/#endpoint-and-credentials).
