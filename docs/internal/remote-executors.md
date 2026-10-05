# Remote executor architecture

This document describes the implementation, not the user interface. It records the ownership rules and the details that are easy to break when changing remote execution.

The short version is simple. The coordinator owns the work item. An SSH executor currently owns replaceable copies of the workspace, agent process, and private terminals. Local tmux remains the user interface.

`docs/internal/remote-agent-placement.md` examines an alternative where Pi and its conversation remain on the coordinator while Pi tools operate on the remote workspace. That option was not previously implemented or rejected in this repository.

## Scope and status

Remote executors currently support:

- managed-slot workspaces transferred with Git bundles
- TUI and headless agent runtimes
- prompt, steering, follow-up, abort, and shutdown control
- driver-owned conversation export and coordinator checkpoints
- local tmux gateway sessions attached to private worker tmux sessions
- remote agent and shell scrollback
- remote yanks copied into the coordinator tmux paste buffer
- restricted coordinator access for commands run inside a worker workspace
- release, archive, delete, reconciliation, and executor shutdown

They do not yet support:

- moving an already materialized item to another executor
- snapshots of dirty tracked or untracked files
- incremental conversation checkpoints
- durable event cursors
- automatic reconnection of gateway SSH panes or the authority tunnel

`docs/src/content/docs/guides/workspace-moves.md` describes planned workspace movement. Do not implement movement by editing `checkout.specification.executor` after materialization.

## Non-negotiable ownership rules

1. The main coordinator daemon is authoritative for work-item identity, lifecycle, labels, source context, checkout intent, the accepted workspace claim, and the primary conversation identity.
2. A worker daemon stores a replica for local execution. Its manifest is not a second authority.
3. A workspace claim is identified by both an opaque claim ID and a monotonically increasing generation.
4. A runtime and terminal must match the current workspace claim. An old generation cannot regain control after release or rematerialization.
5. The executor never needs repository credentials. The coordinator sends Git objects directly.
6. Local tmux owns navigation and layout. Worker tmux exists only to keep remote agent and shell terminals alive.
7. Losing SSH detaches from a worker terminal. Explicit terminal close destroys the worker terminal resources.
8. List commands use coordinator projections and do not contact every executor. Commands that request fresh status or control may use SSH.
9. The full coordinator operator socket is never forwarded to an executor.
10. Worker terminal sessions must be gone before a slot can be released and reused.

## Process topology

### Control and execution

```text
coordinator host
  wi CLI
    -> coordinator daemon and canonical wi.db
    -> SSH request: wi internal-executor
         -> typed executor protocol
              -> worker-local wi daemon and replica
              -> worker Git repository and managed slot
              -> worker driver process and coding agent
              -> private worker tmux server
```

Each ordinary executor protocol request starts a noninteractive SSH process. The request and response use `executor/protocol`, version 1. `internal/executor/client.go` starts the SSH command. `internal/executor/serve.go` decodes one request, calls `Worker.Handle`, writes one response, and exits.

The protocol operations are:

| Operation | Purpose |
| --- | --- |
| `doctor` | Report version, platform, tools, and private tmux readiness |
| `authority.info` | Prepare the worker authority context and return its socket path |
| `item.delete` | Remove an unclaimed worker replica and its claim files |
| `items.reconcile` | Remove stale replicas that have no live resources |
| `workspace.ensure` | Import Git objects, claim a slot, and create or refresh the replica |
| `workspace.status` | Run worker-local worktree observation |
| `workspace.release` | Validate claim fencing, release the slot, and return a Git bundle |
| `runtime.ensure` | Start or reuse a TUI or headless runtime and wait for readiness |
| `runtime.status` | Return runtime, events, status, manifest, and conversation snapshot |
| `runtime.control` | Send prompt, steer, follow-up, abort, or shutdown |
| `terminal.copy_mode` | Enter copy mode on the worker agent or selected shell session |
| `terminal.close` | Close worker terminal metadata and all matching private sessions |
| `shutdown` | Stop the worker daemon and private tmux server |

The worker runs its own `wi` daemon under isolated XDG roots. This lets existing workspace, runtime, conversation, and event code run locally on the executor. The isolation is keyed by the coordinator installation's `MasterID`.

### Interactive terminals

```text
coordinator tmux server
  work-item gateway session
    agent window
      -> interactive SSH
           -> worker tmux agent session
                -> agent TUI
    shell window or split
      -> interactive SSH
           -> worker tmux shell session for that gateway pane
                -> login shell in the worker workspace
```

