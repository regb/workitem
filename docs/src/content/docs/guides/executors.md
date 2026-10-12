---
title: Use executors
description: Keep Pi on the host and run an item's tools and shell in Docker or over SSH.
---

An executor selects where an item's tools and shell run: in a Docker container or over SSH. Each item has an independent Git clone, jobs, and shell in its own workspace. Wi, Pi, credentials, conversations, and the user-facing tmux session stay on the host. The seven coding tools and Pi's `!` and `!!` commands run in the executor. There is no host filesystem fallback.

## Prepare an image

Docker must be running on the Wi host. Images are user-maintained. This small reference image is also used by the integration tests:

```dockerfile
FROM alpine:3.22
RUN apk add --no-cache bash git tmux file ripgrep coreutils
WORKDIR /workspace
```

Build it locally:

```bash
docker build -t wi-dev .
```

The image must provide Bash, Git, tmux 3.3 or later, ripgrep, `file`, coreutils including `timeout`, and the ordinary shell utilities. `/workspace` and `/wi` must be writable by its default user. Add your compiler, editor, and other project tools to the image. Wi checks requirements during materialization. It does not install tools or copy a Wi binary into the executor.

Register the image in `~/.config/wi/config.toml`:

```toml
[executors.dev]
driver = "docker"
image = "wi-dev"
network = "none"
```

`network` accepts `none`, the default, or `bridge`. Wi supplies no host mounts, Docker socket, credentials, SSH forwarding, privileged mode, or host network. Host-side Pi extensions and MCP servers are still trusted host code; an executor does not sandbox the Pi process itself.

## Use an SSH host

Instead of a Docker image, select a reachable SSH account with the same tool requirements:

```toml
[executors.builder]
driver = "ssh"
host = "build-server"
root = "/home/dev/wi"
# Optional absolute host-side OpenSSH config file:
# ssh_config = "/home/me/.ssh/config"
```

`host` is a normal SSH destination or config alias. Use your OpenSSH config for user, port, and identity selection. Authenticate noninteractively and verify the server host key before starting Wi. Wi uses batch mode and strict host-key checking, disables agent/X11/port forwarding, and never prompts for a password.

`root` must be a writable, canonical absolute path without symlinks, at most 45 bytes long to leave room for tmux socket names. Wi creates `<root>/<claim-name>/workspace` and `<root>/<claim-name>/state`. Release removes only that claim directory after ownership checks, stopping its jobs and exporting commits. It never provisions or deletes the SSH host. A disconnected host is an error, not an absent workspace.

SSH provides no filesystem or process sandbox: tools have the remote account's existing permissions and access to its files and credentials. Prefer a dedicated account. Wi does not copy or forward host credentials. Use Docker when container isolation is required.

Docker and SSH share the same commands, claim generations, tool routing, Git exchange, jobs, and lifecycle. To switch backends, release the current workspace, change the profile settings, then ensure again. A live claim pins its original connection and environment settings; changing configuration does not relocate it.

```bash
wi new --executor builder "Build on the server"
wi start build-on-the-server
```

## Create work

```bash
wi new --executor dev "Implement the parser"
wi workspace ensure --item implement-the-parser
wi start implement-the-parser
```

Or create and start together:

```bash
wi start --new --executor dev "Implement the parser"
wi new --executor dev --agent-mode headless --prompt "Implement and test the parser" "Parser"
```

`--executor` and `--home` are mutually exclusive. The bundled Pi driver is required. The selected profile is part of the checkout specification, and a materialized claim pins its image and network settings.

Only committed Git history transfers. Staged, unstaged, and untracked host files do not. The executor owns its `.git`; the host repository is never mounted inside it. The agent sees the executor's workspace path as its working directory and reads project instructions there. Repository-local executable Pi configuration is not loaded. Host-only skill files are not mounted into the executor.

## Jobs and shells

The Pi window remains on the host. The shell window attaches to a private tmux server inside the executor. Run editors, diff tools, tests, and development servers in that shell.

Bash tool calls are independent jobs, not input injected into the user shell. Executor-side tmux holds each job while stdout and stderr go to one combined log. Wi streams the log to Pi. Jobs and shells survive a disconnected attachment while the environment remains running.

Abort explicitly signals the job's process group. An executor-side timeout still applies while the host is disconnected. Jobs have a maximum duration of 24 hours, an output-file limit of at most 16 MiB, and retention of 32 completed jobs. Programs that daemonize out of their process group are only guaranteed to stop when the container is destroyed. On SSH, a daemonized process outside the job group may require manual cleanup; Wi does not terminate unrelated remote processes.

A new Pi runtime cancels unfinished jobs from the previous runtime; it does not replay interrupted tool calls. Their logs remain under `/wi/jobs` until pruned. Headless operation needs no host tmux server. The executor still requires tmux.

`wi terminal copy-mode` routes executor shell copy mode to the inner tmux. Host tmux owns navigation; executor tmux owns shell scrollback.

## Return work

Commit on the item's branch. `origin` is a real remote backed by a host-supplied bundle file: fetch works, push does not. No signing keys or forge credentials are copied into the executor.

```bash
wi agent runtime stop --item implement-the-parser
wi merge implement-the-parser
```

Merge delivers the current target history, rebases inside the executor, imports a verified bundle into `refs/wi/executor/<item>/<generation>/head`, and fast-forwards the host target. It does not turn the executor into a local worktree. Resolve rebase conflicts in the executor shell and retry. Avoid concurrent edits while merging.

Exports also run after settled agent turns when HEAD changed, before shelving, and before release. They preserve commits, not uncommitted files. A failed automatic export is reported in runtime diagnostics and retried at a later export point. Before merge, executor-authored files have not entered the host checkout. Merge is the trust boundary after which host editors, direnv, and other project tools may execute that content.

## Lifecycle and recovery

- Shelving stops the agent and exports commits, but retains the executor and dirty files. There is no automatic idle shutdown.
- Waiting does not destroy the executor.
- Release and archive refuse dirty work unless explicitly forced. Successful release exports commits, then destroys the Docker container or removes the owned SSH claim directory.
- A later ensure creates a new claim generation from the last exported history.
- A failed create or release retains its claim. Retry the same operation; Wi does not silently discard the partial result.
- A missing container or SSH workspace is not automatically replaced. Force-releasing its claim can recover only previously exported commits.

```bash
wi workspace status --item implement-the-parser
wi workspace release --item implement-the-parser
```

Submodules, Git LFS pointers, reftable repositories, and bundles larger than 256 MiB are unsupported. Generic sandbox drivers, shared caches, and port forwarding are deferred.
