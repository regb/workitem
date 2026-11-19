# Item-scoped code review

Status: implemented design after the review simplification. This replaces the earlier change-group, Git-metadata and dispatch-gate design.

Wi owns review workflow; Pierre renders source, diffs, trees and editing. The browser, CLI and other clients use generated API clients. Application use cases compose Git and coordinator operations. The coordinator is the sole structured-state writer.

## Records and identity

Each item has revisions `r1`, `r2`, threads `t1`, `t2`, drafts, published rounds `q1`, `q2`, replies, optional approvals, capture gaps and merge records. IDs are scoped to the item and do not restart after a merge. There are no change groups. Merge records establish integration boundaries without hiding earlier history.

Review content is canonical item data in the top-level `review_records` bbolt bucket. It is not a rebuildable projection. Content changes and compact events commit in the same transaction. Comment bodies, draft text and other review content stay in canonical records, not event payloads.

Git stores only code snapshots under create-only item-owned refs:

```text
refs/wi/review/<item>/snapshots/<id>
```

A snapshot commit contains a complete tree and has the observed checkout HEAD as its parent. It is not a branch commit or a submitted patch set. Each retained revision is pinned independently. Private snapshot ancestry never enters ordinary integration history.

The app creates the snapshot ref before writing its revision record. A crash or failed record write can leave an unrecorded ref. That ref is harmless. There is no metadata history, draft ref, publication intent, startup recovery scan, reconciliation, quarantine or external-edit detection workflow.

Deleting an item removes its canonical review content in the item-deletion transaction, then deletes its snapshot refs. Ref deletion failure is a partial outcome, not a reason to restore the item or start a repair job. Object pruning is not secure erasure of backups or copies.

## Bases and comparisons

The first captured checkout HEAD establishes the initial review base. It needs no additional snapshot. Each merge result establishes the base for later work on the same item. The item's immutable created-from commit is unchanged.

A revision stores its own base, observed HEAD and named branch or detached state. Historical comparisons resolve these immutable endpoints, never a moving current branch. Compare a revision with its pinned base or with any other item revision. File requests carry an expected comparison version.

Captures follow the actual claimed checkout, including stable branch switches and detached HEAD. Claim generation, checkout root and registered repository checks establish capture authority. Detected branch, HEAD or content movement during a scan rejects that observation. This does not change item identity or authorize merging a switched branch. Ordinary merge still requires the item's named branch.

## Capture without admission gates

All drivers and runtime modes use the same daemon-side app use case after an accepted `agent.settled` event. There is no opt-in, capability negotiation, version probe, pre-execution baseline snapshot or held-input queue.

One capture runs per item. Settlements during a capture coalesce into one pending rerun. Duplicate native events do not schedule another capture. Different items capture independently. Manual capture and capture-plus-reply use the same snapshot implementation and per-item capture lock.

Git work runs outside the item-operation and controller locks. A short final record write rechecks the claim and serializes publication. Reads, sends and controls remain available. Capture never changes agent status or control admission.

`ItemObservation.capturing` travels with `GetItem` and `ListItems`, including existing dashboard streams. It is not a separate RPC. While capturing, the attention view groups the item as working rather than needing attention because the agent just settled. Unread and native agent state remain separate facts.

No checkout skips silently. Other capture failures retain a visible gap and reason. They never reject a successful settlement, hold input, or replay a message. Ordinary driver follow-up queues and uncertain-delivery semantics remain independent.

Snapshots use a scratch index and preserve real HEAD, branch refs, staging, stash, file modes and symlink blobs. They do not run repository filters or follow symlinks. Git ignore rules and bounded file/total sizes govern inclusion; exclusions are visible and never make a revision unusable. There is no secret-path deny-list or promise of exclusive agent authorship. Native continuations, background work and external writers can race capture, so snapshots are best-effort observations, not precise per-run coverage claims.

## Discussion and optional approval

A comment is anchored to an immutable comparison and optionally a path, old/new side and line range. Original anchors do not move when code changes. An outdated location is not a resolved concern.

