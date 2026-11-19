# Retained review snapshots

Review content is canonical item data in the coordinator's top-level `review_records` bbolt bucket. It is not a rebuildable projection. Revisions, threads, drafts, rounds, replies, optional approvals, merge records, capture gaps, and recent retry receipts are committed with compact events. Bodies stay in canonical records, never event payloads.

Git stores code only, under create-only `refs/wi/review/<item>/snapshots/<id>` refs. The app creates the ref before storing its revision. A crash or failed record write can leave a harmless unrecorded ref. There is no metadata ref, publication intent, recovery scan, reconciliation, quarantine, or Git-content repair path.

Revision, thread and round identities are item-scoped `rN`, `tN` and `qN`. The first captured checkout HEAD establishes the base without another snapshot. Merge results establish subsequent bases. Captures follow the actual claimed checkout and record its branch or detached HEAD; ordinary merge source semantics are unchanged.

## Capture

The daemon triggers the app use case after accepted agent settlement for every driver and mode. One capture runs per item; further settlements coalesce into one pending rerun. Manual and capture-plus-reply operations use the same snapshot function. Capture is observational, not agent busy state, and never holds sends, controls, or native execution. Missing checkouts skip silently. Other failures create a visible gap/reason.

Snapshots use a scratch index and preserve user HEAD, branch, staging, stash and working files. Git ignore rules and bounded file/total sizes govern inclusion. There is no secret-path deny-list. Exclusions are reported and never make a revision unusable.

```sh
wi review revisions --json
wi review checkpoint --capture-id manual-observation
wi review capture-status --json
wi review diff r1 r2 --patch
```

## Discussion and notification

Save reviewer drafts against immutable comparisons, then publish exact draft IDs as a round. Publishing consumes the selected drafts and appends messages atomically. Agent replies can mark a concern addressed but cannot resolve it. Only published reviewer decisions resolve, reopen or withdraw concerns.

```sh
wi review comment --revision r2 --path source.go --line 12 -m 'Check this case'
wi review publish --revision r2 --drafts d1
wi review threads --unresolved --json
wi review reply --revision r3 --thread t1 -m 'Fixed' --addressed
wi review reply --capture --stdin --operation-id fixes
wi review notify q1
```

Capture-plus-reply stores the new revision and whole validated reply batch together. Successful retry identities return the original result without recapturing newer files. Only the most recent 200 receipts are retained. Older retries are refused using the coordinator's compact command record rather than silently reapplying mutations.

Publication commits before one generic notification attempt: idle prompt or busy follow-up. No agent, controller contention and delivery failures are partial outcomes; the round remains published. The browser's published-round section and `wi review notify qN` provide explicit notification actions. A failed browser attempt retains its command identity for a manual retry; nothing retries automatically. There is no review notification outbox or retry worker. A round stores only notified-at and the ordinary command ID. Uncertain messages are never replayed automatically.

Approval is informational and never blocks operations. Deleting an item removes its canonical review records in the same transaction, then deletes its snapshot refs. Ref cleanup failure reports a partial outcome, without retaining the item or creating a repair job.
