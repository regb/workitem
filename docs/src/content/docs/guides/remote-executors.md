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
tmux_config = "/home/me/.tmux.conf"
shell_config = "/home/me/.config/wi/executor.bashrc"
forward_env = ["TERM", "COLORTERM"]
```

The configuration and shell files are read on the coordinator, then copied to worker-owned state. Keep the shell file portable. A normal interactive shell file that invokes machine-specific package managers or absolute paths may fail on another operating system.

Check the pairing:

```bash
wi executor doctor macbook
```

The check uses SSH batch mode. It verifies tool discovery and starts a temporary session on an isolated tmux server with the forwarded terminal profile.

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

Only managed-slot work can select an executor. Repository-home work remains local. An existing item with an absent workspace and no native conversation can be assigned before it starts:

```bash
wi executor assign --item investigate-macos-behavior macbook
```

Placement cannot move after native conversation work begins in this version.

The coordinator sends a Git bundle, so the worker does not need access to the repository origin. The worker keeps ignored build output in its reusable slot.

## TUI access

A remote TUI runs inside the worker's dedicated tmux server. When invoked from local tmux, `wi switch` and `wi terminal enter` detach the selected local client and replace it with the SSH attachment. The remote server owns the terminal directly, so there is no local gateway session and no nested tmux. The detached local session keeps running.

```text
terminal
  -> SSH
     -> worker-owned tmux server
        -> Pi TUI
```

Detaching from the remote tmux session ends SSH and returns to the shell that launched the local tmux client. Reattach the still-running local session normally. Losing SSH does not stop the worker's tmux session or runtime. The dedicated server uses the forwarded tmux layout, shell executable, shell startup file, and allowlisted environment.

Headless runtimes do not create tmux resources.

## Authoritative commands on the worker

The coordinator maintains a separate SSH ControlMaster tunnel for each executor. The tunnel reverse-forwards a Unix socket from the worker to the main daemon's restricted executor endpoint. It is independent of terminal attachment, so detaching remote tmux does not remove authoritative access for running remote items.

Remote TUI and headless environments receive the forwarded endpoint automatically. These commands currently use it:

```bash
wi list
wi new "Follow-up discovered on the worker"
wi switch
```

`wi list` reads the main catalog and its daemon projections. `wi new` creates an absent managed-slot backlog item in the current authoritative repository and assigns it to the same executor. Remote authoritative creation rejects `--repo`, `--home`, and `--prompt`.

`wi switch` opens the canonical picker. After selection it records a one-shot access handoff on the main daemon and detaches the remote tmux client. The local direct-attachment process consumes that handoff and enters the selected item. A remote target attaches through its executor; a local target attaches to local tmux. This does not nest tmux servers. Picker previews are disabled on the executor because unrestricted descriptions are not exposed through the executor endpoint.

Run other lifecycle and runtime operations from the coordinator until the executor endpoint explicitly permits them.

The executor endpoint is separate from the full operator socket and the sandboxed agent endpoint. It permits canonical catalog reads, list observation reads, one-shot switch handoffs, and constrained item creation. It rejects lifecycle, workspace, runtime, terminal, delete, and shutdown control. If the tunnel is unavailable, supported commands fail instead of silently falling back to the worker cache. A later coordinator interaction with the executor recreates a lost tunnel.

## Status and events

Explicit status, control, and monitor commands refresh a remote item's observations through SSH. List and attention commands use the latest daemon projection instead of opening an SSH connection for every remote item. Compact runtime events are deduplicated into the coordinator event log. Pi conversation content is checkpointed through driver-owned export and import operations when status or control synchronizes the runtime. Diagnostics remain in the worker cache.

A worker connection failure leaves lifecycle unchanged and produces an observation warning. It does not prove that the runtime stopped.

## Returning work

Remote release requires a clean worktree, just like local managed-slot release. The worker exports its Git refs in a bundle and the coordinator imports the implementation branch before releasing the claim.

Uncommitted files are not copied back in this first implementation. Review them through the remote TUI and commit them explicitly before release or merge. `wi merge` returns a clean remote branch to a local managed slot before running the normal local integration transaction.

## Limits

- Worker tools are not provisioned automatically.
- Repository-home checkouts cannot run remotely.
- Conversation checkpoints currently transfer complete native files rather than incremental append ranges.
- Executor operations other than the authority tunnel use short-lived SSH requests.
- The authority tunnel is re-established by coordinator interactions rather than by a separate always-running reconnect supervisor.

Use `wi executor shutdown --force <name>` to stop resources associated with the current coordinator data root on a worker. It does not affect the worker's ordinary tmux server or another coordinator's executor state.
