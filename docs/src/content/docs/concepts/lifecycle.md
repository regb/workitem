---
title: Lifecycle
description: Use backlog, active, and closed states deliberately, and express waiting with blockers.
---

A bounded task moves through one lifecycle state at a time. Close it once its objective is settled; keep unrelated follow-ups in new items.

| State | Meaning |
| --- | --- |
| `backlog` | Captured work that holds no workspace or agent resources |
| `active` | Work that holds its workspace and primary agent |
| `closed` | Retained history; resources released |

The lifecycle describes resources, not whether you can make progress. "Waiting on a review" is [waiting](#waiting), not a separate state. How the work ended is a close **resolution**, independent of the item being closed:

| Resolution | Meaning |
| --- | --- |
| `completed` | The objective was met (default) |
| `abandoned` | The work stopped without meeting its objective |
| `superseded` | Another item replaced it |

`wi list --closed` shows the resolution in its own column. The lifecycle does not describe whether an agent process is alive or whether a worktree has changes.

## Normal transitions

```text
new -> backlog
backlog <-> active
backlog or active -> closed (with a resolution)
closed -> backlog (reopen)
```

## Waiting

An item waits until something happens. Underneath, it holds a list of typed **blockers**; an item is waiting while at least one is open. Waiting never changes lifecycle or resources. Active waiting items are listed under WAITING.

| Kind | Example | Cleared |
| --- | --- | --- |
| time (snooze) | `wi wait --until tomorrow` | By the daemon when the time passes, or when you next prompt the item |
| item | `wi wait --on api-schema` | By the daemon when the other item closes |
| external event | `wi wait --for "PR review"` | Explicitly, or by automation |
| usage limit | Set by the daemon when an agent stops on a usage limit | By the daemon once a usage check shows quota is back, or when you next prompt the item |

```bash
wi wait --until 2026-10-20 auth
wi wait --until 3d --on api-schema auth   # two blockers, one atomic step
wi wait --for "security review" auth
wi wait --clear auth                      # stop waiting
wi wait --on api-schema --then "Rebase on main now that the schema landed" auth
```

Each condition adds one blocker, and the item waits until all are cleared. `wi list` and the picker show what an item waits for after its name in yellow: a compact form such as `until Fri 09:00` or `on api-schema`, or the blocker's label when it has one. `wi next --wait` takes the same conditions, at least one required, before moving on.

A time blocker is a snooze: prompting the item means you decided to work on it, so the snooze ends then too. Item and event waits are facts about the world and survive your prompts. `--until` accepts durations (`90m`, `4h`, `2d`), `tomorrow` or a weekday (`monday`, the next one at 09:00), or a local date or date-time. `wi wait --on -` picks the other item interactively.

The daemon checks blockers every few seconds and records each clearance in the item's event history with its cause (`expired`, `prompted`, `item_closed` with the other item's resolution, `item_missing`, `usage_available`, or `manual`). An expired time blocker stops counting immediately, even before the daemon records it. Item blockers refuse dependency cycles. Closing an item clears its own blockers.

### Follow-up messages

`--then MESSAGE` gives a blocker a follow-up: a message for the item's agent. When the blocker is cleared, by its condition or by you, the follow-up is armed, and once the item no longer waits on any blocker the daemon sends it as if you had sent it from the dashboard, starting the agent if it is not running. Several follow-ups are sent in the order their blockers cleared, each once.

A follow-up is about the moment the blocker was set. If you prompt the item after that, you have moved on, and it is dropped instead of sent; a time blocker you end by prompting is such a case. Closing the item drops it too, and `wi wait --clear --drop-follow-up` or `wi blocker clear --drop-follow-up` clears blockers without sending theirs. The item's history records each follow-up as `follow_up.sent` or `follow_up.dropped`, with the reason it was dropped.

### Usage limits

When an agent stops because its provider's usage limit is reached, the daemon makes the item wait with a **usage blocker** instead of leaving it as a problem. The blocker names the provider: `claude` for Claude Code, or for Pi the provider of its model (`openai-codex/...` is `codex`). `wi list` shows it as `usage, resets 15:00`.

