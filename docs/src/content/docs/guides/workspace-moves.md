---
title: Workspace move plan
description: Design and staged implementation plan for moving workspaces and running agents between executors.
---

`wi` does not move materialized workspaces between executors yet. Executor selection is editable only before the first workspace claim. This page records the intended move semantics and the work needed to implement them safely.

## What a move means

A workspace move is a coordinator-managed handoff from one claim to another. It is not operating-system process migration.

The source agent process stops. The target starts a new process using transferred Git state and an imported driver conversation. A successful move preserves the work item, implementation branch, primary conversation, and user-visible agent continuity.

The operation will eventually look like:

```bash
wi workspace move --to netfabric-macbook --item investigate-failure
```

The first implementation should require a clean workspace and a stopped runtime. Later versions can quiesce and resume a running agent automatically.

## Existing foundations

The current model already provides several parts needed by a move:

- The checkout specification and active workspace claim are separate.
- Every claim has an opaque ID and a monotonic generation.
- Release retains the latest claim generation.
- Runtime and terminal records identify the claim that created them.
- Runtime startup and reuse reject superseded claims.
- Remote release transfers committed Git history back to the coordinator.
- Drivers expose conversation export and import operations.
- The coordinator remains authoritative while workers hold disposable replicas.

Executor selection remains fixed after the first materialization. A future move command, rather than `executor select`, will be the only operation allowed to change placement after that boundary.

## Invariants

A move must preserve these rules:

1. The coordinator owns the move record and decides which claim and runtime may act.
2. An item has at most one active workspace claim and one runtime owner.
3. The source claim remains authoritative until cutover.
4. The target reservation is not an active claim and cannot run the primary conversation.
5. Claim IDs, claim generations, runtime IDs, and runtime ownership generations fence delayed operations.
6. The target runtime cannot start until the source runtime has stopped and the conversation checkpoint is complete.
7. Source cleanup never makes its slot reusable while a runtime or shell can still access it.
8. A failed move leaves either the source resumable or the target authoritative. It must not leave both writable.

## Durable move record

The coordinator should persist one active move record per item in `wi.db`. It belongs to the work-item aggregate but should not be folded into the checkout specification or active claim.

A representative shape is:

```json
{
  "id": "01M...",
  "item_id": "01M...",
  "source_claim": {
    "id": "01M...",
    "generation": 3,
    "executor": "local"
  },
  "target": {
    "executor": "netfabric-macbook",
    "generation": 4,
    "reservation_id": "01M..."
  },
  "phase": "target-prepared",
  "runtime_policy": "resume-if-running",
  "conversation_checkpoint": {
    "driver": "pi",
    "format": "pi-jsonl",
    "digest": "sha256:..."
  },
  "workspace_snapshot": {
    "base_commit": "...",
    "head_commit": "...",
    "digest": "sha256:..."
  },
  "created_at": "...",
  "updated_at": "..."
}
```

The record stores the target generation before it becomes active. Normal materialization must refuse that reserved generation.

## Move phases

The durable phases should be explicit and idempotent:

```text
requested
source-quiescing
source-quiesced
checkpointed
target-preparing
target-prepared
cutover
target-starting
source-releasing
completed
failed
```

### Validate

The coordinator verifies:

- the item still owns the recorded source claim
- no other move is active
- the target executor is configured and compatible
- the implementation branch and created-from commit are available
- the driver supports the required conversation export and import operations
- the current workspace state fits the implemented transfer policy

The initial version should accept only a clean, committed workspace and a stopped runtime.

### Quiesce the source

For a stopped move, this phase only verifies that no runtime or terminal owns the claim.

For a live handoff, the coordinator asks the runtime to settle. It waits for the current turn and tool call to finish, rejects new prompts, checkpoints the conversation, and then stops the runtime. A forced move may abort the current turn, but it must report that loss explicitly.

Worker shells are not migratable. The move closes them before cutover. The local gateway may keep its tmux layout, but its panes reconnect to newly created target terminals.

### Capture state

The source exports two independently verified artifacts:

- a workspace snapshot
- a driver-owned conversation checkpoint

