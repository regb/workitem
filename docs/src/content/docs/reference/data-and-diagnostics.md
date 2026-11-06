---
title: Data and diagnostics
description: Locate wi data, inspect the daemon, and understand what to back up.
---

Use `wi info` instead of guessing paths:

```bash
wi info
wi info --json
wi info data-root
wi info operator-socket
```

`wi info` asks the daemon (`SystemService.GetInfo`) for its version, API version, data root and default agent profile; `operator-socket` is the socket the command reached it on. Path resolution follows the XDG base directory specification; [Endpoints](../api/#endpoints) lists the daemon's sockets.

## What to back up

Back up the data root reported by `wi info data-root`. It contains:

- `wi.db`, which stores the repository registry, work-item metadata, lifecycle, labels, event history, workspace and agent records, and the summaries of recent [jobs](../api/#conventions)
- `repositories/<id>.git`, the independent Git stores containing native remote/branch-tracking config, integration and task branches, and pinned created-from commits
- managed worktrees, including any uncommitted state
- each item's `DESCRIPTION.md`
- driver-owned native conversations, including Pi JSONL files

Git owns repository objects, refs, remote URLs, and branch tracking. Their current values are read from Git; timestamped observations and historical operation snapshots in `wi.db` are not substitute authority. Descriptions and native conversations are also not duplicated in the database. Back up the complete data root rather than selecting individual files.

Logs, control sockets, cached helper files, and terminal process records live under the XDG state, runtime, or cache directories. They are operational files, not work-item history.

Do not edit `wi.db` or native conversation files manually. For a consistent backup, stop writers and keep the executable matching the database checkpoint. Moving a data root also requires repairing concrete Git worktree paths; copying `wi.db` alone is insufficient.

The current database uses schema 8. The daemon upgrades schema 5, 6, or 7 on startup after publishing a complete, private, synced snapshot at `wi.db.schema-N.bak`. A backup left by an earlier attempt must match the current pre-upgrade database; incomplete, stale, symlinked, or non-private backups block the upgrade and remain untouched. Preserve such a file elsewhere before retrying. Other older schemas still require an explicit offline migration.

Keep the old executable until the transition is complete. Stop the old daemon with its matching build before starting the new one. Clients check the daemon's API version with `SystemService.GetInfo` and refuse a different major version; builds from before the API cannot talk to an API daemon, and an API client cannot talk to an older daemon, so stop an older daemon with its own build.

## Observation freshness

The daemon checks active worktrees when it starts, every 30 seconds in the background, and when a client asks: `ListItems` with `inspect` re-observes agents, worktrees and conversations before answering, and otherwise the daemon answers from its latest observations. `wi workspace status` reads the selected checkout's state from the daemon.

The daemon does not watch every filesystem change. A cached observation can therefore remain stale until the next reconciliation. Clients learn about changes the daemon does see from its change feed (`EventService.WatchChanges`) instead of polling. An open picker is also a frozen snapshot; close and reopen it to refresh.

## Diagnose the local daemon

Ordinary commands start the daemon when needed, unless a service manager owns
it (see [Managed daemon](../configuration/#managed-daemon)):

```bash
wi daemon status
wi daemon doctor
```

`status` reports the daemon's health (`SystemService.GetHealth`); `doctor` runs its diagnostics (`RunDiagnostics`): the database's schema and event sequence, and the observation projection's warnings. Run the daemon in the foreground when startup itself is failing:

```bash
wi daemon serve
```

If the daemon is unavailable, commands fail without changing the item: no command falls back to doing the work itself. The daemon writes its log to `daemon.log` under its XDG state directory. See [Troubleshooting](../troubleshooting/) for common recovery steps.

## Shut everything down

```bash
wi shutdown
```

Shutdown asks the daemon (`SystemService.Shutdown`) to stop tracked agents, close wi-owned tmux sessions, and stop itself last; the command follows its job until the daemon goes away. It does not stop the tmux server or search for processes by name. A service-managed daemon stops its agents and terminals and keeps running.

Busy or uncontrollable agents remain running unless you request verified force cleanup:

```bash
wi shutdown --force
```

Force cleanup stops a process only after the daemon verifies that it still belongs to the recorded agent.

## Privacy and access

Data directories and daemon endpoints are restricted to the current user.

Each daemon socket has its own access level, checked per method; the peer must run as the same user. The separate agent-tool socket grants fewer methods than the operator socket. Sandboxed agents can read items, file safe managed-slot backlog work, and add notes and attachments, but they cannot control lifecycle, workspaces, agents, terminals, deletion, or shutdown. See [Endpoints](../api/#endpoints).

The database stores compact agent events, not complete prompts, responses, text deltas, tool arguments, paths, or error text. Full conversation content remains in driver-owned native files.