The SSH pane is a gateway, not the terminal owner. Scrollback, copy mode, and the live shell or TUI belong to worker tmux.

### Restricted reverse authority

```text
worker process
  WI_EXECUTOR_DAEMON_SOCKET
    -> worker authority.sock
         -> reverse SSH Unix-socket forwarding
              -> coordinator executor endpoint
```

`Client.EnsureAuthority` creates a persistent SSH control master and reverse-forwards the coordinator's restricted executor socket. The worker sees only `authority.sock`.

The endpoint currently allows:

- ping and restricted status
- list manifests
- safe backlog item creation
- activity barrier
- agent observation

It rejects lifecycle, workspace, runtime, terminal, delete, and shutdown control. Executor-side item creation must name the current authoritative parent item. The new item inherits repository identity and executor selection, and its created-from commit must match the parent.

## Durable and replicated state

The canonical checkout has two parts:

```json
{
  "specification": {
    "kind": "managed-slot",
    "executor": "worker-name",
    "branch": "wi/example"
  },
  "claim_generation": 2,
  "claim": {
    "id": "01M...",
    "generation": 2,
    "executor": "worker-name",
    "repository_key": "e7a7dc9a89d726bc",
    "slot": "slot-0001",
    "path": "/remote/path/to/slot-0001"
  }
}
```

The specification is desired placement. The claim is actual ownership. Creating an item does not allocate a slot. First materialization creates generation 1. Release clears the claim but retains the generation. Rematerialization creates generation 2.

Executor selection is mutable only before first materialization. `Checkout.EverMaterialized` and historical `checkout.assigned` events enforce this even after release.

Repository-home checkouts are local-only. `wi new --home --executor <remote>` fails with `repository-home checkout requires the local executor`. Selecting a remote executor for an existing repository-home item fails with `repository-home checkout cannot select an executor`. An already materialized managed slot cannot be changed to remote either, even after release, because that operation is a workspace move and moves are not implemented yet.

Worker manifests translate remote intent into local execution:

- `repository.current_root` points to the worker bare repository
- `checkout.specification.executor` becomes `local`
- the worker creates its own local claim for the managed worktree
- conversation executor fields become `local` inside the replica

The coordinator accepts the worker's returned claim as its remote claim. Runtime and terminal records include the accepted claim ID and generation.

## Worker filesystem layout

For one coordinator installation, the worker root is:

```text
~/.local/share/wi-executor/<master-key>/
```

Important paths include:

```text
xdg/data/wi/                         worker daemon data root
xdg/config/wi/config.toml            generated agent and driver config
xdg/state/                           worker diagnostics
repositories/<repository-key>.git   bare repository
claims/<repository-key>/<slot>      slot owner file
locks/                               item, repository, and reconciliation locks
authority.json                       selected executor name
authority.sock                       restricted reverse authority endpoint
```

Runtime files use a short path because Unix socket paths are bounded:

```text
$TMPDIR/wi-executor-<uid>/<master-key>/
```

Generated tmux configuration and the terminal profile live under the worker user cache directory:

```text
<cache>/wi/executors/<master-key>/
```

Never derive these paths from an item slug. `MasterID`, item IDs, repository keys, slot names, and gateway pane IDs have separate validation rules.

## Materialization sequence

`App.ensureRemoteWorkspace` performs the coordinator side of materialization.

1. Resolve the configured executor.
2. Run doctor and require an exact `wi` version match.
3. Check Git, tmux, Bash, and the selected agent's required tools.
4. Establish restricted reverse authority.
5. Reconcile stale worker replicas. Keep replicas selected for this executor or holding a conversation pinned to it.
6. Pick the first unused `slot-NNNN` for the repository key.
7. Read the canonical description.
8. Build worker-specific agent and driver configuration. Host-local Pi executable and extension paths do not cross machines.
9. Create a Git bundle with all coordinator refs.
10. Send the manifest, description, bundle, slot, terminal profile, and generated config to `workspace.ensure`.
11. Validate the returned executor, repository key, claim ID, and generation.
12. Save the accepted claim in the canonical manifest and append `checkout.assigned`.

The worker serializes materialization with an item lock and a repository lock. It rejects unsafe path components and bundles larger than 512 MiB.

The worker imports coordinator branches under:

```text
refs/remotes/wi-coordinator/*
```

