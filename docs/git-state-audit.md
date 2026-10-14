# Git state ownership audit

Scope: repository registry, item source references, checkout claims, Git observations, integration/recovery plans, and legacy migration records. The audit separates current external state from Wi intent and historical snapshots. It is not a review of every agent or terminal field.

## Finding changed in this patch

The registry’s upstream URL/branch pair duplicated native remote and branch-tracking config and could drift from it. Remove it from the current repository record. Read repository-local Git config, including includes, for display, fetching, and integration. The normal `wi repo upstream` command edits origin and branch tracking atomically through the daemon. There is no fallback to a database URL. Operator-visible JSON contains a derived `RepositoryView`, explicitly marked as sourced from Git.

Keep Wi command acknowledgements, fetch observations, and frozen reconciliation targets as historical/recovery facts. They do not drive the current configuration. Past event and command payloads are not rewritten.

## Remaining fields

| Field or record | Authority and reason | Decision |
| --- | --- | --- |
| Repository ID, name, created time, archive state | Wi registration identity and availability. Git history can be shared by independent registered stores; Git does not encode their names or archive policy. | Keep canonical in Wi. |
| Repository integration branch | Wi’s chosen integration policy. A bare store’s symbolic HEAD can reflect the onboarding checkout or be changed for ordinary Git use; it does not designate Wi’s integration target. | Keep canonical in Wi. |
| Repository store path | Derived from repository ID and data root. | Already derived, not persisted as a second location. |
| Registered item repository paths/common-dir/remote URL | Legacy-shaped operational fields used in memory. Registered `Repository.MarshalJSON` persists only ID and created-from commit, and rejects those path/URL fields on registered input. | Already not duplicated in current item records. Avoid adding more hydration where it is not needed. |
| Item created-from commit | Immutable provenance: which commit the task began from. Current branch tips and reflogs change or expire. | Keep the historical SHA in Wi, pin its object in Git. |
| Item checkout branch specification | Expected branch association, not observed HEAD. It exists without a checkout and survives release and archive. Inferring it from a directory or branch naming convention would adopt manual branch switches and lose mismatch detection. | Keep canonical intent in Wi; read actual branch from Git. |
| Workspace claim path, slot, claim ID/generation | Exact resource ownership and stale-operation fencing. `git worktree list` describes resources, including warm released slots, not which item is currently authorized to use them. | Keep claims. A local path can be calculated from root/repo/slot, but that does not replace ownership or the bound claim used by an operation. |
| Worktree HEAD, current branch, dirty state, branch mismatch | External observations refreshed with `Git.WorktreeSnapshot`; cached projections feed list/attention views. | Keep rebuildable, timestamped observations. Git remains authoritative; mutations inspect it again. |
| Fetch SHA, destination, time | Snapshot of a Wi fetch. Native tracking refs are read for the current comparison; observation time is accepted only when the destination and commit still match. A native fetch can change refs without generating a Wi event. | Keep the observation, not a second current ref/configuration. |
| Sync plan old/upstream/new SHA and configuration snapshot | Frozen operation inputs, CAS guards, rollback/restart state, and detection of configuration changes during reconciliation. Re-reading only the latest refs would discard the operation’s original intent. | Keep until completion/abort; preserve compact history. |
| Recovery/pin refs and matching IDs/SHAs in events | Git refs retain objects; Wi identifies why and when those refs were created. Reflog expiry or later ref changes must not rewrite history. | Keep both roles. |
| Source/branch/dirty-file copies in offline migration plans | Original inventory and interrupted-migration recovery, not current repository authority. Dirty snapshots intentionally retain staged/unstaged/untracked content absent from ordinary commit history. | Preserve legacy tools and protected plans. No automatic cleanup. |

## Limits and follow-up candidates

- Runtime metadata is not persisted just because it can be calculated, but logical ownership and historical facts must not be replaced by an observation.
- The legacy operational repository field names can be misleading in memory. Their serializer already excludes them for registered repositories; renaming/removing them would require a separate legacy-tool audit.
- Local claim paths have some representational redundancy with slot IDs and the data root. Removing the path should be considered only with operation-fencing and relocation tests, not folded into upstream cleanup.
- Git tracking defined outside repository-local configuration, local-branch tracking, ambiguous merge/URL values, and caches mapped onto local heads are not accepted for remote synchronization. Unsupported states should be reported, not inferred from database copies.
