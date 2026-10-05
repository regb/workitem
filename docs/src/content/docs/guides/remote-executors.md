---
title: Remote executors
description: Run managed-slot agent runtimes on a prepared machine over SSH.
---

A remote executor is a materialization target owned by the coordinator. The work item, source context, lifecycle, labels, and compact event history remain on the coordinator machine. The executor receives Git objects, creates a managed slot, and runs the selected agent there.

Remote executors are not peer wi installations. Their local data root is a worker cache scoped to the coordinator data root. Ordinary supported `wi` commands inside a remote item use the main daemon as their authority.

## Prepare a worker

Install these programs on the worker:

- the same compatible `wi` version
- Git
- tmux
- the selected coding agent and driver dependencies
- the project's build tools

The coordinator does not install or update worker tools. Configure key-based SSH access before registering it.

```toml
[executors.macbook]
destination = "macbook"
wi_command = "/Users/me/go/bin/wi"
shell = "/opt/homebrew/bin/bash"
forward_env = ["TERM", "COLORTERM"]
```

`shell` names an executable on the worker. The allowlisted environment values are copied from the coordinator.

Check the pairing:

```bash
wi executor doctor macbook
```

The check uses SSH batch mode. It verifies tool discovery and starts a temporary session on an isolated tmux server with the worker terminal profile.

## Create and run work

```bash
wi new --executor macbook "Investigate macOS behavior"
wi start investigate-macos-behavior
```

Or create and prompt a headless runtime:

```bash
wi new --executor macbook --agent-mode headless \
  --prompt "Run the test and explain the failure." \
  "Investigate macOS behavior"
```

Only managed-slot work can select an executor. Repository-home work remains local. An existing item can select its future executor before its workspace is first materialized:

```bash
wi executor select --item investigate-macos-behavior macbook
```

The first workspace claim fixes executor selection in this version. Release preserves the selection and claim generation, so it does not turn release into an incomplete move operation. The [workspace move plan](../workspace-moves/) describes the staged path to clean, conversation-aware, and live-agent moves.

The coordinator sends a Git bundle, so the worker does not need access to the repository origin. The bundle contains committed Git state only. If the coordinator checkout is dirty when you create a remote item, `wi` warns that staged, unstaged, and untracked files are not included and names the commit used by the remote workspace. The worker keeps ignored build output in its reusable slot.

## TUI access

A remote TUI runs in a one-pane session on the worker's dedicated tmux server. That tmux instance is a private persistent terminal host. It has no prefix, status line, mouse handling, user bindings, or plugins.

The user-facing session remains on the coordinator's tmux server. It starts with agent and shell windows. The session's default command is a worker shell attachment, so new windows and splits created with normal tmux commands also run shells in the executor worktree.

```text
local tmux client and work-item session
  ├── agent window -> SSH -> worker persistent Pi terminal
  └── shell window -> SSH -> worker persistent shell terminal
```

Each worker terminal is a private one-pane tmux session. The worker creates a fresh persistent shell for every local shell pane. `wi switch` uses local `switch-client`, so local navigation, layouts, paste buffers, and shortcuts remain available. Full-screen agent scrollback belongs to the worker tmux pane, because the local pane only sees the current SSH-attached screen. Use `wi terminal copy-mode --item <item>` or the conditional `prefix+[` binding in the [tmux guide](../tmux/) to navigate that remote history. Switching away leaves the local panes and SSH attachments running. Losing SSH closes the local gateway session in this prototype, but it does not stop the worker runtime; switching to the item again recreates the gateway and reattaches.

The worker tmux server is an implementation detail behind executor terminal operations. The coordinator does not mirror layouts or issue remote tmux navigation commands. Closing the item's terminal removes its private shell sessions; transport loss only detaches them. Headless runtimes do not create tmux resources.

## Authoritative commands on the worker

The coordinator maintains a separate SSH ControlMaster tunnel for each executor. The tunnel reverse-forwards a Unix socket from the worker to the main daemon's restricted executor endpoint. It is independent of terminal attachment, so closing or losing a local gateway does not remove authoritative access for running remote items.

Remote TUI and headless environments receive the forwarded endpoint automatically. `wi list` and constrained `wi new` currently use it. The list reads the main catalog and its daemon projections. Creation produces an absent managed-slot backlog item in the current authoritative repository and selects the same executor for its future workspace. Until remote Git-object upload is implemented, its base must equal the parent item's coordinator-known created-from commit. It rejects newer remote-only commits, `--repo`, `--home`, and `--prompt`.

Navigation runs in the local gateway session, so `wi switch` uses the normal operator endpoint rather than the executor endpoint. Run other lifecycle and runtime operations from the coordinator until the executor endpoint explicitly permits them.

The executor endpoint is separate from the full operator socket and the sandboxed agent endpoint. It permits canonical catalog reads, list observation reads, and constrained item creation. It rejects lifecycle, workspace, runtime, terminal, delete, and shutdown control. If the tunnel is unavailable, supported commands fail instead of silently falling back to the worker cache. A later coordinator interaction with the executor recreates a lost tunnel.

## Status and events

Explicit status, control, and monitor commands refresh a remote item's observations through SSH. List and attention commands use the latest daemon projection instead of opening an SSH connection for every remote item. Compact runtime events are deduplicated into the coordinator event log. Pi conversation content is checkpointed through driver-owned export and import operations when status or control synchronizes the runtime. Diagnostics remain in the worker cache.

A worker connection failure leaves lifecycle unchanged and produces an observation warning. It does not prove that the runtime stopped.

## Returning work

Remote release requires a clean worktree, just like local managed-slot release. The worker exports its Git refs in a bundle and the coordinator imports the implementation branch before releasing the claim. Before materialization, the coordinator also reconciles the worker replica catalog and removes unclaimed replicas that no longer have canonical items. Replicas with live resources are retained and reported.

Uncommitted files are not copied back in this first implementation. Review them through the remote TUI and commit them explicitly before release or merge. `wi merge` uses an internal clean return to a local managed slot before running the normal local integration transaction. That integration step does not make executor selection generally editable.

## Limits

- Worker tools are not provisioned automatically.
- Repository-home checkouts cannot run remotely.
- Conversation checkpoints currently transfer complete native files rather than incremental append ranges.
- Executor operations other than the authority tunnel use short-lived SSH requests.
- The authority tunnel is re-established by coordinator interactions rather than by a separate always-running reconnect supervisor.

Use `wi executor shutdown --force <name>` to stop resources associated with the current coordinator data root on a worker. It does not affect the worker's ordinary tmux server or another coordinator's executor state.
