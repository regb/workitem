---
title: Data and diagnostics
description: Locate wi data, inspect the daemon, and understand what to back up.
---

Use `wi info` instead of guessing paths:

```bash
wi info
wi info --json
wi info agent-socket
```

Path resolution follows the XDG base directory specification.

## What to back up

Back up the data root reported by `wi info data-root`. It contains:

- `wi.db`, which stores work-item metadata, lifecycle, labels, event history, and workspace and runtime records
- each item's `DESCRIPTION.md`
- Pi conversation JSONL files written by Pi

The description and Pi conversation files are not duplicated in the database. Back up the complete data root rather than selecting individual files.

Logs, control sockets, cached helper files, and terminal process records live under the XDG state, runtime, or cache directories. They are operational files, not work-item history.

Do not edit `wi.db` or Pi JSONL files manually.

## Observation freshness

`wi` checks active worktrees when the daemon starts, every 30 seconds in the background, and before commands that need a fresh attention snapshot. Commands such as `wi list`, `wi switch`, `wi next`, and `wi attention queue` refresh worktree observations before ranking items. `wi workspace status` inspects the selected checkout directly.

The daemon does not watch every filesystem change. A cached observation read by a low-level client can therefore remain stale until the next reconciliation. An open picker is also a frozen snapshot; close and reopen it to refresh.

## Diagnose the local daemon

Ordinary commands start the daemon when needed:

```bash
wi daemon status
wi daemon doctor
```

`doctor` checks connectivity, database metadata, and Pi session indexing. Run the daemon in the foreground when startup itself is failing:

```bash
wi daemon serve
```

If the daemon is unavailable, commands fail without changing the item. See [Troubleshooting](../troubleshooting/) for common recovery steps.

## Shut everything down

```bash
wi shutdown
```

Shutdown stops tracked agent runtimes, closes wi-owned tmux sessions, and stops the daemon last. It does not stop the tmux server or search for processes by name.

Busy or uncontrollable agents remain running unless you request verified force cleanup:

```bash
wi shutdown --force
```

Force cleanup stops a process only after `wi` verifies that it still belongs to the recorded runtime.

## Privacy and access

Data directories and daemon endpoints are restricted to the current user.

The separate agent-tool socket grants fewer commands than the operator socket. Sandboxed agents can file safe managed-slot backlog work, but they cannot control lifecycle, workspaces, runtimes, terminals, deletion, or shutdown.

The database stores compact runtime events, not complete prompts, responses, text deltas, tool arguments, paths, or error text. Full conversation content remains in Pi's native session files.
