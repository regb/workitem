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

The checkout specification records desired state: kind, selected executor, and branch. Materialization creates a separate workspace claim containing a claim ID, monotonic generation, actual executor, repository key, slot, and path. The checkout retains the latest generation after release. A later ensure creates the next generation on the same selected executor.

Executor selection is mutable only before the first materialization. Releasing a claim does not make placement editable again. Agent runtime and terminal records carry the claim ID and generation that created them, so a stale process cannot resume against a replacement claim. This keeps placement changes distinct from a workspace move, which must transfer runtime and conversation ownership as well as Git state. See the [workspace move plan](../guides/workspace-moves/).

A managed slot may select a configured executor. The source context and item remain on the coordinator, while Git objects, checkout materialization, runtime, and optional terminal run on the worker.

```bash
wi new --executor macbook "Check macOS behavior"
```

The slot identity is scoped by executor and repository. Remote release imports committed branch history before relinquishing the worker claim. See [Remote executors](../guides/remote-executors/) for configuration and current limits.

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

Repository-home work is local-only. A repository-home item borrows that checkout. `wi` validates that it exists, is on the local default branch, and has no other active home claim. It does not switch, reset, clean, or remove the directory.

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
