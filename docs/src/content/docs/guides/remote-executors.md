---
title: Remote executors
description: Keep Pi on the Wi host while running workspace tools on another machine over SSH.
---

A remote executor adds a managed workspace on another machine. The Wi host still owns the work item, coordinator daemon, native Pi conversation, Pi process, and user-facing tmux session. Pi's workspace-scoped tools execute against the remote claim.

```text
Wi host
  coordinator + Pi + conversation + tmux
    -> multiplexed SSH requests
       -> claim-fenced workspace on executor
```

This is useful when a project needs a different operating system, architecture, toolchain, or more compute. Ordinary remote access to Wi does not require an executor: install Wi on an always-on host and attach to its tmux sessions over SSH instead.

## Prepare an executor

Install these programs on the executor:

- the same compatible `wi` version
- Git
- the project's build tools

The bundled Pi driver runs only on the Wi host. Pi, its credentials, its user configuration, and the executor's `~/.pi` are not required on the executor. Configure key-based SSH access, then register the machine:

```toml
[executors.macbook]
destination = "macbook"
wi_command = "/Users/me/go/bin/wi"
```

`destination` is an SSH destination or host alias. `wi_command` is the executor-local Wi executable. Check the connection and versions with:

```bash
wi executor doctor macbook
```

Wi does not provision or update executor software.

## Create and run work

```bash
wi new --executor macbook "Investigate macOS behavior"
wi start investigate-macos-behavior
```

Only managed-slot workspaces can select a remote executor. Repository-home workspaces belong to the Wi host and remain local. Before an item is materialized, you can change its selected executor:

```bash
wi executor select --item investigate-macos-behavior macbook
```

The first workspace claim fixes executor selection in this version.

The Wi host sends committed Git state in a bundle, so the executor does not need repository-origin credentials. Staged, unstaged, and untracked files in the host checkout are not transferred. Wi warns when a dirty source checkout matters.

## Runtime and tool behavior

Pi starts on the Wi host and uses the host's Pi settings, packages, extensions, skills, prompts, credentials, themes, and MCP configuration. Wi loads an internal extension that redirects Pi's workspace-scoped `bash`, `read`, `write`, `edit`, `grep`, `find`, and `ls` tools to the claimed executor workspace.

The executor's `~/.pi` is ignored. Repository instructions such as `AGENTS.md` are discovered through the remote tools. Repository-local executable `.pi` configuration on the executor is not loaded.

Every operation carries the item, executor, claim ID, claim generation, repository key, and slot. The executor validates those values and confines the operation's working directory to the claimed workspace. A stale claim fails instead of falling back to the Wi host filesystem.

Tool processes are independent. SSH connection multiplexing avoids a fresh transport handshake for every call, but shell state does not persist between Bash invocations. Cancellation uses a separate claim-bound operation so it can terminate an active command. Workspace release waits for active tool leases before making a slot reusable.

Only the bundled Pi driver currently supports host-owned runtimes with remote workspace tools. Other drivers fail closed for remotely placed workspaces.

## TUI and terminal behavior

The Pi TUI and work-item tmux session remain on the Wi host. Attach and switch exactly as for local work:

```text
terminal -> SSH to Wi host -> host tmux -> host Pi TUI
```

There is no worker-hosted Pi TUI and no second Pi process against the conversation. Copy mode and scrollback belong to the host tmux session.

The default shell window is host-local in this version. Use Pi's Bash tool for claim-fenced executor commands. Persistent user shells attached to executor workspaces may be added separately; they must not be used as hidden transport for model tool calls.

Headless runtimes also remain on the Wi host and do not depend on tmux.

## Returning work

Release requires a clean executor worktree. The executor exports committed Git refs in a bundle, and the Wi host imports the implementation branch before clearing the claim. Uncommitted files are not copied back. Commit wanted changes before release, archive, or merge.

```bash
wi workspace status --item investigate-macos-behavior
wi workspace release --item investigate-macos-behavior
```

If an executor is unavailable, lifecycle intent remains canonical on the Wi host, but operations requiring the workspace fail until the executor returns.

## Limits

- Executor software and project dependencies are user-provisioned.
- Only committed Git state is transferred in either direction.
- Repository-home workspaces cannot use remote executors.
- Executor selection cannot change after first materialization.
- Persistent interactive executor shells are not yet part of the host-owned runtime path.
- A remote executor scales workspace execution; it does not make the coordinator highly available.

Use `wi executor shutdown --force <name>` to stop worker resources associated with the current Wi data root. It does not affect another coordinator's executor state.