The first move implementation can use the existing clean Git bundle path. Dirty transfer needs a versioned snapshot containing the base and head commits, index changes, tracked worktree changes, untracked non-ignored files, file modes, and symlinks. Ignored files and build caches stay on the source executor.

Artifacts need content digests and bounded sizes. The coordinator records the digests before preparing the target.

### Prepare the target

The target imports Git objects and the conversation checkpoint into an isolated reservation. It checks out the implementation branch in a slot that is not visible as the item's active claim.

Preparation validates the repository key, branch head, conversation format, driver configuration, and resulting paths. It does not start the primary runtime.

### Cut over

Cutover is the commit point. One coordinator transaction:

- verifies the source claim ID and generation
- consumes the target reservation
- advances the checkout claim generation
- changes the selected executor
- installs the target claim
- updates the conversation executor after successful import
- revokes source runtime authority
- records the source claim as a cleanup obligation

Before cutover, failure resumes or leaves the source unchanged. After cutover, the target is authoritative. Recovery must continue forward or perform a new move back rather than silently restoring the source.

### Start and release

If the source had a runtime, the coordinator starts a replacement target runtime with the imported conversation. It waits for `runtime.ready` before reconnecting the local agent pane.

Only then does it destroy source terminal resources, release the source claim, and make the source slot reusable. Cleanup is retryable and remains visible in the move record until complete.

## Failure handling

Failures before cutover retain the source claim. Any target reservation can be deleted without affecting canonical placement.

A failure during cutover relies on the database transaction to publish either the old claim or the new claim, never a partial combination.

Failures after cutover retain target ownership and a source cleanup obligation. They may leave the target runtime stopped or in `problem`, but must not restore source write authority automatically.

Coordinator restart recovery reads active move records and resumes the current phase. Every worker operation includes the move ID, source claim identity, target reservation identity, and expected phase so delayed SSH responses cannot advance another move.

## Delivery stages

### Stage 1: cold move for clean, inactive items

- Add durable move records and target reservations.
- Require a clean committed workspace, stopped runtime, and closed terminals.
- Transfer Git state with the existing bundle mechanism.
- Cut over the checkout specification and claim transactionally.
- Release the source only after target validation.

This stage proves ownership, recovery, and rollback without conversation or runtime handoff.

### Stage 2: portable conversation handoff

- Advertise driver checkpoint portability and format compatibility.
- Export the source conversation after the runtime stops.
- Import and verify it on the target.
- Change `PrimaryConversation.Executor` only during cutover.
- Start a target runtime on request.

### Stage 3: live-agent user experience

- Add a runtime quiesce barrier that waits for `settled`.
- Reject new source controls after quiescing starts.
- Stop, checkpoint, move, restart, and reconnect in one command.
- Preserve the local gateway session while recreating remote agent and shell terminals.

This is a logical live move. There is a short period with no running agent process.

### Stage 4: dirty workspace transfer

- Define and version the workspace snapshot format.
- Transfer staged, unstaged, and untracked non-ignored files.
- Verify file modes, symlinks, paths, and content digests.
- Add size limits, progress reporting, and interrupted-transfer cleanup.

### Stage 5: operational hardening

- Persist event synchronization cursors.
- Use incremental conversation checkpoints.
- Add reconnecting authority and gateway tunnels.
- Add move status, resume, and abort commands.
- Exercise coordinator and worker crashes at every phase.

## Test plan

The move suite should cover:

- local to remote, remote to local, and remote to remote moves
- a delayed release from the source after target cutover
- target preparation failure with the source unchanged
- coordinator restart in every durable phase
- target runtime startup failure after cutover
- source cleanup failure without target ownership loss
- incompatible drivers and conversation formats
- concurrent move and lifecycle commands
- stale runtime events from the old executor
- clean, dirty, staged, untracked, symlink, and executable-file snapshots
- TUI and headless runtime handoff
- loss of SSH during preparation, cutover response, and cleanup

Until Stage 1 lands, choose the executor at `wi new` time or with `wi executor select` before the first materialization.
