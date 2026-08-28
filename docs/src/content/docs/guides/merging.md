---
title: Merge completed work
description: Rebase an item branch and fast-forward a local target safely.
---

`wi merge` integrates one clean working or waiting item into an existing local branch.

```bash
wi merge --item auth main
```

The operation:

1. requires a completely clean source checkout
2. rebases the persisted implementation branch onto the target
3. fast-forwards the local target reference
4. synchronizes a worktree where the target is checked out

It does not fetch, commit, squash, push, delete the source branch, release the workspace, or archive the item.

## Choose the target

Without an explicit target, `wi` tries the local branch named by `origin/HEAD`, then `main`, `master`, `develop`, or `trunk`.

```bash
wi merge --item auth
wi merge --item auth --target develop
```

The target must already exist locally.

## Failure and rollback

If rebase, target advancement, or target-worktree synchronization fails, `wi` aborts or rolls back and restores the source and target commits from before the operation.

Dirty files in the target worktree are allowed only when they do not overlap incoming paths. The source checkout must always be clean.

## Finish the work item separately

Inspect the result and use normal Git commands to push if required. Then archive the item:

```bash
wi archive auth
```

Keeping integration and lifecycle separate avoids silently discarding a useful case file or cleaning up a task before you have verified the merged result.
