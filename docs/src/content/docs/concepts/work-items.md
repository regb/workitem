---
title: Work items
description: Understand the durable record at the center of wi.
---

A work item is the durable record for one development task. It is more than a branch, worktree, process, or terminal session.

A work item owns:

- a stable ID and active slug
- a title and self-contained description
- lifecycle state
- labels and deep-work classification
- repository and checkout specification
- one durable primary Pi conversation
- append-only event history

Workspaces, agent processes, and tmux sessions can disappear and be rebuilt. The work item remains the identity that ties them together.

## Creation does not imply execution

```bash
wi new --desc-file task.md "Investigate cache invalidation"
```

Ordinary creation records backlog work only. This separation makes capturing an idea cheap and safe. It also preserves a deliberate partial result if later startup fails.

To create and delegate in one command:

```bash
wi new --prompt "Find the race, add a regression test, and report the cause." \
  "Investigate cache race"
```

This stores the prompt as the description, changes the item to `working`, starts an unattached runtime, and sends the prompt to Pi.

## Descriptions are handoffs

A good description states the problem, relevant evidence, constraints, and what counts as done. Do not rely on the shell history or the conversation in which the item was created.

```bash
wi show cache-race
```

`show` returns the durable metadata and hydrated description. Agents can use the same command to recover their task context.

## IDs and slugs

The ID is permanent. The slug is a convenient alias while an item is active.

Selectors may use:

- a full ID or unique ID prefix
- an active slug
- an unambiguous keyword from an active item's slug, title, description, or labels

Archiving clears the active slug. Restoring an archived item to backlog allocates a fresh slug while retaining its ID and history.

## Labels and deep work

Labels organize work:

```bash
wi label --item cache-race backend reliability
wi list --label +backend --label -blocked
```

Deep work is a separate built-in property with capacity limits:

```bash
wi deep --item cache-race
wi deep --clear --item cache-race
```

A label named `deep-work` does not change deep-work capacity.
