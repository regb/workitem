---
title: Lifecycle
description: Use backlog, working, waiting, and archived states deliberately.
---

Every item has one durable lifecycle state.

| State | Meaning |
| --- | --- |
| `backlog` | Captured work outside the active working set |
| `working` | Active and actionable work |
| `waiting` | Active work intentionally blocked on an external response |
| `archived` | Inactive retained history |

The lifecycle says what the task means to you. It does not describe whether an agent process is alive or whether a worktree has changes.

## Normal transitions

```text
new -> backlog
backlog -> working
working <-> waiting
working or waiting -> backlog
backlog, working, or waiting -> archived
archived -> backlog
```

## Primitive state changes

```bash
wi state show auth
wi state set waiting auth
```

`wi state set` changes lifecycle only. It does not start or stop an agent, create or release a worktree, or close tmux. Use it when you want that exact narrow operation.

## Composed workflow commands

Most day-to-day transitions should use the safer composed commands:

- `wi start` activates backlog work and ensures its workspace and agent.
- `wi resume` returns waiting work to working and ensures its runtime.
- `wi shelve` stops using the active resources when safe and returns work to backlog.
- `wi archive` shuts down and releases clean resources before archiving.
- `wi delete` permanently removes an already archived, fully cleaned item.

These commands can report partial outcomes. If a durable transition succeeds and a later resource operation fails, `wi` retains the durable result and tells you what remains.

## State is not status

Keep these questions separate:

1. Is the task backlog, working, waiting, or archived?
2. Is its agent busy, idle, offline, or in a problem state?
3. Is its worktree absent, clean, changed, or in a problem state?

`wi list` combines those facts without collapsing them into one ambiguous status.
