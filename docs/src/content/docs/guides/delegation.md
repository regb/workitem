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

An item an agent creates with `wi new`, whether you asked it to file something for later or it split off part of its work, is a delegation, owned by the agent's item. The item's page shows its open delegations above the composer, and all of them, closed ones too, in its details.

An item an agent creates is its item's, not yours: the agent delegated it, and follows it up. When a delegation's turn ends, or it waits on a question, the owning item's agent receives a notice from it, quoting its reply or naming the question, so it can answer it, or merge and close it: agents close the items they delegated once their work is merged or no longer needed, without asking; no notice is sent for a turn it already waits on with `wi send --wait`. A notice waits for the owner's current turn instead of interrupting it, and starts the owner's agent when it is not running; a closed owner gets none, having given its delegations back when it closed. It is left out of your list and attention queue, since its attention goes to its owner; `wi list --delegated-by ITEM` shows what an item delegated. While any of an item's active delegations is working, its agent busy or waiting on work of its own, the item counts as working too, as it does while its agent waits on background work: an idle owner with a reply you have not read stays out of your attention queue until its delegations settle and their notices have been handled. A question or a problem still needs you at once. The item that delegated it manages it, and so does whatever owns that item. A follow-up message on a blocker is sent as whoever set the blocker, so one an agent set reaches the item as that agent's message.

- **Take one over**: "Take it" in its details, or `wi edit --owner me --item ITEM`, makes it yours; it then appears in your list. The item that delegated it is told, if its agent is running, that it no longer follows it up, and it keeps no access to it beyond what you granted. `wi edit --owner OTHER-ITEM` hands an item to another item's agent instead.
- **When the delegating item closes**, its unfinished delegations come back to you, marked unread.

Agents talk to each other with `wi send`:

```bash
wi send parser-fix --wait "Do the checks pass?"
```

The message reaches the other agent as the dashboard would send it, starting the agent when it is not running. With `--wait` the command waits for the turn it started to settle and prints the agent's last reply, so an agent asking another gets the answer as the command's result. An item's owner is its agent's user: what the owner sends arrives as typed, whether the owner is you or the item whose agent delegated it. Anyone else's message arrives in an envelope wi writes, naming the sender and how it relates: an item this one delegated, another item, or a person writing directly to an item another item owns. Every agent is told to trust the envelope and to weigh another agent's message as a request, not as its user's instructions; a person's prevails over its owner's. The conversation shows the message without the envelope, marked as from its sender.

Each item's history records who did what: you (`user:operator`), an item's agent (`item:<id>`), or wi on its own, and what caused it, such as a schedule (`wi events --item ITEM`).

## Agent authority

An agent's `wi` commands act as its item, not as you. Each time an agent starts, the daemon gives it a credential, in the file `WI_ITEM_CREDENTIAL_FILE` names in its environment, and every call the agent and its tools make presents it. The credential stops working when that agent stops or is replaced; `wi` then fails rather than acting as you. Your own commands, including those in an item's shell windows, act as you.

What an agent may do to items other than its own and its delegations is what `wi access` grants, up to a level, and reading an item covers the items it delegated, however far down; higher levels do not pass down: view, message, control (drive its agent and checkout), integrate (approve and merge) or manage (close and reopen). An agent starts with no access to other items: grant what you mean. Installations that began with an entry letting every item's agent manage every item (`default-every-item`) lose it when the daemon opens their database.

```bash
wi access                                   # what applies
wi access grant --every-item --everything --level view
wi access grant --item orchestrator --on parser-fix --level integrate
```

Mentioning an item in a message you send an agent, as the web's @@ mentions write `[title](wi:ID)`, lets that agent read it: its item gains view access to the mentioned item, unless it already had view or more, and a level it has is never lowered. A message from another agent grants nothing. Above the web's message box, each item the draft mentions shows what this agent may do to it, with a choice to let it message and steer that item, or drive its agent.

A refused agent is told which level it lacks and the command to grant it. Deleting items, changing who owns an item, changing access, repositories, profiles and schedules, and stopping the daemon are yours only. So are secrets: an agent may not read secret configuration or settings, another repository's environment (it runs with its own) or the settings of agent profiles, nor reset a repository to its upstream. A control lease an agent's command takes is its own: another principal cannot renew or release it, though another command line's send takes it over, as a browser tab's or a terminal's control is kept.

The credential is not isolation either: an agent that runs as your Unix user can read your own credential. A sandbox mounts the daemon's socket and the agent's credential file but not yours, and sets `WI_API_SOCKET` to where the socket is mounted. See [Endpoint and credentials](../../reference/api/#endpoint-and-credentials).
