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

```bash
wi wait --until 2026-10-20 auth
wi wait --until 3d --on api-schema auth   # two blockers, one atomic step
wi wait --for "security review" auth
wi wait --clear auth                      # stop waiting
```

Each condition adds one blocker, and the item waits until all are cleared. `wi list` and the picker show what an item waits for after its name in yellow: a compact form such as `until Fri 09:00` or `on api-schema`, or the blocker's label when it has one. `wi next --wait` takes the same conditions, at least one required, before moving on.

A time blocker is a snooze: prompting the item means you decided to work on it, so the snooze ends then too. Item and event waits are facts about the world and survive your prompts. `--until` accepts durations (`90m`, `4h`, `2d`), `tomorrow` or a weekday (`monday`, the next one at 09:00), or a local date or date-time. `wi wait --on -` picks the other item interactively.

The daemon checks blockers every few seconds and records each clearance in the item's event history with its cause (`expired`, `prompted`, `item_closed` with the other item's resolution, or `item_missing`). An expired time blocker stops counting immediately, even before the daemon records it. Item blockers refuse dependency cycles. Closing an item clears its own blockers.

### Blockers

`wi blocker` is the primitive behind `wi wait`. Each blocker has an ID, a kind with its condition (a time, another item, or an external event name), an optional short display label (such as `#1234`), and optional structured `key=value` metadata. wi stores metadata without interpreting it, so [plugins](../../reference/configuration/#plugins) can find the blockers they understand and clear them:

```bash
wi blocker add --kind external --event "API review" --label "API #42" --meta github.pr=acme/api#42 auth
wi blocker list auth                      # IDs, kinds, reasons, metadata
wi blocker clear --id b1a2b3c4 auth
wi blocker clear --all auth
```

## Primitive state changes

```bash
wi state show auth
wi state set backlog auth
```

`wi state set` changes lifecycle only. It does not start or stop an agent, create or release a worktree, or close tmux. Use it when you want that exact narrow operation.

## Composed workflow commands

Most day-to-day transitions should use the safer composed commands:

- `wi start` activates backlog work and ensures its workspace and agent.
- `wi resume` ensures an active item's runtime without changing lifecycle or waiting.
- `wi shelve` stops using the active resources when safe and returns work to backlog. The item keeps waiting.
- `wi close` shuts down and releases clean resources before closing with a resolution. It refuses a checkout with uncommitted changes; `--commit-changes` commits them to the item's branch after the agent stops, so reopening the item finds them.
- `wi delete` permanently removes an already closed, fully cleaned item.

If the original objective is still unfinished, reopen the closed item by ID. Use a new item for a different objective:

```bash
wi list --closed --ids
wi reopen <item-id>
```

Reopening retains the item's ID, branch, conversation and history, clears its resolution, and assigns a fresh active slug. Reopening is for going back to the work, so `wi reopen` then starts the item as `wi start` does, recreating its workspace and agent and resuming the conversation; it takes the same `--agent-mode`, `--model`, `--reasoning` and `--force` options. `wi reopen --backlog` only returns the item to the backlog, as the lifecycle primitive `wi state set backlog --item <item-id>` does; start it later with `wi start <active-slug>`. If starting fails, the item stays reopened in the backlog.

These commands can report partial outcomes. If a durable transition succeeds and a later resource operation fails, `wi` retains the durable result and tells you what remains.

## Upgrading from the waiting state

Databases created before this lifecycle are upgraded in place on first daemon start: `working` becomes `active`, `archived` becomes `closed`, and each `waiting` item becomes `active` with one external blocker named "Waiting (migrated from the former waiting state)". Existing agent responses are marked read so the upgrade does not surface every waiting item at once. The daemon keeps a byte-for-byte copy of the old database next to it as `wi.db.schema-5.bak`.

## State is not status

Keep these questions separate:

1. Is the task backlog, active, or closed, and is it waiting?
2. Is its agent busy, idle, offline, or in a problem state?
3. Is its worktree absent, clean, changed, or in a problem state?

`wi list` combines those facts without collapsing them into one ambiguous status.