It imports tags under `refs/wi-coordinator/tags/*`. It never force-updates unrelated worker branches from the incoming bundle. A clean warm slot is switched to the requested branch and start point. A dirty warm slot is not reused.

The worker then writes the slot owner file, starts its private daemon, creates or reconciles the item replica, refreshes the description, and returns the local claim details.

A dirty coordinator checkout is not transferred when creating a new remote managed-slot item. The Git bundle contains committed Git objects and refs only. The remote slot starts from the recorded commit or coordinator branch. Item creation warns when the coordinator checkout is dirty, names the selected executor and abbreviated commit, and states that staged, unstaged, and untracked files are not included.

Once a remote managed slot exists, release, shelve, and archive reject dirty worker state with an uncommitted-changes error. The worker's release path performs its own clean-worktree check, so `--force` does not turn dirty files into a transferable snapshot.

### Partial materialization failure

Materialization is not one distributed transaction. A worker can create a slot and replica before the coordinator saves the accepted claim. The item lock and reconciliation reduce the damage, but they do not make the operation atomic. Code in this path must return the retained result or a precise recovery error. It must not delete canonical work-item state to hide a later failure.

## Runtime and conversation flow

`runtime.ensure` accepts `tui` or `headless`. Headless execution does not require local or worker tmux. Tmux is part of TUI composition and optional terminal access only.

The worker invokes its local `wi agent runtime ensure`, then waits up to 30 seconds for either:

- a `runtime.ready` event for the current runtime ID
- a runtime problem state

Readiness is driver-neutral. Do not infer it from Pi prompt capabilities.

The worker returns:

- serialized runtime state
- the worker manifest
- up to 500 recent compact runtime events
- an exported conversation snapshot when requested

The coordinator marks the runtime with the remote executor and clears worker-local PID fields. It ingests compact events into the authoritative daemon and imports the conversation snapshot through the configured driver backend.

Conversation identity remains canonical in the main manifest. Native conversation content is driver-owned. The current checkpoint is a full export on status or control paths, not an incremental stream. Conversations remain executor-pinned until an explicit move can export, validate, import, and cut over their ownership.

Conversation and terminal placement must not be conflated:

| Resource | Coordinator | Executor while remote work is active |
| --- | --- | --- |
| Conversation identity and executor assignment | Canonical manifest | Replica manifest translated for local execution |
| Native conversation file | Last imported durable checkpoint | Live writable copy used by the running driver |
| Runtime record | Mirrored record without worker PID ownership | Live process and worker-local runtime record |
| User-facing tmux layout | Local gateway session | None |
| Agent and shell terminal process | SSH gateway panes only | Private persistent worker tmux sessions |
| Terminal scrollback and first copy buffer | Not retained by the SSH gateway | Worker tmux |

The live driver writes the executor copy. The coordinator copy currently advances only when a coordinator-initiated executor request returns a conversation export:

- initial runtime ensure after readiness
- explicit agent or runtime status
- prompt, steer, follow-up, abort, or shutdown control
- runtime shutdown and the status polling around lifecycle cleanup

A worker `turn.ended` or `agent.settled` event does not currently trigger a checkpoint by itself. A user can complete a long TUI turn and make no later coordinator request, leaving the local conversation checkpoint behind until status, control, or shutdown runs. A clean runtime shutdown requests a final checkpoint, but worker loss can still lose conversation bytes written after the last successful export.

The current full-file copy is not a complete recovery point because it excludes dirty workspace state. It also should not be treated as proof that the coordinator must permanently store native execution artifacts. Centralized control-plane ownership and centralized byte storage are separate design choices.

`docs/internal/remote-checkpointing.md` separates projection synchronization, planned movement, worker-loss recovery, and long-term archival. It records the unresolved failure model, artifact placement, consistency, retention, and transfer decisions. Do not add turn-end artifact uploads until those requirements are chosen.

Runtime control uses the executor protocol and the worker's ordinary `wi agent control` command. Supported commands are prompt, steer, follow-up, abort, and shutdown.

### Event limitation

The worker currently returns only the latest 500 events. The coordinator has no durable per-executor cursor. A long disconnected interval can lose older activity events. Fix this with persisted sequence cursors, not a larger magic limit.

## Terminal implementation

### Private worker tmux

The worker starts tmux on a dedicated socket with generated configuration:

