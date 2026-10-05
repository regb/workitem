# Remote checkpointing design questions

Status: design exploration. This document does not define an implementation yet.

Remote execution has three separate synchronization problems. Treating all three as "checkpointing" hides the decisions we need to make.

1. The coordinator needs fast, authoritative control-plane projections for `wi list`, `wi next`, attention, lifecycle, and ownership fencing.
2. A workspace move needs a portable handoff artifact while the source executor is still available.
3. Recovery from permanent executor loss needs durable copies outside the source executor before the loss happens.

Only the first requirement exists for every remote item. The second exists when a move is requested. The third is a product guarantee we have not chosen.

## Confirmed architecture

The coordinator is the centralized control plane. It owns:

- work-item identity and lifecycle
- checkout specification
- accepted workspace claim ID and generation
- primary conversation identity and assigned executor
- runtime ownership and capabilities
- compact semantic events and read projections
- move transactions and cutover decisions

This does not imply that the coordinator must store every byte produced by an executor.

The executor is the active data plane for a remotely running item. It owns the live instances of:

- the managed worktree
- staged, unstaged, and untracked workspace state
- the driver-native conversation file
- the runtime process
- persistent agent and shell terminals

The unresolved question is where portable or recoverable copies of workspace and conversation content should live, and when to create them.

## Fast list and attention do not require snapshots

`wi list`, `wi next`, and attention need bounded semantic observations, not native conversation files or worktree archives.

The coordinator needs facts such as:

- runtime ready, busy, settled, stopped, or failed
- turn and tool activity sequence
- current runtime ID
- current workspace claim ID and generation
- current HEAD
- expected and actual branch
- clean or dirty worktree
- observation timestamp and freshness
- terminal availability when relevant

These facts fit compact events and projections. They should synchronize through durable cursors. List rendering must not open SSH connections to every executor.

A native conversation export is unnecessary for this path. Parsing a coordinator-side copy of the full conversation is one way to derive projections, but it is not the only way and is expensive for remote execution. Drivers or worker observers can emit the same bounded semantic facts directly.

## Current implementation

The worker currently exports the entire native conversation file during coordinator-initiated operations such as runtime ensure, status, control, and shutdown. The coordinator imports that file into the canonical item directory. Its conversation importer can then update local projections.

This mechanism mixes two roles:

- projection synchronization
- content retention

It does neither completely. Full-file transfer is expensive, and the imported copy may lag indefinitely when no coordinator request runs. It also excludes dirty workspace state, so it is not a complete recovery point.

Do not assume the current full-file copy is the final checkpoint architecture.

## Four possible guarantees

We need to choose which guarantees `wi` promises.

### Control-plane continuity

The coordinator survives worker disconnection and continues to show the last known item, runtime, and worktree observations.

This requires compact events, observation freshness, and durable cursors. It does not require conversation or workspace snapshots.

### Planned movement

A user can move an item while the source executor is available.

The coordinator quiesces the source, asks it to export portable workspace and conversation artifacts, validates a target reservation, and cuts over ownership transactionally. The artifact may be streamed through the coordinator without becoming a retained coordinator copy.

### Worker-loss recovery

A user can recover recent uncommitted work and conversation history after the source executor is permanently lost.

This requires periodic durable copies outside the source executor. A move-only export cannot provide this guarantee because the source no longer exists when recovery begins.

### Long-term archival

A user can archive native conversation and workspace artifacts independently of the worker lifecycle.

This adds retention, deletion, privacy, encryption, quota, and export requirements. It should not be introduced accidentally as a side effect of list projections.

## Artifact placement choices

Centralized control does not require centralized byte storage. There are several models.

| Model | Strengths | Costs and failure behavior |
| --- | --- | --- |
| Source-pinned | No routine transfer; simplest active execution | Move and recovery require the source to remain available |
| Coordinator-local artifact store | Simple authority and recovery model; no third service | Coordinator disk and bandwidth grow with conversations and workspaces; sensitive content is duplicated |
| External object store | Durable, resumable, and scalable | Requires credentials, provisioning, encryption, retention policy, and offline behavior |
| Direct source-to-target transfer | Coordinator stores metadata only; avoids double storage | Source must remain online; networking and authentication become more complex; no worker-loss recovery |
| Stream through coordinator without retention | Fits coordinator-owned SSH topology and move orchestration | Source must remain online; retry and resume need temporary bounded storage |

The current implementation behaves like an incomplete coordinator-local store for conversations and a source-pinned store for dirty workspace state.

## Requirements before choosing a design

### Failure model

Decide what failures must preserve uncommitted work:

- transient SSH loss
- worker process or daemon restart
- worker host reboot
- worker disk loss
- executor removal from configuration
- coordinator loss

