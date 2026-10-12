# Docker and SSH executors

Status: implemented on `remote-tool-routing`; final acceptance work is recorded in `HANDOFF.md`. The earlier worker-daemon executor implementation has been removed. No migration is supported.

## Placement

The host owns the coordinator, `wi.db`, descriptions, Pi, credentials, native conversations, runtime control, and user-facing tmux. Each item owns an independent clone, operation state, and private tmux server holding jobs and interactive shells. Docker places these at `/workspace` and `/wi` in an owned container. SSH places them under a dedicated claim directory on an existing host. No Wi binary, worker daemon, or conversation replica runs in the executor.

Checkout kind `executor` selects `[executors.<name>]`, with either Docker image/network or SSH host/root/config settings. Materialization reserves a host claim before external work. The claim pins environment settings, environment name, claim ID, and monotonic generation. Docker labels and shared workspace markers fence operations. Missing ready containers or SSH workspaces are reported, never silently recreated.

The user guide, [Use executors](../src/content/docs/guides/executors.md), documents commands, requirements, limits, and lifecycle behavior.

## Implementation

- `internal/executor/executor.go`: shared client, profile validation, claim identity, jobs, status, and attachment.
- `internal/executor/environment.go`: shared acquisition/removal interface. Docker owns a container; SSH owns a directory only.
- `internal/executor/ssh.go`: OpenSSH transport and SSH workspace acquisition/removal. Arguments are shell-quoted, host keys checked strictly, forwarding disabled, and authentication noninteractive.
- `internal/executor/scripts.go`: embedded short shell operations and tmux-held job wrappers. No installed helper or public executor protocol.
- `internal/executor/git.go`: bundle seed/update/import and quarantine refs.
- `internal/app/executor.go`: claim reservation, provisioning, export, release, and shell access.
- `internal/app/integration_executor.go`: in-executor rebase and host target integration with conditional rollback.
- `internal/app/executor_sync.go`: coalesced exports after acknowledged settled turns, outside event ACK handling.
- `internal/app/executor_tools.go`, `internal/workspacetool/`: per-runtime streaming Unix socket bridge.
- `internal/agentdriver/pi/remoteexec/index.ts`: bundled Pi tool overrides and user Bash routing.

Headless runtimes do not require host tmux. Every executor image requires tmux. The host shell window runs `wi internal-executor-attach`; the Pi window continues to run host Pi.

## Jobs

Each Bash call creates an independent tmux session and records its runtime, output, process-group ID, and exit status. Retries use the same job ID. Output is read by offset. Transport loss stops the follower, not the job; automatic follower reconnection is not implemented. A replacement Pi runtime explicitly cancels old unfinished jobs rather than replaying them.

Coreutils timeout supplies the job process group and deadline. Abort signals that group, then escalates. Daemonized children that leave the group may require container destruction. Completed logs are pruned to 32 jobs. Output is bounded by the process file-size limit, not a truncating ring buffer. Calls time out after at most 24 hours.

Short operations use a mkdir lock and a bounded wait. A process killed before its EXIT trap can leave `/wi/lock`; operators must inspect the executor and remove a stale lock after confirming no operation is running. A resident helper and more elaborate lease recovery are deferred.

## Files and Git

All seven coding tools and `!`/`!!` route into the executor, with no local fallback. Writes stage data beside the destination, verify its checksum, and rename. Edits also compare the original content hash immediately before rename. These checks prevent incomplete transport writes and detect many concurrent changes, but do not fence arbitrary writers in the interactive shell.

Bundles transfer committed history only. Origin is a bundle-backed remote with fetch support, not a push endpoint. Host imports use a temporary isolated repository, strict validation, and `refs/wi/executor/<item>/<generation>/head`. Source hooks, signing configuration, credentials, and native agent conversations are not copied. Submodules, LFS pointers, reftable, and bundles over 256 MiB are rejected.

Merge sends current target history, rebases in the executor, imports its branch, and conditionally advances the host target. A failed rebase is aborted; retry after repairing the branch. Do not edit concurrently with merge. Host tools can execute executor-authored content once it enters the host checkout through merge.

## Partial outcomes

Provisioning failures keep the reserved claim. A retry reuses it. Shelve exports commits and retains the container and dirty files. Release refuses dirty work unless forced, marks admission closed, stops jobs, rechecks status, exports, and records destruction intent before removing the container. Failed destruction remains retryable. A missing executor can be force-released using only previously exported commits. SSH removal deletes only its claim directory after validating ownership and closing jobs; the remote machine is never created, stopped, or deleted. Unreachable SSH hosts are errors, not missing workspaces. SSH tools have the account's full permissions, not container isolation.

Settled exports check runtime and claim ownership and skip unchanged HEAD. Failures leave the previous export intact and are diagnostic, not lifecycle changes.

## Deferred

Generic executor drivers, shared caches, port forwarding, credential forwarding, resident helpers, automatic transport reconnection, and distributed coordinator availability are outside this change. Research notes are background evidence, not implementation requirements.
