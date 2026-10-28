---
title: Attention
description: Understand how wi identifies work that needs a human decision.
---

Attention answers a practical question: which active item should you inspect next?

An item needs attention when it is `active`, its agent is idle, neither agent nor worktree observation reports a problem, and it is not [waiting](../lifecycle/#waiting). Busy work appears in progress.

## Three tiers

| Tier | Contents | How it is used |
| --- | --- | --- |
| Queue (NEEDS ATTENTION) | Active, idle items that are not waiting | Ranked; `wi next` walks it |
| WAITING | Active items with an open blocker | Outside any ranking; review it periodically, like a GTD waiting-for list |
| Backlog | Items holding no resources | Started deliberately |

Defer and waiting are different tools. Defer means "later, among things I can act on now" and only reorders the queue; your next prompt clears it. Waiting means "not actionable until something happens" and removes the item from the queue. A one-day snooze is `wi wait --until 1d`, not a defer.

## Read and unread

An agent response you have not read is **unread**. Unread titles are bold in `wi list` and the picker, like unread mail. Read state is tracked per item:

- On an item that is not waiting, unread only changes the display. Reading never moves the item in the queue.
- On a waiting item, an unread response puts the item back in NEEDS ATTENTION. Reading it returns the item to WAITING. This covers follow-up prompts sent to an item that waits for a review: the answer surfaces without clearing what it waits for.

The picker shows waiting alongside the agent's status, including busy items and waiting items with unread responses. The item name and blocker label share the available column width, borrowing space from each other when one is short. Only when both cannot fit does the picker truncate them, keeping a visible condition label. Conditions appear in yellow after `·`, without a redundant `waiting:` prefix. The complete condition remains searchable.

`wi switch`, the picker, and `wi next` mark the item read; other clients record the same fact with `wi attention read`. So does `wi wait`, because parking an item is a decision made after seeing it. If you opened something without really reading it, mark it unread again:

```bash
wi attention unread auth
wi attention read auth
```

A tmux client left attached to a pane never marks anything read while it stays; wi cannot tell whether you are looking at it. Switching away from an item's session with `wi switch`, the picker, or `wi next` marks the item you leave read, unless you marked it unread: a reply that finished while you watched is not news. A turn still running ends after that mark, so its reply is still unread.

Aborting a turn, with the web client's stop button, `wi agent control abort`, or the agent's own interrupt key, ends it normally: the agent is idle, not a problem, and the item comes back to NEEDS ATTENTION with an unread reply. The conversation marks the stopped reply as aborted, and the next message continues as usual.

An intermediate assistant or tool error does not end an active agent loop. Pi's hooks keep the item busy through retries, recovery, compaction, and queued work. `agent_settled` records completion: successful settlement is idle, and failed settlement is a problem. Pi also reports native current state from its selected branch. Moving with `/tree` to an earlier successful or empty branch can therefore clear an abandoned failure without another prompt; this is a state observation, not a new completion. Startup, compaction, and reload use the same reconciliation. Current-owner hooks take priority over intermediate native messages while the verified runtime is online. Offline or fatal runtimes remain problems. There is no error-recovery timeout, and live active loops are exempt from native-history staleness checks. These observations do not change the item's lifecycle.

Older runtimes without live hooks still use native-history inference. Installing Wi does not inject hooks into a running Pi process; explicitly restart that item’s agent to load the updated driver.

## Inspect the queue

```bash
wi attention queue
wi attention activity auth
wi list
```

The activity view reads the daemon's compact projection of prompt, completion, defer, read, and unread timestamps. It does not scan event history or inspect a native conversation. Read/unread and defer commands return this snapshot in JSON; navigation records the read marker without fetching an activity result. Marker writes update their projection in the same transaction as their history event.

The `attention.priority` setting selects the ranking strategy; see [Configuration](../../reference/configuration/#attention-ranking).

## Move through ready work

```bash
wi next
```

`next` selects from the ranked attention queue and enters the selected item's TUI. It wraps when it reaches the end.

Common variants combine an explicit action on the current item with selection of the next one:

```bash
wi next --defer                      # move the current ready item behind the others
wi next --wait --for "PR review"     # park the current item until a review lands
wi next --close                      # close safely as completed, then enter a distinct successor
```

These actions preserve successful partial outcomes. For example, a recorded defer remains even if entering the next tmux session fails.

## Use the queue from scripts

```bash
wi --json attention queue
wi --json next
```

JSON mode returns the same ranked queue used by interactive commands. It may select and prepare an item, but it never attaches a terminal.

## Filter the working set

Attention uses the same label rules as `wi list`:

```bash
wi next --label +backend --label -blocked
```

Rules can come from configuration, `WI_LIST_LABELS`, and repeatable command-line flags.