Transient transport loss already preserves worker state. Permanent worker loss is the decision that forces off-worker artifacts.

### Recovery point objective

If worker-loss recovery is required, define the acceptable loss window. Examples include:

- only explicit checkpoints
- last completed agent turn
- no more than five minutes
- clean commits only

Without a stated recovery point objective, "checkpoint at turn end" is an attractive guess rather than a requirement.

### Recovery time objective

Define how quickly a large item must become usable on another executor. This affects chunking, deduplication, compression, and whether checkpoints should be pre-positioned.

### Content scope

Define what a portable workspace artifact contains:

- repository identity
- base and HEAD commits
- implementation ref
- index state
- tracked worktree changes
- untracked non-ignored files
- executable bits
- symlink targets
- submodule state if supported

Ignored files, build caches, secrets outside the repository, sockets, devices, and arbitrary absolute paths should remain excluded unless a later design explicitly includes them.

Define conversation portability separately through the driver protocol. A driver may use one append-only file, several files, a database, or an external service.

### Consistency boundary

Workspace and conversation artifacts must describe a coherent point in execution. Options include:

- stopped runtime
- quiesced and settled runtime
- driver-provided snapshot barrier

A `turn.ended` event may be sufficient only if the driver contract guarantees that conversation content and tool-created files are flushed before the event. `agent.settled` is a better semantic candidate, but it still needs a written contract.

### Authority and fencing

Every artifact descriptor must include:

- item ID
- source executor
- workspace claim ID and generation
- runtime ID when a runtime exists
- conversation ID and format
- event sequence or barrier identity
- content digest
- artifact format version

A delayed upload from a superseded claim must not become the current recovery artifact.

### Transfer behavior

Large artifacts need:

- bounded chunks
- resumable upload and download
- digest verification
- size and file-count limits
- safe relative paths
- cancellation and garbage collection
- progress reporting
- backpressure so checkpointing does not stall an active turn

The existing line-oriented coordinator protocol should not carry unbounded JSON byte fields.

### Retention and privacy

Choose:

- how many checkpoints to retain
- whether archival differs from recovery retention
- when release, archive, and delete remove artifacts
- whether artifacts are encrypted at rest
- whether sensitive conversation and source content may leave the executor
- quota behavior when a checkpoint is too large

## Move requirements

Movement needs centralized transaction control, not necessarily permanent centralized artifact storage.

A move can use this sequence:

1. Coordinator creates a durable move record and target reservation.
2. Source runtime stops or reaches a defined quiescence barrier.
3. Source exports a versioned workspace artifact and driver-owned conversation artifact.
4. Coordinator records descriptors and digests.
5. Target imports and validates both artifacts while still reserved.
6. Coordinator transactionally changes executor selection and advances the workspace claim generation.
7. Target runtime starts.
8. Source cleanup becomes a retryable obligation.

For a clean, inactive workspace, the existing Git bundle may be the entire workspace artifact. Dirty workspace support adds a separate snapshot format.

The artifact payload could be streamed through the coordinator during this transaction. Permanent coordinator retention is needed only if movement must survive source loss after export or if worker-loss recovery is a separate guarantee.

## Recommended direction for the next design step

Use a staged decision rather than adding turn-end uploads now.

1. Finish durable event cursors and observation freshness. This solves the fast list and attention requirement without native snapshots.
2. Define an explicit portable checkpoint interface for an on-demand, stopped move. Keep artifact metadata separate from payload storage.
3. Implement clean cold moves using Git bundles and driver export on demand.
4. Define the dirty workspace artifact and a consistency barrier for conversation plus workspace state.
5. Decide whether worker-loss recovery is a product requirement. If it is, choose an artifact store and recovery point objective before adding periodic or settled uploads.
6. Remove routine full-file conversation transfer from status and control once projections no longer depend on coordinator-side native parsing.

The likely baseline is:

```text
coordinator
  owns metadata, projections, fencing, and move transactions

executor
  owns live mutable workspace and conversation content

portable artifacts
  created on demand for move or explicit backup

external durable checkpoints
  added only if worker-loss recovery becomes a stated guarantee
```

This keeps the control plane centralized without assuming that all execution data belongs on the coordinator disk.

## Open decisions

- Must `wi` recover uncommitted work after permanent worker loss?
- If yes, what is the acceptable loss window?
- Is coordinator-local storage sufficient, or is an external artifact store required?
- Should users be able to disable conversation or workspace retention?
- What event or driver barrier guarantees a coherent checkpoint?
- Does archive retain native conversation content indefinitely?
- Should committed Git state be checkpointed separately, or can repository bundles remain on-demand?
- How are large generated but non-ignored files handled?
- What quotas and failure messages apply when checkpointing cannot complete?