The daemon clears it once quota is back. With the [usage plugin](../../reference/configuration/#plugins) reporting the provider, the blocker's time is the reset of the provider's exhausted windows (the last one, when several are exhausted); at that time the daemon checks usage again and clears the blocker only if the check shows quota. If quota is still exhausted, it keeps checking every five minutes, and it keeps checking in the background while any item waits on usage, whether or not a client is open. When the usage data shows no exhausted window, the limit is one the plugin does not report, and the blocker is retried after 15 minutes. Without usage data for the provider, it is retried after an hour. Each retry costs at most one refused turn, which sets a new usage blocker.

The usage blocker's follow-up continues the conversation: by default `Continue where you left off.`, set by [`agent.usage_limit_message`](../../reference/configuration/#usage-limits). The agent continues from its conversation and the checkout as they are: a tool call interrupted by the limit may have partly run, and nothing is undone. Clearing the blocker yourself sends the follow-up at once; `--drop-follow-up` does not. Prompting the item ends the wait and drops the follow-up, since you took over. A usage blocker you clear is not set again for the same failure, only for the next one.

### Blockers

`wi blocker` is the primitive behind `wi wait`. Each blocker has an ID, a kind with its condition (a time, another item, a provider's usage limit, or an external event name), an optional short display label (such as `#1234`), optional structured `key=value` metadata, and an optional follow-up. wi stores metadata without interpreting it, so [plugins](../../reference/configuration/#plugins) can find the blockers they understand and clear them:

```bash
wi blocker add --kind external --event "API review" --label "API #42" --meta github.pr=acme/api#42 auth
wi blocker list auth                      # IDs, kinds, reasons, metadata
wi blocker clear --id b1a2b3c4 auth
wi blocker clear --all auth
```

## Plumbing and porcelain

Like Git, Wi has two levels of operations. Plumbing does one thing: change the lifecycle record, claim or release a checkout, start or stop an agent. Porcelain moves an item and its resources together, by performing that plumbing in order. The daemon enforces the lifecycle's requirements in the plumbing itself, so no sequence of calls, from any client, leaves an unsafe state:

| State | Checkout claim | Agent |
| --- | --- | --- |
| `backlog` | none | not running |
| `active` | optional | optional |
| `closed` | none | not running |

```bash
wi state show auth
wi state set backlog auth
```

`wi state set` is the lifecycle plumbing. It changes lifecycle only: it does not start or stop an agent, or claim or release a worktree. A transition whose target state's requirements are not met is refused, naming what is still held, so stop the agent and release the checkout first, or use the porcelain. A closed item returns to work through the backlog. See [Plumbing and porcelain](../../reference/api/#plumbing-and-porcelain) for how each workflow command maps to plumbing.

## Composed workflow commands

Most day-to-day transitions should use the composed commands, the porcelain:

- `wi start` activates backlog work, claims and sets up its checkout, and starts its agent.
- `wi resume` starts an active item's agent without changing lifecycle or waiting.
- `wi shelve` stops using the active resources when safe and returns work to backlog. The item keeps waiting.
- `wi close` shuts down and releases clean resources before closing with a resolution. It refuses a checkout with uncommitted changes; `--commit-changes` (the web client's **Commit and close**) commits them to the item's branch after the agent stops, so reopening the item finds them.
- `wi delete` permanently removes an already closed, fully cleaned item.

If the original objective is still unfinished, reopen the closed item by ID. Use a new item for a different objective:

```bash
wi list --closed --ids
wi reopen <item-id>
```

Reopening retains the item's ID, branch, conversation and history, clears its resolution, and assigns a fresh active slug. Reopening is for going back to the work, so `wi reopen` then starts the item as `wi start` does, recreating its workspace and agent and resuming the conversation; it takes the same `--agent-mode`, `--model`, `--reasoning` and `--force` options (`--force` exceeds the [deep-work capacity](../../reference/configuration/#deep-work-capacity)). `wi reopen --backlog` only returns the item to the backlog, as the lifecycle primitive `wi state set backlog --item <item-id>` does; start it later with `wi start <active-slug>`. If starting fails, the item stays reopened in the backlog.

The daemon runs these operations as jobs, which keep going if the command is interrupted. They can report partial outcomes: if a durable transition succeeds and a later resource operation fails, Wi retains the durable result and tells you what remains.

## Upgrading from the waiting state

Databases created before this lifecycle are upgraded in place on first daemon start: `working` becomes `active`, `archived` becomes `closed`, and each `waiting` item becomes `active` with one external blocker named "Waiting (migrated from the former waiting state)". Existing agent responses are marked read so the upgrade does not surface every waiting item at once. The daemon keeps a byte-for-byte copy of the old database next to it as `wi.db.schema-5.bak`.

## State is not status

Keep these questions separate:

1. Is the task backlog, active, or closed, and is it waiting?
2. Is its agent busy, idle, offline, or in a problem state?
3. Is its worktree absent, clean, changed, or in a problem state?

`wi list` combines those facts without collapsing them into one ambiguous status.
