---
title: Work items
description: One bounded task, with a durable record after completion.
---

A work item is a short-lived development task with one objective and a clear completion condition. Fix a defect, add a bounded feature, or answer a specific investigation question. Avoid items such as "Maintain the API" that have no natural end.

Review the result, integrate it where appropriate, and archive the item. File unrelated discoveries as new tasks rather than continually expanding the original scope.

## What remains durable

The item owns its ID, title, description, lifecycle, labels, repository reference, checkout specification, selected agent profile, conversation identity, and append-only history. Worktrees, agent processes, and terminals can disappear without erasing that record.

Durability protects interruptions and preserves completed work; it does not mean keeping an item active indefinitely.

## Capture and execution

```bash
wi new --desc-file task.md --title "Investigate cache invalidation"
```

Supply `--title` or explicitly use `--untitled`. The latter creates an editable `Untitled task` title and derives a slug such as `item-933sga1r` from the last eight characters of the item ID. The flags are mutually exclusive. `--slug` overrides either default; generated slug collisions receive numeric suffixes. Wi never infers a title or slug from a description or prompt.

`--untitled` is only a creation option, not a durable classification. Later title edits leave the active slug and branch unchanged. Restore follows the normal current-title allocation policy. Creation selects a [registered repository](../repositories/) from the current item or an explicit `--repo NAME`.

Ordinary creation records backlog work without allocating a checkout or agent. Add `--start` to activate the item and send the configured startup message. `--prompt` overrides only that message, not the description. Existing-item `start` and `resume` send no prompt. See [Delegate work](../../guides/delegation/).

## Descriptions are handoffs

Include the problem, relevant evidence, constraints, and an observable definition of done. The recipient should not need the conversation that produced the task.

```bash
wi show cache-race
```

`show` returns metadata and the description.

## IDs and slugs

The ID is permanent; the active slug is a human-readable selector. Selectors can also use a unique ID prefix or an unambiguous active-item keyword. `wi start` specifically requires an active slug or slug substring.

The slug is fixed at creation while the item remains active, including before its first checkout. Archiving clears it. Restoring to backlog allocates a fresh slug while retaining the ID and history.

The implementation branch specification is fixed at creation and survives archive/restore; a new active slug does not rename the branch. Title and description remain editable in every lifecycle state. Use `wi edit --title "New title"` for the current item or `wi edit --item SELECTOR --title "New title"` for another. `wi edit` with no edit flags opens title and description in an editor. Select archived items by ID. Updating them does not change the slug or branch, alter native conversation history, or automatically send a prompt. Auxiliary title suggestions update presentation only.

## Labels and deep work

```bash
wi edit --item cache-race --label backend --label reliability
wi list --label +backend --label -blocked
wi edit --item cache-race --deep
wi edit --item cache-race --no-deep
```

Labels organize tasks. Deep work is a separate classification subject to active-capacity limits; a label named `deep-work` does not enable it.
