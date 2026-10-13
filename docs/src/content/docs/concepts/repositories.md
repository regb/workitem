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

Register only repositories whose project-local agent resources you trust. Registration authorizes those resources in verified managed worktrees, including executable Pi extensions, project settings, MCP servers, and package loading. The bundled Pi driver receives `--approve` for each such launch, without writing a global or parent-directory trust decision. Future commits and local changes in those worktrees are covered by this repository-level policy. Unregistered paths and worktrees belonging to another Git store are not automatically approved. Direnv `.envrc` approval remains separate.

The integration policy is stored in the registry; it is not rediscovered when creating or merging items. There is currently no command to change it. The bare store has no permanent integration checkout.

Normal `wi repo list` output shows active repositories by name, integration branch, state, and upstream URL/branch or `local-only`. `wi repo list --all` includes archived repositories, marked `archived`. `--json` retains the stable ID and other registry metadata; `repo show` also prints the ID and Git store path.

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

An upstream is the integration branch’s optional remote destination, stored as a URL and branch together. Wi does not keep a separate `origin_url` or the checkout used for onboarding in the repository record.

URL onboarding uses that URL and the integration branch as upstream. Local onboarding uses the checkout’s origin URL when available. If the local integration branch already tracks a differently named origin branch, Wi preserves that remote branch name; otherwise it uses the integration branch name. Without an origin, upstream stays empty. Existing registered repositories are not retroactively configured.

```bash
wi repo add project --from git@example.com:owner/project.git --branch main
wi repo add local-project --from /path/to/repository
wi repo add offline-project --from /path/to/repository --local
# Override inference or configure an existing repository:
wi repo upstream project git@example.com:owner/project.git --branch main
wi repo upstream project --clear
```

Use `repo add --upstream URL --upstream-branch BRANCH` to override inference. URLs are credential-free; authentication stays in Git credential helpers or SSH. The selected URL is stored directly, so later edits to Git remote aliases do not silently change Wi’s integration destination. The original checkout remains independent. Onboarding imports Git history but does not fetch the inferred upstream again.

```json
"upstream": {
  "url": "git@example.com:owner/project.git",
  "branch": "main"
}
```

Omitting `upstream` means local-only integration.

```bash
wi repo status project          # cached observation; no network access
wi repo status project --fetch # inspect the current upstream without integrating
wi repo sync project           # fetch and fast-forward the integration branch
```

Status reports `untracked`, `unfetched`, `equal`, `ahead`, `behind`, or `diverged`, with ahead/behind counts and the last Wi fetch time when known. Cached observations do not prove the remote is unchanged.

Synchronization advances a behind branch, leaves an ahead branch alone, and refuses divergence by default. It never pushes or rebases integration history. Choose explicitly when histories diverge:

```bash
wi repo sync project --merge
wi repo sync project --reset-to-upstream --yes
```

`--merge` preserves both histories in a dedicated detached reconciliation checkout. If there are conflicts, the error and `repo status` report its path. Resolve and stage the files there, then run `wi repo sync project --continue`. `--abort` leaves integration unchanged and refuses to discard unrelated dirty or untracked files. Pending reconciliation survives daemon restart and blocks upstream changes and item integration until finished or aborted.

`--reset-to-upstream --yes` replaces the owned integration tip with the fetched upstream. Wi shows the exact tips and up to 20 local-only commit IDs before applying the operation. It checks that the confirmed tips have not changed, and retains the old tip under `refs/wi/recovery/repository-sync/<operation-id>`. Item branches, their workspaces, the original checkout, and the remote remain unchanged. The old commits are removed from the integration branch’s history, not deleted from the store. Synchronization rejects active integration, active Git operations, and a checked-out integration branch.

With an upstream configured, `wi merge` synchronizes the registered integration base before rebasing an item. Use `wi merge --local` for deliberate offline/local integration. Other explicitly selected target branches remain local-only. Creation does not fetch; sync before creating an item if you want its base to include current upstream changes.

Publishing stays explicit. Use Git against the store reported by `wi repo show` to push. See [Merge completed work](../../guides/merging/) for integration and rollback rules.
