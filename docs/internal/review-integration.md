# Ordinary merge

`wi merge` is the only item integration command. The source is the immutable item branch in its claimed checkout. Switched branches and detached HEAD are refused. An idle online agent may merge. A running turn or in-progress capture refuses merge, without stopping the agent.

1. Capture an immutable merge-input snapshot using the same app-layer snapshot implementation as manual and settlement capture.
2. Commit uncommitted checkout work as close does, including untracked nonignored files. Remove unchanged attachment copies. Changes outside snapshot exclusions must still match the captured input.
3. Resolve the target under the existing repository integration lock and rebase onto its fenced commit when necessary. Disable hooks, autostash and update-refs for rebase. Conflicts leave the target unchanged and remain in the real checkout for ordinary Git resolution.
4. By default, create one commit from the rebased tree, with the previous target commit as its only parent. The message defaults to the item title and is editable in the web merge form. Private snapshot commits never become ordinary ancestors.
5. Publish the target with compare-and-swap. Already-contained work succeeds without creating an empty commit.
6. Record the input revision, resulting ordinary commit/tree, target and before/after tips as canonical item content. The merge result becomes the base for subsequent captures.
7. Advance the source branch to the result using compare-and-swap. Squash preserves the rebased tree, so continued checkout work is clean. Synchronize an eligible target checkout using the existing nonoverlap safeguards.

`--rebase` preserves the rebased ordinary series and fast-forwards instead of creating a squash. `--message` applies only to squash. Existing `--target`, `--local`, upstream synchronization and target-checkout restrictions remain.

Approval is informational. Unresolved threads and unpublished drafts produce warnings, not refusals. Exclusions do not make a revision unusable. Ignored files overlapping a rebase or target-checkout update are protected.

Successful durable steps are never rolled back just because a later step fails. In particular, a published target remains published if recording evidence or advancing/synchronizing a checkout fails. The job reports a partial outcome. There is no automatic replay after target advancement. An explicit retry of already-integrated code cannot create an empty duplicate squash.