```tmux
set-option -g status off
set-option -g prefix None
set-option -g prefix2 None
set-option -g mouse off
set-option -g history-limit 50000
set-option -g extended-keys on
set-option -g set-clipboard on
set-option -g allow-passthrough on
set-window-option -g mode-keys vi  # or emacs from executor config
```

It does not load the user's tmux configuration. No worker prefix, status bar, plugins, or mouse bindings may compete with the coordinator tmux interface.

The agent session uses `wi-<short-item-id>`. A shell session uses:

```text
wi-shell-<short-item-id>-<hash-of-local-gateway-pane-id>
```

The shell SSH command passes the local `$TMUX_PANE` to `internal-executor-shell`. This gives each local shell pane a stable worker session while the gateway pane exists. It also lets a reconnected SSH process attach to the same remote shell.

Older shell sessions created before pane identities were added have random suffixes. Copy-mode targeting may use one legacy shell only when exactly one matching shell session exists. If several legacy sessions exist, fail instead of guessing.

### Local gateway tmux

`EnsureGatewaySession` creates or reuses a normal local work-item session. Its environment includes `WI_ID` and `WI_EXECUTOR`. The agent window runs `internal-executor-attach`. The shell window and later windows or splits use the generated shell SSH command as `default-command`.

The local session owns user-facing navigation. It does not own remote history. Closing an SSH transport must not kill the worker session. Re-entering the item attaches again. Automatic SSH reconnect is not implemented yet.

### Copy mode and scrollback

A plain local `tmux copy-mode` on an SSH gateway pane sees only the gateway's screen and small local history. It cannot expose history retained by worker tmux.

Remote copy mode therefore uses `wi terminal copy-mode`:

- no attachment ID targets the worker agent session
- `--pane %NNN` targets the worker shell attached to that local pane

The local binding must use tmux format values from the pane that received the key. Do not route with shell variables such as `$WI_ID` or `$WI_EXECUTOR`; `run-shell` does not reliably inherit the invoking pane's session environment. Do not wrap the command in nested `if-shell` or nested `run-shell` calls that change format context.

The working shape is:

```tmux
bind '[' run-shell -E -t '#{pane_id}' \
  'if [ -n "#{WI_EXECUTOR}" ] && [ "#{WI_EXECUTOR}" != local ]; then if [ "#{window_name}" = agent ]; then wi terminal copy-mode --item "#{WI_ID}" >/dev/null; else wi terminal copy-mode --item "#{WI_ID}" --pane "#{pane_id}" >/dev/null; fi; else tmux copy-mode -t "#{pane_id}"; fi'
```

A yellow `[0/0]` indicator from the local tmux means the wrong copy mode opened. Worker tmux has no status bar. Inspect both levels when debugging:

```text
local gateway pane: pane_in_mode must remain 0
worker target pane: pane_in_mode must become 1
worker target pane: scroll_position must change after navigation
worker target pane: history_size must be greater than the visible screen
```

`tmux_mode_keys` must match expected navigation. With vi keys, `k`, `j`, `Ctrl-u`, and `Ctrl-d` work in worker copy mode.

### Clipboard transfer

Copy buffers start on the tmux server that owns copy mode. A yank in worker copy mode first updates the worker buffer. Worker tmux then emits an OSC 52 clipboard update through the SSH client.

The coordinator tmux must accept application OSC 52 updates into its own server-wide paste buffer:

```tmux
set-option -s set-clipboard on
```

The usual `external` value is not enough. It may update the host terminal clipboard, but it ignores application attempts to update tmux buffers. The result is deceptive: paste may work while still attached remotely, then `prefix+]` in another local work-item session pastes an older coordinator buffer.

The complete path is:

```text
worker copy selection
  -> worker tmux buffer
  -> OSC 52 over its attached SSH client
  -> coordinator tmux accepts OSC 52
  -> coordinator server-wide paste buffer
  -> prefix+] in any local work-item session
```

A yank made before `set-clipboard on` is not synchronized retroactively. Yank it again after reloading the local tmux configuration.

When testing this, use a unique value and verify the latest buffer on both tmux servers. Remove test buffers afterward. Do not test by injecting keys into a user's live shell or agent pane.

## Environment rules

The worker process environment uses isolated `XDG_*` paths and sets:

```text
WI_EXECUTOR_WORKER=1
WI_TMUX_SOCKET=<private-worker-socket>
WI_TMUX_CONFIG=<generated-config>
```

Once authority is configured it also sets:

```text
WI_EXECUTOR=<configured-name>
WI_EXECUTOR_DAEMON_SOCKET=<worker-authority.sock>
```