Reviewer drafts publish as an exact batch. Publishing consumes those drafts and appends thread messages and a round in one transaction. Text still being composed stays in memory and is never replaced by polling. Drafts saved to the coordinator are durable.

Agent replies form validated batches. `reply --capture` records the new revision and entire reply batch together. Addressed is evidence at a revision, not reviewer resolution. Published reviewer decisions resolve, reopen or withdraw concerns.

Only the most recent 200 retry receipts remain in the item state. Successful retries in that window return the original result without recapturing newer bytes. The coordinator's compact command history refuses expired retries rather than silently reapplying them.

Request review, request changes, approval and withdrawal are informational records. Approval is optional and never blocks merge, close or another operation. New revisions do not inherit an old revision's approval.

## One-shot notification

Round publication commits before a generic notification is attempted once. Delivery is an idle prompt or busy follow-up. The notification identifies the item, round and revision and points to the CLI; it does not copy comment bodies.

No agent, controller contention or failed delivery is a partial outcome. The round stays published. There is no review notification outbox, retry loop or agent startup. An explicit `wi review notify qN` sends again without republishing. The round retains only notified-at and the ordinary command ID. Uncertain messages are never automatically replayed.

Review uses the ordinary operator endpoint. Explicit sandbox restrictions still apply to every command. Caller headers cannot grant authority. There is no review credential or review-specific socket route. Cloud sandbox credentials are outside this design.

## One merge command

`wi merge` captures a pre-rebase input, commits uncommitted item-branch work as close does, and rebases onto the fenced target when needed. An idle online agent may merge. Only a running turn or an in-progress capture refuses merge; merge never stops the agent. Rebase conflicts leave the target unchanged and stay in the real checkout for `git rebase --continue` or `--abort`.

The default result is one squash commit with the rebased tree and the previous target as its only parent. Its message defaults to the item title and is editable in the browser. `--rebase` preserves the rebased ordinary series and fast-forwards instead. Already-contained work succeeds without an empty commit.

Target publication uses compare-and-swap. The source branch advances to the result for continued work. Canonical evidence links the pre-rebase input revision with the resulting ordinary commit/tree and before/after target tips. The result becomes the next review base.

Existing upstream synchronization, local-only mode, target selection and managed-checkout safeguards remain. Ignored source data and overlapping target-local changes are protected. Unresolved threads and unpublished drafts warn without blocking merge. Private snapshot refs are never exported as ordinary ancestry.

Successful durable steps remain if a later record or checkout operation fails. In particular, a published target is not rolled back because source advancement or target-checkout synchronization failed. Report the partial outcome; do not automatically repeat integration.

## Client workflow

```sh
wi review revisions --json
wi review checkpoint --capture-id observation-1
wi review diff r1 r2 --patch
wi review comment --revision r2 --path src/example.go --line 20 -m 'Check this case'
wi review publish --revision r2 --drafts d1
wi review threads --unresolved --json
wi review reply --capture --stdin --operation-id fixes
wi review resolve t1 --revision r3
wi review notify q1
wi merge --message 'Reviewed result'
wi merge --rebase
```

The browser separates live Git view from immutable snapshots, shows capture observations, gaps, exclusions and merge boundaries, and keeps Commit and ordinary Merge distinct from revision feedback. File editing and Git History retain their existing behavior. Browser file, comment and message buffers remain memory-only with unload warnings; only submitted records are durable.

## Verification and related docs

Tests cover canonical persistence and deletion, compact events without bodies, bounded retries, immutable anchors, atomic publication and capture-plus-reply, one-shot notifications, every runtime mode, coalesced nonblocking capture, claim fencing, Git/index preservation, no private ancestry export, merge conflicts/CAS/partial outcomes, continued work and browser draft safety.

- [Code panel and source editing](code-review.md)
- [Snapshot and discussion operations](../internal/review-checkpoints.md)
- [Runtime capture observations](../internal/review-runtime-support.md)
- [Branch authority](../internal/review-branches.md)
- [Ordinary merge](../internal/review-integration.md)
- [Endpoint authority](../internal/review-access.md)
