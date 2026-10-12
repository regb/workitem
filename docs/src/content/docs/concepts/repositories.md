---
title: Repositories
description: Register independent Git stores, select task bases, and rename or archive repositories.
---

A registered repository is a named, Wi-owned bare Git store. Work items reference its stable ID and an immutable created-from commit. Their managed worktrees share that store, not your original checkout.

## Register a repository

```bash
wi repo add project --from /path/to/repository --branch main
wi repo list
wi repo show project
```

Local onboarding imports committed Git state into an independent store under `$DATA_ROOT/repositories/<id>.git`. It does not copy dirty files, change your external checkout, or depend on it afterward. The import does not use hardlinks or object alternates.

`--branch` selects the integration branch. When omitted for a local source, Wi discovers it once from the local branch corresponding to `origin/HEAD`, then `main`, `master`, `develop`, or `trunk`. If discovery fails, choose explicitly. URL onboarding always requires `--branch`:

```bash
wi repo add project --from https://github.com/example/project.git --branch main
```

The integration policy is stored in the registry; it is not rediscovered when creating or merging items. There is currently no command to change it. The bare store has no permanent integration checkout.

Normal `wi repo list` output shows active repositories by name and integration branch. `wi repo list --all` includes archived repositories, marked `archived`. `--json` retains the stable ID and other registry metadata; `repo show` also prints the ID and Git store path.

## Select the repository for new work

Outside a Wi item, choose a registered name:

```bash
wi new --title "Fix token refresh" --repo project --desc-file task.md
```

Inside an item, omit `--repo` to use that item's repository. An explicit name overrides it. Running in an ordinary Git checkout does not automatically register or select that checkout.

All items use managed slots. Moving or removing an external onboarding checkout does not affect Wi's store.

## Select a starting commit

Creation resolves the base immediately and records the resulting commit. It does not fetch or copy dirty files.

| Base | Resolution |
| --- | --- |
| Omitted or `^` | Registered integration branch in the owned store |
| Branch, tag, commit hash, or `main~1` | Owned store |
| `HEAD`, `HEAD~1`, `@`, reflog expressions, or pseudo-refs | Current claimed worktree |
| `-` | Current worktree's `@{-1}` |

```bash
wi new --title "Independent task" --base=^
wi new --title "Build on this task" --base=HEAD
wi new --title "Prepare release fix" --base=release-branch
```

Contextual expressions require a current item, a present claim, an actual current directory inside that checkout, and the same repository. A nested unrelated Git repository, missing Git revision, or cross-repository `--repo` fails. Explicitly selecting the same repository is allowed. Wi never silently resolves worktree-relative meanings in the bare store.

The created-from commit is provenance, not the current task tip. The task branch in the owned store tracks committed progress and survives workspace release. See [Workspaces](../workspaces/) for materialization and branch-divergence rules.

## Rename without moving resources

```bash
wi repo rename project platform
wi repo show platform
```

Names are unique, flat human-facing selectors. Rename changes only the name: IDs, stores, branches, item references, claims, and conversations stay unchanged. The old name stops resolving. Renaming to the current name is a no-op; conflicts and invalid names fail.

## Archive without deleting Git data

```bash
wi repo archive project
wi repo list --all
wi repo restore project
```

Archiving hides a repository from the default list and rejects new items, whether selected with `--repo` or inherited from the current item. The name remains reserved. Use `repo show` to inspect it or `repo list --all --json` to see its `archived: true` metadata.

Existing items remain usable, including their normal workspace, agent, and merge operations. Archiving does not stop running agents or change item lifecycle, claims, conversations, or descriptions. It preserves the Git store, implementation branches, unpushed commits, dirty and untracked files, and the original external checkout. No push or cleanliness check is needed because nothing is deleted.

Restore makes the same repository available for new items again. It does not materialize a workspace or runtime. Repeated archive or restore requests are no-ops when already in the requested state. Both commands also accept a stable repository ID. Wi records `repository.archived` and `repository.restored` events.

There is no repository deletion command.

## Fetch, merge, and push

Onboarding may record a sanitized origin URL. Fetching the owned store updates remote-tracking refs, not its local integration or task branches. There is no automatic fetch during creation or merge. Use Git explicitly against the store reported by `wi repo show` when fetching or pushing.

`wi merge` defaults to the registered integration branch. It refuses to advance that branch if it is checked out in the owned store. See [Merge completed work](../../guides/merging/) for integration and rollback rules.
