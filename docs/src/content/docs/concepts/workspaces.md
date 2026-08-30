---
title: Workspaces
description: Learn how wi assigns and reuses Git checkouts.
---

In `wi`, a workspace is a Git checkout claim. It is not a tmux session and it is not an agent process.

## Managed slots

Most items use a managed slot. Creation records the repository, implementation branch, and created-from commit, but leaves the checkout absent:

```bash
wi new "Add pagination"
wi workspace status add-pagination
```

`wi start` or `wi workspace ensure` assigns a reusable worktree slot and checks out the item's persistent implementation branch. Releasing the workspace keeps the slot available for another item rather than creating an unlimited number of directories.

```bash
wi workspace ensure add-pagination
wi workspace release add-pagination
```

Release refuses while an active runtime or terminal depends on the checkout.

## Relocated repositories

If the source repository checkout moves after item creation, update an item before materializing its workspace:

```bash
wi workspace relocate --repository ~/vcs/new/project --item add-pagination
```

`wi` verifies that the replacement checkout has the recorded origin and contains the item's created-from commit. The workspace must be absent. Relocation records the current operational path while retaining the original creation path as provenance.

## Repository-home claims

Some work must happen in the repository's primary checkout on its local default branch:

```bash
wi new --home "Update release metadata"
```

A repository-home item borrows that checkout. `wi` validates that it exists, is on the local default branch, and has no other active home claim. It does not switch, reset, clean, or remove the directory.

Only one active item can claim a repository home at a time.

## Created-from commit and implementation branch

Without `--base`, creation records the commit from the local default branch. It does not fetch. Use an explicit base for stacked or unusual work:

```bash
wi new --base HEAD "Follow up on current branch"
wi new --base release-branch "Prepare release fix"
```

The item's managed implementation branch persists even if the physical slot is released and later reassigned.

## Workspace status

Worktree observation is reported separately from agent activity:

```bash
wi workspace status add-pagination
wi agent status add-pagination
wi list
```

This matters when an idle agent has left changes for review, or when an agent is busy in a clean checkout.
