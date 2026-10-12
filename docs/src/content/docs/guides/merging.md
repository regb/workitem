---
title: Merge completed work
description: Fast-forward a local target, rebasing only when needed.
---

`wi merge` integrates one clean working or waiting item into an existing local branch.

```bash
wi merge --item auth main
```

The operation:

1. requires a completely clean source checkout
2. keeps the implementation branch unchanged if it already contains the target; otherwise rebases it onto the target
3. fast-forwards the local target reference
4. synchronizes a checked-out non-integration target when safe

When the target is already an ancestor, Wi preserves all commit IDs and merge history, including subtree merges. Otherwise it uses a plain rebase, which can flatten merge history and require manual resolution.

It does not fetch, commit, squash, push, delete the source branch, release the workspace, or archive the item.

## Choose the target

Without an explicit target, Wi uses the registered repository’s integration branch. This policy is chosen at onboarding, not rediscovered from `origin/HEAD`.

```bash
wi merge --item auth
wi merge --item auth --target develop
```

The target must already exist in the owned store. Wi refuses to advance the registered integration branch while it is checked out there; release that checkout before merging. The original external onboarding checkout is independent and is not synchronized.

## Failure and rollback

If rebase, target advancement, or target-worktree synchronization fails, `wi` aborts or rolls back and restores the source and target commits from before the operation.

For an explicitly selected non-integration target, dirty files in its worktree are allowed only when they do not overlap incoming paths. The source checkout must always be clean.

## Finish the work item separately

Inspect the result and use normal Git commands to push if required. Then archive the item:

```bash
wi archive auth
```

Verify the integration before archiving. The task record remains available; file further objectives as new items.
