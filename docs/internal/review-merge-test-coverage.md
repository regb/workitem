# Merge test coverage after redesign

This accounts for every test removed from the three original merge test files at `171981e`. The rewritten behavior commits leftovers, rebases the actual checkout, squashes by default, and preserves successful durable steps on later failure. `--rebase` retains ordinary fast-forward/series behavior. No detached dirty-snapshot checkout is created.

The replacement tests are in `internal/app/integration_merge_test.go`, `integration_merge_safety_test.go`, `integration_merge_regressions_test.go`, `integration_merge_retention_test.go`, and isolated CLI/browser tests.

## Original integration_merge_dirty_test.go

| Removed test | Replacement coverage or intentional change |
| --- | --- |
| `TestDirtyMergeFailuresPreserveOriginalCheckout` | Exact dirty index/HEAD preservation intentionally went away: leftovers are committed first. Conflict retention is covered by `TestMergeRebaseConflictLeavesCheckoutResolvableAndTargetUntouched`; target/source CAS by `TestMergeTargetAndSourceCASFailuresRetainSuccessfulSteps`; sync failure by `TestMergeTargetSyncFailureRetainsPublicationAndSource`; overlap after rebase by `TestMergeOverlappingDirtyTargetAfterRebaseRetainsBothCheckouts`. These retain source work and protect unpublished targets, rather than rolling everything back. |
| `TestMergeRejectsUnmergedIndexWithoutOperationMarker` | Retained under the same test name in the regression file. It checks refusal and byte-equivalent staged index entries. |
| `TestDirtyMergeReportsRetainedTemporaryCheckout` | Intentionally removed: there is no detached temporary checkout or cleanup failure path to report. Real-checkout conflicts remain resolvable, and committed leftovers survive later failure. |
| `TestDirtyMergeCleansTemporaryCheckoutAfterCancellation` | Temporary cleanup is obsolete. `TestMergeCancellationRetainsCommittedLeftoversWithoutTemporaryWorktree` verifies cancellation keeps committed leftovers, leaves the target untouched, and does not add a worktree. |
| `TestDirtyMergeRejectsConcurrentSourceCommitWithoutReset` | `TestMergeRejectsConcurrentSourceCommitWithoutReset` injects a source commit before publication and verifies refusal, retained concurrent HEAD/content, and unchanged target. |

## Original integration_merge_fast_forward_test.go

| Removed test | Replacement coverage or intentional change |
| --- | --- |
| `TestMergeWorkItemFastForwardFailuresDoNotResetSource` | CAS failures are covered by `TestMergeTargetAndSourceCASFailuresRetainSuccessfulSteps`, dirty overlap by `TestMergeProtectsIgnoredSourceAndDirtyTargetFiles`, and sync failure by `TestMergeTargetSyncFailureRetainsPublicationAndSource`. The old expectation that sync failure rolls the already-published target back intentionally went away. |
| `TestMergeWorkItemFailsClosedWhenAncestryCheckFails` | `TestMergeFailsClosedWhenAncestryCheckFails` injects an ancestry error and checks no rebase or source/target ref change. |
| `TestMergeWorkItemFastForwardsWithoutRebase` | `TestMergeRebasePreservesSeries` and `TestMergeRebaseFastForwardPreservesSubtreeMerge` exercise `--rebase` fast-forward behavior. The latter makes any attempted rebase fail, so a successful run proves it was skipped. Default merge intentionally squashes instead. |
| `TestMergeWorkItemFastForwardPreservesSubtreeMerge` | `TestMergeRebaseFastForwardPreservesSubtreeMerge` ports the subtree fixture, uses `--rebase`, verifies the exact tip and both parents, checks root/vendor content in both checkouts, and forbids rebase. |

## Original integration_merge_test.go

| Removed test | Replacement coverage or intentional change |
| --- | --- |
| `TestMergeWorkItemRebasesFastForwardsAndSyncsTargetWorktree` | `TestMergeSyncsManagedTargetWorktreeAndRecordsItsEvent` verifies a real checked-out managed target, required rebase, checkout synchronization and nonoverlapping local-data preservation. Default final publication is now squash; ordinary-series publication is covered separately. |
| `TestMergeWorkItemExplicitTargetWithoutWorktree` | `TestMergeExplicitTargetWithoutWorktree` checks target selection, ref advancement and no checkout-sync claim. |
| `TestMergeWorkItemRejectsMissingRegisteredTargetWorktreeBeforeRebase` | `TestMergeMissingRegisteredTargetRefusesBeforeRebase` retains the deleted-directory fixture, checks the explicit unavailable-worktree error, forbids rebase, and verifies source HEAD stays unchanged. |
| `TestMergeWorkItemPreservesDirtySource` | Keeping leftovers uncommitted intentionally went away. `TestMergeCommitsLeftoverKindsBeforeIntegration` covers untracked, unstaged, staged, mixed, deleted, renamed, ignored and ignored-only cases under the new semantics. `TestMergeProtectsIgnoredSourceAndDirtyTargetFiles` checks ignored overwrite refusal; `TestMergeSquashesDirtyAndCommittedWorkAndAdvancesContinuation` covers divergent-target rebase plus committed/dirty work and clean continuation. |
| `TestMergeWorkItemRestoresSourceWhenTargetMoves` | Automatic source reset intentionally went away. `TestMergeTargetAndSourceCASFailuresRetainSuccessfulSteps` checks that failed target CAS leaves the target untouched and source work retained. |
| `TestMergeWorkItemRollsBackTargetAndSourceWhenSyncFails` | Rollback after publication intentionally went away. `TestMergeTargetSyncFailureRetainsPublicationAndSource` verifies the partial result, recorded evidence and retained published/source tips. |
| `TestMergeWorkItemAbortsConflictingRebase` | Automatic abort intentionally went away. `TestMergeRebaseConflictLeavesCheckoutResolvableAndTargetUntouched` checks unmerged entries remain in the real checkout, source leftovers are committed, and the target stays unchanged. |
| `TestMergeWorkItemRollsBackRebaseForOverlappingDirtyTarget` | Automatic source rollback intentionally went away. `TestMergeOverlappingDirtyTargetAfterRebaseRetainsBothCheckouts` verifies refusal after rebase, unchanged target ref, retained dirty target bytes and retained source content. |
| `TestMergeWorkItemRecordsManagedTargetEvent` | `TestMergeSyncsManagedTargetWorktreeAndRecordsItsEvent` checks `checkout.updated_by_merge` on the target item with the actual source item ID and result commit. |

## Additional corrected behavior

- `TestMergeAllowsIdleOnlineAgentAndRefusesRunningTurn` uses the real isolated binary and verifies an idle online agent merges without stopping or replacing its generation, while a running turn is refused.
- `TestMergeRefusesCaptureInProgress` checks a live capture worker causes a clear refusal without changing the source.
- Browser tests keep Merge enabled for idle online agents, disable it for capture, and leave ordinary Commit's stopped-agent policy unchanged.
- The upstream `needsAttention` tests now include capturing idle/unread cases and a question during capture, preserving background-work and busy-agent behavior.