Only configured `forward_env` variables cross to the terminal profile. `TERM` and `COLORTERM` are typical. The remote shell path and copy-mode key style come from executor configuration.

A worker shell copies the agent terminal session environment, then removes nested terminal and agent-specific values such as `TMUX`, `TMUX_PANE`, `WI_TMUX_SOCKET`, `WI_TMUX_CONFIG`, and `WI_AGENT_*`. It sets the worker `WI_DIR` and `WI_WORKTREE`. This avoids attaching a new shell to stale agent control sockets or the wrong tmux server.

## Release and cleanup

A safe remote release proceeds in this order:

1. Confirm the remote branch matches the checkout specification.
2. Reject uncommitted tracked or untracked changes. `--force` does not create a dirty-file snapshot.
3. Stop the runtime and wait for it to stop.
4. Close local gateway tmux resources when it is safe to do so.
5. Ask the worker to close the agent session and every matching shell session.
6. Ask the worker to release the workspace using the exact claim ID and generation.
7. Have the worker bundle all Git refs.
8. Import the implementation branch into the coordinator repository.
9. Clear the canonical claim but retain `claim_generation`.
10. Keep the worker worktree as a clean warm slot.

The worker refuses release while any matching terminal session remains. It also rejects a stale claim ID, generation, repository key, or slot.

Archive and shelve compose runtime stop, terminal close, and workspace release. Delete tells the selected executor to delete the replica after canonical deletion checks. Reconciliation removes replicas absent from the coordinator keep set only when they have no claim, live runtime, or terminal sessions.

### Release partial failure

Release is also not atomic across hosts. The worker releases before the coordinator imports the returned bundle and clears its claim. If bundle import or the canonical save fails, the coordinator may still show a claim that the worker has released. Treat this as a recovery case. Do not silently allocate a different slot or lower the generation to make a retry pass.

## Failure semantics

- A failed SSH request does not prove that the worker made no change. Inspect worker state before retrying a materialization or release mutation.
- Network loss leaves worker tmux and agent processes alive. It may close the local SSH pane because reconnect loops are not implemented.
- An absent worker tmux server or item session is normal during cleanup. Inspection errors and contradictory claims are not.
- A stale authority socket must be removed before reverse forwarding. `StreamLocalBindUnlink=yes` handles the SSH bind side.
- Worker and coordinator `wi` versions must match for materialization. Doctor reports both platform and tool versions.
- A stale runtime, terminal, or release request from an older claim generation must fail.
- Lists must stay local to the coordinator. Do not add synchronous executor status calls to list rendering.

## Security boundaries

- Executor transport is SSH in batch mode. There is no generic transport abstraction.
- `wi_command` is one executable path without arguments.
- The worker validates master IDs, work-item IDs, executor names, repository keys, slots, and gateway pane IDs before using them.
- Worker roots are private to the user. Generated config and authority context use mode `0600`; directories use `0700`.
- The reverse authority socket exposes only `AccessExecutor` methods.
- Git credentials and origin configuration do not need to exist on the worker.
- Runtime events contain compact semantic fields. They do not carry unrestricted prompts, responses, paths, tool arguments, or errors into canonical event history.
- Driver conversation import and export must validate conversation identity and locator containment.

## Validation checklist

Run this sequence after changing executor code.

