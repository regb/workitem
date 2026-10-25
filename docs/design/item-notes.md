# Item notes and attachments

A work item's description states its intent and stays that way. What happens while working on it needs a lighter record than the agent conversation, which is long, driver-owned, and hard to scan. Two additions cover it, without item kinds, authors, or an outcome field.

## Notes

A note is a short, timestamped line of progress: a decision, an abandoned approach, a blocker, a milestone. Notes are append-only; a correction is a new note. Together they form the item's activity log, for a quick view of what was done and tried, for handover, and for other items' agents to read.

- `wi note TEXT` appends; `wi notes` lists. Both resolve the item like other commands.
- A note is at most 2 KB. Longer content belongs in an attachment.
- Notes have no author. Operators and agents add them the same way, and the agent endpoint allows it.
- While the item has a worktree, a note records its `HEAD`, so "tried X" keeps the commit it was tried at. The commit may later become unreachable after a rebase; the SHA still names it.
- Notes are quiet metadata: they never count as unread, never raise attention, and are never sent to an agent.

## Attachments

An attachment is a named file kept with the item, outside Git: a report, a screenshot, evidence. A research task can ask for its findings as `REPORT.md`; nothing in the core knows about research.

- `wi attach FILE [--as NAME]` copies the file; the original is left alone. `wi attachments` lists, `wi attachment cat NAME` prints the current content.
- Attaching an existing name replaces it. Content is stored by its SHA-256, and each attach appends a note naming the attachment, its hash, and its size. Earlier content stays readable by hash, so older notes keep describing what was attached then. There are no version numbers.
- Names are plain file names. Files are limited to 16 MB each, so one fits a coordinator request.

## Storage

Both are canonical filesystem content beside the description, written only by the coordinator, one at a time:

```
items/<ID>/notes.jsonl               one {"time", "text", "head"?, "attachment"?} per line
items/<ID>/attachments/<sha256>      attached content
```

The log is the only index: an attachment name's current content is the latest note that attached it. Content is stored before its note, so a note never names missing content; an append cut short is dropped by the next one. Readers read the files directly; deleting an item removes them with its directory. Each addition wakes the change stream, so the web view refreshes, but adds no domain event: the log is its own history.

## Commits

No commit-tracking concept. What landed is already recorded by `wi merge` events (source and target SHAs). Notes record the worktree's `HEAD`. Pinning recorded commits against garbage collection (for example under `refs/wi/`) can come later if a lost commit ever matters.
