# Remote executor architecture

This document describes the host-owned runtime implementation. The Wi host owns the coordinator, work item, native conversation, Pi process, and user-facing tmux. An SSH executor owns a replaceable managed workspace and runs claim-fenced tool processes.

The previous worker-hosted Pi implementation is preserved at Git ref `snapshot/worker-hosted-remote-executor`. Do not reintroduce worker runtime or conversation ownership as an accidental side effect of changing workspace execution.

## Invariants

1. The coordinator daemon is authoritative for item identity, lifecycle, checkout specification, accepted workspace claim, primary conversation, and runtime owner.
2. Pi, the driver process, native conversation bytes, and runtime control sockets stay on the Wi host.
3. Only managed slots can be remote. Repository-home means the Wi host's primary checkout.
4. Every remote operation names the current item, executor, claim ID and generation, repository key, and slot.
5. The executor resolves the workspace path from its own accepted claim. A caller-supplied path is never authority.
6. Operation working directories must remain inside that workspace.
7. Remote operations fail closed. They never fall back to the host checkout.
8. Active operations hold shared claim leases. Release and other claim mutations take the exclusive item lock and cannot reuse a slot until operations finish.
9. Claim fencing controls ownership; it is not an OS sandbox. Executor isolation and credentials remain deployment concerns.
10. Tool calls are independent processes. A reusable SSH transport is an optimization, not hidden shell state.

## Topology

```text
Wi host
  coordinator daemon + canonical wi.db
  native Pi conversation
  Pi driver and TUI/headless runtime
  host tmux
    -> internal Pi remote-workspace extension
       -> internal-workspace-runtime
          -> per-runtime Unix socket
             -> executor client
                -> multiplexed SSH: wi internal-executor
                   -> worker claim validation
                   -> process in managed workspace
```

The runtime remains bound to the workspace claim ID and generation even though `AgentRuntime.Executor` is `local`. Conversation executor is also `local`. Workspace placement and runtime placement are separate concepts.

## Materialization

`App.ensureRemoteWorkspace`:

1. Resolves the configured executor and runs doctor.
2. Requires an exact Wi version and Git on the executor.
3. Reconciles stale worker replicas.
4. Builds a Git bundle containing committed coordinator refs.
5. Sends the manifest, description, repository identity, and selected slot through `workspace.ensure`.
6. Validates the returned claim and saves it in the canonical manifest.

The worker uses an isolated data root under:

```text
~/.local/share/wi-executor/<coordinator-key>/
```

It stores a local item replica, bare repository, reusable managed slots, claim owner files, and lock files. That replica exists to reuse local workspace code; it is not a second work-item authority.

Incoming coordinator branches are imported under `refs/remotes/wi-coordinator/*`. Existing worker implementation branches are not force-rewritten. Dirty warm slots are not reused.

Source transfer is committed-only. Staged, unstaged, untracked, and ignored host files do not cross. Dirty executor files likewise do not return automatically.

## Host runtime startup

For a remote claim, `EnsureAgentRuntime` still uses the normal local primary-agent service. It:

- creates or resumes a host-owned conversation;
- starts a local runtime record fenced to the remote claim;
- chooses a host-safe process CWD rather than passing the remote absolute path to `cmd.Dir`;
- starts a private workspace-tool Unix socket;
- injects the bundled Pi remote-workspace extension;
- supplies the logical remote CWD, opaque claim target, helper executable, and executor label through scrubbed `WI_WORKSPACE_*` variables.

Only the bundled Pi driver currently supports this composition. A remote workspace with another or externally configured driver is rejected before launch.

Host Pi settings are authoritative. Worker `~/.pi` is ignored. Because Pi starts from a host-safe item directory, executor-local `.pi` executable configuration is not loaded. Repository instructions and context files are read through redirected tools.

## Pi tool adapter

`internal/agentdriver/pi/remoteexec` is the shared Agentbox remote-exec core plus a small Wi wrapper. It overrides workspace-scoped built-ins:

- `bash`
- `read`
- `write`
- `edit`
- `grep`
- `find`
- `ls`

It also redirects context-file discovery and path completion. Local paths under Pi's host process CWD map to the logical executor workspace. Paths outside that mapping are rejected by executor CWD validation rather than interpreted on the host.

The extension expects an OCI-shaped helper command:

```text
wi internal-workspace-runtime container inspect TARGET
wi internal-workspace-runtime exec [-i] [-w CWD] [-e KEY=VALUE] TARGET COMMAND...
```

