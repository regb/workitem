# Snapshot and merge branches

An item's immutable checkout specification identifies its Wi branch. Capturing is non-destructive and may observe a different named branch or detached HEAD in the claimed checkout. This does not change item identity, its branch specification, workspace claim or conversation.

Manual, settlement and capture-plus-reply snapshots share the same app implementation. It checks the registered Git common directory and actual checkout root, then observes branch, HEAD and bounded file content before and after capture. A nested directory, replaced `.git`, stale claim or detected concurrent switch is rejected. A stable branch switch before capture is allowed.

Every revision stores the branch and HEAD actually observed. Detached snapshots have an empty observed branch and an explicit detached flag. A new capture gets a new item-scoped revision identity even when bytes match a previous observation. Earlier bases, comparisons, comments and approvals remain unchanged. An exact successful retry returns its original record rather than recapturing.

The initial base is the first captured checkout HEAD. Later bases come from merge results, without another baseline snapshot. Release, slot reuse, closure and reopening retain existing review history.

Ordinary merge keeps item-branch semantics. It refuses a switched or detached checkout instead of choosing the snapshot's branch as a new merge source. There is no selected-revision preparation/integration path. Commit, live editing and workspace release retain their existing branch safeguards.

Capture failure never holds input. A missing checkout skips silently; other failures create a visible gap/reason. Later settlement capture can proceed normally. There is no held boundary, retry/continue decision, runtime quality label or schema-compatibility path.
