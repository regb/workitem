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

A reused slot keeps its untracked and ignored files, such as `node_modules` or build caches, so it starts warm. A setup command that wants a fresh slot can run `git clean` itself.

## Repository files and setup

Some files a checkout needs are deliberately not in Git, such as a `.env`. Wi keeps them per repository instead, at the path they take in a checkout:

```bash
wi repo files add .env                       # from the current directory
wi repo files add biskoui certs/dev.pem --from ~/certs/biskoui.pem
wi repo files list
```

Every new checkout claim is set up before an agent starts in it:

1. The repository files are copied to their paths, replacing what a reused slot held there. A file the branch tracks at the same path wins: the copy is skipped with a warning. Copies of files since removed are deleted unless they were edited. Wi lists the files in the owned store's `info/exclude`, which every worktree reads, so Git ignores them without any `.gitignore` change.
2. The setup command runs in the checkout with `/bin/sh`, if one is configured.

The setup command is the repository setting `plugins.setup.run`. A repository may also declare one in its checked-in `.config/wi.toml`, read from its default branch so a branch cannot change it; it runs only once the operator sets `plugins.setup.trust` to `always`:

```toml
[workspace.setup]
run = "script/setup"
```

```bash
wi repo settings set biskoui plugins.setup.run 'npm install'
wi repo settings set biskoui plugins.setup.trust always
```

The command runs without the direnv environment, since it may create files an `.envrc` needs. It gets `WI_ID`, `WI_WORKTREE`, `WI_REPOSITORY_ID`, `WI_SLOT`, `WI_SLOT_REUSED` (`1` when another claim used the slot before), and `WI_FILES`, a private copy of every repository file that lives only for the run. List files that should not be copied automatically in `plugins.files.skip`; setup can still copy or transform them from `$WI_FILES`. It must finish within `plugins.setup.timeout_seconds` (600 by default). Its output goes to a log under XDG state, and `wi workspace status` shows the outcome.

Setup runs once per claim; resuming an agent on the same claim does not repeat it. If it fails, the item stays active with its checkout claimed and no agent starts. Fix the cause and run `wi workspace setup`, which repeats setup and refuses while an agent runs in the checkout. `wi start`, `wi resume` and `wi reopen` accept `--skip-setup` to start the agent without setting up a new claim.

## Environment variables

A repository can have environment variables, kept by the daemon rather than in a file:

```bash
wi repo env set DATABASE_URL=postgres://localhost/app PORT=8080
wi repo env list            # values hidden; --show-values prints them
wi repo env unset PORT
```

Wi sets them for every agent runtime (TUI and headless, including the item's tmux session) and setup command it starts in the repository's checkouts. A repository's `.envrc`, when direnv loads it, applies after them. They are passed to the process as it starts, never written to a file, so they work the same wherever the checkout runs; a project that needs a `.env` on disk can write one in its setup command. Common `.env` loaders leave variables that are already set alone, so these take precedence over a committed `.env`. Changes apply to agents and setup started afterwards. Values may be secrets: history records which keys changed, never values. `WI_*` names, `TMUX` and `TMUX_PANE` are reserved.

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
