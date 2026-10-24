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

- `wi.db`, which stores the repository registry, work-item metadata, lifecycle, labels, event history, and workspace and runtime records
- `repositories/<id>.git`, the independent Git stores containing native remote/branch-tracking config, integration and task branches, and pinned created-from commits
- managed worktrees, including any uncommitted state
- each item's `DESCRIPTION.md`
- driver-owned native conversations, including Pi JSONL files

Git owns repository objects, refs, remote URLs, and branch tracking. Their current values are read from Git; timestamped observations and historical operation snapshots in `wi.db` are not substitute authority. Descriptions and native conversations are also not duplicated in the database. Back up the complete data root rather than selecting individual files.

Logs, control sockets, cached helper files, and terminal process records live under the XDG state, runtime, or cache directories. They are operational files, not work-item history.

Do not edit `wi.db` or native conversation files manually. For a consistent backup, stop writers and keep the executable matching the database checkpoint. Moving a data root also requires repairing concrete Git worktree paths; copying `wi.db` alone is insufficient.

The current database uses schema 8. The daemon upgrades schema 5, 6, or 7 on startup after publishing a complete, private, synced snapshot at `wi.db.schema-N.bak`. A backup left by an earlier attempt must match the current pre-upgrade database; incomplete, stale, symlinked, or non-private backups block the upgrade and remain untouched. Preserve such a file elsewhere before retrying. Other older schemas still require an explicit offline migration.

Keep the old executable until the transition is complete. Stop the old daemon with its matching build before starting the schema-7 daemon. Daemon replacement checks the advertised database schema, and daemon control requests must identify a matching supported schema. Older binaries cannot stop a newer writer. Health reads and fenced runtime events remain schema-independent.

## Observation freshness

`wi` checks active worktrees when the daemon starts, every 30 seconds in the background, and before commands that need a fresh attention snapshot. Commands such as `wi list`, `wi switch`, `wi next`, and `wi attention queue` refresh worktree observations before ranking items. `wi workspace status` inspects the selected checkout directly.

The daemon does not watch every filesystem change. A cached observation read by a low-level client can therefore remain stale until the next reconciliation. An open picker is also a frozen snapshot; close and reopen it to refresh.

## Diagnose the local daemon

Ordinary commands start the daemon when needed, unless a service manager owns
it (see [Managed daemon](../configuration/#managed-daemon)):

```bash
wi daemon status
wi daemon doctor
```

`doctor` checks connectivity, database metadata, and native conversation indexing. Run the daemon in the foreground when startup itself is failing:

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

The database stores compact runtime events, not complete prompts, responses, text deltas, tool arguments, paths, or error text. Full conversation content remains in driver-owned native files.
