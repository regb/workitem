# Lifecycle, waiting, and attention

This records the design decisions behind wi's work-item lifecycle, waiting,
and attention, and the questions still open.

## Lifecycle describes resources

`backlog | active | closed`. Backlog holds no resources, active holds the
workspace and primary agent, closed retains history. Closing records a
resolution, `completed | abandoned | superseded`, because whether work is
finished is independent of whether it is closed. Waiting is not a lifecycle
state: an item waits through blockers.

## Waiting and typed blockers

A blocker is durable operator intent on a backlog or active item. Several can
be open; the item waits until all are cleared. Each blocker has an ID, a kind
with a self-describing condition, an optional short display label (such as
`#1234`), and optional structured `key=value` metadata that wi stores without
interpreting, so automation can find and clear the blockers it created.
Blockers carry no free-form reason and clearances carry no note: the condition
says why an item waits, and the clearance cause says why it stopped.

| Kind | Condition | Cleared |
| --- | --- | --- |
| time (snooze) | a time | when it passes, or when the operator next prompts the item |
| item | another item | when that item closes or no longer exists |
| external | an event name | explicitly, or by automation |

A snooze means "not now"; prompting the item means the operator decided it is
now. Item and external blockers are facts about the world and survive prompts.

The coordinator sweeps blockers every 5 seconds over persisted state (no
timers). Each clearance is an ordinary item event, `blocker.cleared`, with its
cause (`expired`, `prompted`, `item_closed` with the other item's resolution,
`item_missing`, `manual`, `closed`), so history shows when and why an item
stopped waiting even after daemon downtime. An expired time blocker stops
counting immediately. Item blockers refuse dependency cycles. Closing an item
clears its own blockers.

`wi wait --until|--on|--for` adds one blocker per flag, atomically when
combined, and `--clear` clears all. `wi blocker add|list|clear` is the
primitive: one typed blocker with an optional label and metadata, listing with
IDs, and clearing one by ID or all.

## Attention tiers and read state

- Queue (NEEDS ATTENTION): active, idle items that are not waiting, ranked.
  Defer reorders within it.
- WAITING: active items with an open blocker, outside the ranking, reviewed
  periodically like a GTD waiting-for list. A waiting item whose agent is busy
  is listed IN PROGRESS.
- Backlog.

Read state is explicit operator intent recorded as `attention.read` and
`attention.unread` events. A response is unread when the last completed turn
or the last unread mark is newer than the last read mark.

- Item not waiting: unread is a display marker (bold title) only.
- Waiting item: unread puts it in the queue; reading returns it to WAITING.

Wi's own clients mark read (`wi switch`, the picker, `wi next`), and
`wi attention unread` corrects an accidental open; an attached tmux pane never
marks read. Waiting also marks the item read: the decision to park it is made
after seeing it.

## Upgrading stored data

Schema 7 replaces the former `working | waiting | archived` lifecycle in place
on first daemon start, keeping a complete pre-upgrade snapshot next to the database
(`wi.db.schema-N.bak`): `working` becomes `active`, `archived` becomes
`closed`, and a `waiting` item becomes `active` with one external blocker
named `waiting`; existing responses are marked read so the upgrade does not
surface every waiting item at once. (Schema 6 was a short-lived intermediate
format with free-form blocker reasons; schema 7 converts those to event
names.) A daemon from before schema 6 rejects requests from newer clients, so
the first upgrade needs a manual `wi daemon stop` with the old build.

The backup is written to a private temporary file, synced, and published
atomically without replacing an existing backup. The directory is synced
before migration starts. On retry, an existing backup must be a private
regular file matching the current pre-upgrade snapshot exactly. An incomplete,
unsafe, or stale backup blocks migration and remains untouched. Preserve it
elsewhere before retrying; Wi never silently overwrites recovery data.

## Open questions

1. Waiting items with an unread response join the queue at their natural rank;
   whether they should rank first is a strategy choice.
2. Event names: `work_item.archived` is now `work_item.closed`; older history
   keeps its original names.
