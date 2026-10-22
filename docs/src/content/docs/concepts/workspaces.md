---
title: Workspaces
description: Learn how Wi assigns managed Git worktrees and preserves task branches.
---

A workspace is a Git checkout claim, not a tmux session or an agent process. All new items use managed slots from a [registered repository](../repositories/).

The checkout specification records the implementation branch and materialization policy. A nullable claim records the current assignment, including its path and identity. Release clears the claim but preserves the specification and last claim generation. Every later assignment receives a new identity and higher generation, even when reusing a directory. Runtime and terminal records bind to that assignment to fence stale owners.

## Additional Git worktrees

You can create additional Git worktrees sharing the owned repository. Keep them outside Wi\'s reserved `worktrees/<repository-id>/slot-*` namespace. Git registration alone does not make a checkout a Wi workspace.

Wi does not adopt those external checkouts, inspect their in-progress Git operations, synchronize their files, or prune their stale registrations. A missing review checkout or an unrelated external rebase does not block repository synchronization or item merging. Wi still checks its own slots, including warm slots without an active claim.

Shared Git refs still require protection. Wi will not advance the integration branch while it is checked out anywhere. It also refuses to merge into an explicitly selected branch checked out in an external worktree, rather than changing that checkout behind your back. Manage those checkouts with Git yourself.

## Managed slots

Creation records the repository ID, implementation branch, and created-from commit, but leaves the checkout absent:

```bash
wi new --title "Add pagination"
wi workspace status add-pagination
wi start add-pagination
```

Run these creation commands inside an existing item, or add `--repo NAME`. Starting work assigns a reusable worktree slot. If the task branch does not yet exist, Wi creates it at the recorded starting commit. Later materializations use the existing task branch tip, not the original base.

You can ensure a workspace without starting an agent:

```bash
wi workspace ensure add-pagination
wi workspace release add-pagination
```

Release refuses while an active runtime or terminal depends on the checkout. It also requires the recorded implementation branch and a clean worktree. The slot directory is retained for reuse, and the task branch survives release.

## Branch divergence

Checking out another branch or detaching HEAD in an assigned slot is reported as `changed`, with branch-divergence details. Inspection, navigation, and agent access remain available. Release and merge still require the recorded implementation branch. Wi does not automatically switch branches to repair divergence.

## Starting commit versus current progress

Without `--base`, creation records the registered integration branch commit without fetching. Stacking on the current task requires an explicit contextual base such as `HEAD`. See [Starting commits](../repositories/#select-a-starting-commit) for fail-closed resolution rules.

The database retains immutable provenance and the task branch name. The Git branch ref, not the database or slot occupancy, records the latest committed task tip. Dirty files belong to the assigned worktree and are not implicitly copied to another item.

## Workspace status

Worktree observation is separate from agent activity:

```bash
wi workspace status add-pagination
wi agent status add-pagination
wi list
```

An idle agent may leave changes for review; a busy agent may have a clean checkout. Lifecycle changes alone do not release either resource.