`internal/workspacetool` translates this contract to a bounded JSON request over the runtime Unix socket. The target is the opaque claim ID, not a path.

This embedded code originated in the same author's Agentbox remote-exec extension and is distributed under this repository's MIT license.

## Workspace execution protocol

`workspace.exec` carries:

```text
item ID
executor name
claim ID and generation
repository key and slot
working directory
argv
stdin
environment overrides
timeout
```

The worker validates:

- all claim fields equal its current local claim;
- the resolved CWD is the claim root or a descendant;
- argv, environment keys, request sizes, and timeout are bounded;
- environment entries cannot contain NUL bytes.

The command runs without a shell unless argv explicitly names one. Stdout and stderr are each capped at 16 MiB. The response includes exit code and truncation flags. Transport messages remain bounded by the executor protocol limit.

Each SSH request launches `wi internal-executor`, but the client uses OpenSSH `ControlMaster=auto` and `ControlPersist=60` with a short per-executor control path. This reuses transport authentication while retaining independent process semantics.

### Cancellation and release fencing

Normal Bash execution writes a marker identifying the remote process group. On cancellation, the Pi extension sends a second `workspace.exec` request that reads the marker and terminates that group.

Workspace operations acquire a shared item-file lock. This allows the cancellation request and other independent tools to run while the original call is active. Claim-changing operations acquire the exclusive lock. Therefore release waits for all active tool calls, then revokes the claim before a slot can be reassigned.

This lock is cross-process because each SSH request has a separate worker process. Replacing it with an in-memory counter would break fencing.

## Terminals

The user-facing TUI and copy mode belong to host tmux. Terminal session CWD uses a host-safe item directory for remote claims while `WI_WORKTREE` retains the logical remote path for display and identity.

The default shell window is currently host-local. Model tool calls must not be injected into persistent shells: hidden state, framing, concurrency, timeout, and cancellation semantics are unsafe. A future user-facing remote shell may use a private worker tmux session, but it remains separate from Pi tool transport.

Headless runtimes remain tmux-independent.

## Release

A safe release is:

1. Stop or verify absence of the host Pi runtime.
2. Close host terminal resources.
3. Acquire the worker's exclusive item lock, which waits for shared tool leases.
4. Verify the worker worktree is clean.
5. Export worker Git refs and import the implementation branch on the Wi host.
6. Release the worker local claim and clear the canonical claim.

The worker records a release receipt before clearing its local claim. If host bundle import fails, retrying the same fenced release can export the bundle again instead of becoming permanently stuck between worker and host state.

The claim generation remains durable. Rematerialization advances it, so delayed old operations fail even if a slot path is reused.

Uncommitted executor files are not checkpointed. Forced lifecycle operations must not imply that they were transferred.

## Security boundary

The executor client uses SSH batch mode. The coordinator operator Unix socket is not exposed over TCP. The worker can receive a restricted reverse-forwarded authority socket for explicitly supported worker-side Wi commands, but remote tool execution does not require full operator authority.

Claim checks prevent one Wi item from addressing another item's slot. They do not prevent a process running as the executor user from accessing other files available to that OS account. Stronger isolation requires a dedicated user, VM, container, or sandbox configured by the operator.

Do not forward host Pi credentials or load worker Pi configuration. Do not silently execute a failed remote request on the Wi host.

## Failure behavior

- SSH failure leaves lifecycle and claim intent unchanged and returns an operation error.
- A stale claim returns an explicit fencing error.
- If Pi or the host restarts, a replacement local runtime resumes the host-owned conversation and reconnects to the still-valid workspace claim.
- If the worker is lost, canonical item and conversation state remain on the Wi host; dirty worker-only workspace state may be lost.
- Partial materialization can leave a worker slot before the coordinator saves the claim. Reconciliation may clean unclaimed replicas, but code must report partial outcomes rather than deleting canonical state.

## Tests that must remain

- protocol round trips and malformed input limits;
- stale claim ID and generation rejection;
- CWD escape rejection;
- output, input, environment, command, and timeout bounds;
- concurrent shared tool leases and exclusive release fencing;
- helper inspect/exec parsing and stdin/stdout behavior;
- remote runtime records and conversations remain local while retaining claim identity;
- local process startup never uses a remote path as `cmd.Dir`;
- unsupported drivers fail closed;
- TUI and headless startup both route tools without worker tmux or Pi;
- clean release waits for active operations and advances fencing on rematerialization.
