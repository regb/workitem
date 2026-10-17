# Lifecycle, waiting, attention, runtime control, and plugins

This records the design decisions behind wi's work-item lifecycle, waiting,
attention, runtime control, and plugin model, and the questions still open.

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

## Runtime channels and controllers

A runtime exposes channels, reported by its driver per runtime instance in its
ready capabilities (`control: structured | emulated`):

| Runtime | Observe | Control | Terminal |
| --- | --- | --- | --- |
| Pi headless | yes | structured, atomic idle guard | no |
| Pi TUI | yes | structured (bridge), atomic idle guard | yes |
| Claude headless | yes | structured, observed idle only | no |
| Claude TUI | yes | emulated (tmux keystrokes) | yes |

Clients declare the channel they need: a browser controller needs structured
control, a terminal controller needs the terminal channel, observers need only
observe.

Each agent has at most one controller and any number of observers. The
controller lease lives in coordinator memory (a rebuildable observation, lost
on restart):

- A vacant lease is invisible: the first sender takes it.
- If the runtime lacks the needed channel and is idle, wi replaces it on the
  same conversation without asking (browser: keystroke TUI or offline runtime
  -> headless; terminal: headless -> TUI).
- Displacing another controller, or aborting a turn in flight, requires
  explicit takeover, which aborts the turn and replaces the runtime when
  needed.
- Browser leases are renewable and expire; terminal control is implicit while
  a tmux client is attached to the item's session.
- While a browser controls a TUI runtime, wi disables input to the agent pane
  (`select-pane -d`); terminals keep rendering. `wi switch` enters read-only
  and `wi switch --take` takes over. When no lease is live, the next status
  check re-enables pane input.
- Enforcement covers clients that go through wi; a raw `tmux attach` can still
  type into an enabled pane.

Each lease has a generation ID. Host acquisition uses an expected lease ID,
including the vacant state, so a stale plan cannot silently replace a different
controller. Renewals retain the ID; replacement changes it. Denied acquisitions
perform no abort or runtime replacement.

Structured sends also check the controller lease. Browser callers supply the
holder, client ID, and lease ID returned by acquisition. A plain CLI send uses
the shared terminal identity and holds a vacant lease only for delivery; it
cannot send through a browser's lease. Browser leases default to 45 seconds.

Handoff, structured delivery, and pane repair use a separate per-item controller
lock, always acquired before resource operation locks. Read-only terminal entry
must successfully disable pane input before attaching. Input-control failures
are reported rather than presenting a writable terminal as read-only.

The browser client that motivates the lease (a web UI) is a separate change
built on this API.

## Plugins

Native hooks and executable plugins use the [same catalog and settings model](unified-plugins.md).
Direnv supplies native preparation hooks. GitHub supplies declared executable
commands, repository-scoped polling jobs, and static instruction assets. Both
use registered options in their `[plugins.NAME]` table and daemon-owned repository overrides.

Plugins are fully trusted operator code and use Wi’s normal CLI/API. There is
no per-plugin permission broker. Jobs receive a fresh effective settings snapshot
per repository, and static instruction inspection does not launch code.

## Upgrading stored data

Schema 7 replaces the former `working | waiting | archived` lifecycle in place
on first daemon start, keeping a complete pre-upgrade snapshot next to the database
(`wi.db.schema-N.bak`): `working` becomes `active`, `archived` becomes
`closed`, and a `waiting` item becomes `active` with one external blocker
named `waiting`; existing responses are marked read so the upgrade does not
surface every waiting item at once. (Schema 6 was a short-lived intermediate
format with free-form blocker reasons; schema 7 converts those to event
names. Schema 8 renames stored repository overrides from `extensions.NAME` to
`plugins.NAME`.) A daemon from before schema 6 rejects requests from newer clients, so
the first upgrade needs a manual `wi daemon stop` with the old build.

The backup is written to a private temporary file, synced, and published
atomically without replacing an existing backup. The directory is synced
before migration starts. On retry, an existing backup must be a private
regular file matching the current pre-upgrade snapshot exactly. An incomplete,
unsafe, or stale backup blocks migration and remains untouched. Preserve it
elsewhere before retrying; Wi never silently overwrites recovery data.

## Open questions

1. Terminal presence is coarse: any attached tmux client counts as the
   controller, and one shared `terminal` identity covers all tmux clients.
   Per-client identity and detach hooks would release terminal control
   promptly.
2. Pane input is re-enabled lazily, on the next status check, after a browser
   lease expires. A daemon-side expiry hook would avoid a briefly frozen pane.
3. Claude headless has no atomic idle guard, so idle-only prompts check
   observed status and a race remains.
4. Interactive approvals and questions in headless mode are not modeled as a
   capability yet; this decides how far headless-first goes, especially for
   Claude.
5. A wi-rendered terminal client over the structured channel would let
   terminal users control headless runtimes without swapping to the native
   TUI.
6. Waiting items with an unread response join the queue at their natural rank;
   whether they should rank first is a strategy choice.
7. Event names: `work_item.archived` is now `work_item.closed`; older history
   keeps its original names.
8. The restricted agent endpoint rejects blocker changes, so sandboxed agents
   cannot make their own item wait on a pull request; allowing an agent to add
   blockers to its own item only is undecided.
