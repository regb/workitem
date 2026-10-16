---
title: Merge completed work
description: Fast-forward a local target, rebasing only when needed.
---

`wi merge` integrates one clean active item into an existing local branch.

```bash
wi merge --item auth main
```

The operation:

1. requires a completely clean source checkout
2. fetches and fast-forward-synchronizes a configured upstream when targeting the registered integration branch, unless `--local` is supplied
3. keeps the implementation branch unchanged if it already contains the target; otherwise rebases it onto the target
4. fast-forwards the local target reference
5. synchronizes a checked-out non-integration target when safe

When the target is already an ancestor, Wi preserves all commit IDs and merge history, including subtree merges. Otherwise it uses a plain rebase, which can flatten merge history and require manual resolution.

Without an upstream, or with `--local`, integration remains local-only. It does not commit uncommitted work, squash, push, delete the source branch, release the workspace, or archive the item. Upstream divergence stops before the item is rebased; resolve it explicitly with `wi repo sync NAME --merge` or `--reset-to-upstream --yes`. See [Repositories](../../concepts/repositories/#fetch-merge-and-push) for synchronization and recovery.

## Choose the target

Without an explicit target, Wi uses the registered repository’s integration branch. This policy is chosen at onboarding, not rediscovered from `origin/HEAD`.

```bash
wi merge --item auth
wi merge --item auth --target develop
```

The target must already exist in the owned store. Wi refuses to advance the registered integration branch while it is checked out there; release that checkout before merging. The original external onboarding checkout is independent and is not synchronized.

## Failure and rollback

If item rebase, target advancement, or target-worktree synchronization fails, `wi` aborts or rolls back that item integration. An earlier successful upstream synchronization is retained and reported, rather than rolling the integration branch back to a stale remote base. Its old tip remains under the reported recovery ref.

For an explicitly selected non-integration target, dirty files in its worktree are allowed only when they do not overlap incoming paths. The source checkout must always be clean.

## Finish the work item separately

Inspect the result and use normal Git commands to push if required. Then close the item:

```bash
wi close auth
```

Verify the integration before closing. The task record remains available; file further objectives as new items.