### Static and unit checks

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./internal/executor ./internal/app ./internal/coordinator
go vet ./...
git diff --check
```

### Provisioning and compatibility

1. Install the same development build on coordinator and worker.
2. Run `wi executor doctor <name>`.
3. Check OS and architecture reporting.
4. Check Git, tmux, Bash, Pi when used, worker shell, tmux socket creation, and mode keys.
5. Confirm a noninteractive SSH command can find every configured executable.

### Workspace lifecycle

1. Create an absent remote backlog item.
2. Confirm generation 0 and no claim.
3. Materialize it and confirm generation 1 with a remote claim.
4. Confirm the worker replica, claim file, bare repository, slot, and branch.
5. Commit remotely, release, and confirm coordinator import.
6. Confirm the claim is null but generation remains 1.
7. Rematerialize and confirm a different claim ID at generation 2.
8. Try a stale generation-1 release and require rejection.
9. Archive and delete. Confirm no private sessions, claims, or replicas remain.

### Runtime behavior

Test both `tui` and `headless` modes.

1. Confirm runtime ownership records the active claim ID and generation.
2. Wait for `runtime.ready`.
3. Exercise prompt, steering, follow-up, abort, and shutdown according to advertised capabilities.
4. Confirm status reaches the coordinator without changing list into an SSH fan-out.
5. Confirm the coordinator can import a driver-owned conversation checkpoint.
6. Supersede the claim and require old runtime control to fail.

### Terminal behavior

Test through the real nested path. Direct worker tmux commands are useful diagnostics but are not proof that a user's keys cross both tmux servers.

1. Enter the remote agent window.
2. Enter the remote shell window.
3. Create another local window or split and confirm it gets a distinct persistent worker shell.
4. Detach or interrupt SSH. Confirm worker sessions survive.
5. Re-enter and confirm attachment to the intended session.
6. Use the actual `prefix+[` binding in agent and shell panes.
7. Confirm local `pane_in_mode=0` and worker `pane_in_mode=1`.
8. Scroll beyond the initially visible screen.
9. Yank a unique string remotely.
10. Switch to another local work-item session and paste it with `prefix+]`.
11. Close the terminal explicitly. Confirm local gateway, worker agent, and worker shells are gone.
12. Confirm release succeeds only after terminal cleanup.

## Troubleshooting map

| Symptom | Likely cause | Check |
| --- | --- | --- |
| Yellow `[0/0]` appears in a remote pane | Outer tmux entered copy mode | Binding pane formats and `WI_EXECUTOR` expansion |
| Copy mode opens but cannot reach old output | Wrong tmux level or worker history absent | Local and worker `pane_in_mode`, worker `history_size` |
| Agent scrolls but shell does not | Copy command omitted `--pane`, or shell identity is stale | Gateway pane ID and worker shell session name |
| A shell copy request is ambiguous | Several legacy random shell sessions exist | Close and recreate the affected local shell panes |
| Remote yank pastes remotely but not locally | Coordinator `set-clipboard` is `external` or `off` | `tmux show-options -s set-clipboard` |
| Worker copy keys feel wrong | `tmux_mode_keys` differs from local expectations | Generated worker tmux config |
| New local splits run the wrong command | Gateway `default-command` was not refreshed | Re-enter through `wi` and inspect session option |
| Release says terminals remain | Private agent or shell sessions still exist | Worker dedicated tmux session list |
| Materialization cannot reuse a slot | Warm slot is dirty or claimed by another item | Git porcelain status and claim owner file |
| Runtime starts but never becomes ready | No matching `runtime.ready`, stale runtime ID, or driver failure | Worker event stream and runtime problem state |
| Executor-side `wi new` is denied | Missing authoritative parent or created-from mismatch | Restricted authority item and source commit |
| Authority forwarding fails | Stale socket or stale SSH control master | Worker `authority.sock` and local control socket |

## Source map

- `executor/protocol/`: public typed coordinator-to-worker protocol
- `internal/executor/client.go`: SSH requests, reverse authority, bundles, terminal commands
- `internal/executor/serve.go`: one-request worker process entry
- `internal/executor/worker.go`: worker operations and isolated environment
- `internal/executor/attach.go`: private agent and shell attachment
- `internal/app/executor.go`: coordinator orchestration and checkpoints
- `internal/app/access_tmux.go`: local gateway composition and terminal routing
- `internal/app/workspace.go`: remote status and release
- `internal/app/usecase_shelve_archive.go`: lifecycle cleanup ordering
- `internal/model/manifest.go`: checkout specification, claim, and generation
- `internal/model/agent_runtime.go`: runtime claim fencing
- `internal/model/terminal_runtime.go`: rebuildable terminal handles
- `internal/coordinator/server.go`: restricted executor endpoint
- `internal/tmux/tmux.go`: tmux server, gateway, copy-mode, and client targeting

## Current architecture in one paragraph

The main daemon owns durable intent and identity. A configured SSH executor runs an isolated worker daemon that holds a local replica, a bare repository, a reusable managed worktree slot, a driver process, and private tmux sessions. Coordinator commands use a versioned request-response protocol over short-lived SSH processes. Worker commands get limited access back through a reverse-forwarded restricted Unix socket. Local tmux presents agent and shell gateway panes, while worker tmux preserves the real terminals, history, and copy buffers. Claim IDs and generations fence every workspace-dependent resource. Git bundles and driver exports return durable work to the coordinator. Network loss detaches, explicit close destroys terminals, and release returns Git state before the coordinator clears its claim.
