---
title: Everyday workflow
description: Capture a bounded task, review its result, and close it.
---

Choose one objective you can finish and review. These examples assume a [registered repository](../../concepts/repositories/) and current item context; outside an item, add `--repo NAME` when creating work.

## Capture and start

```bash
wi new --label backend --desc-file task.md --title "Harden token refresh"
wi start harden-token-refresh
```

Describe enough context and completion criteria for someone else to act independently. Use `--deep` for tasks that consume configured deep-work capacity.

`start` activates backlog work, materializes its checkout, and enters the default TUI. It does not send a message. For immediate delegation, use `new --start`; see [Delegate work](../delegation/).

## Supervise

```bash
wi list
wi switch harden-token-refresh
wi next
```

`show` reads task context; `events` reads its history. Agent and worktree status are separate observations, not proof that the task is complete.

## Pause

```bash
wi wait --for "token spec answer" harden-token-refresh  # waiting for an external answer
wi wait --clear harden-token-refresh                    # back in the attention queue
wi shelve harden-token-refresh             # return to backlog and release safe resources
```

Shelving retains the branch, description, and conversation. Primitive `state set` changes lifecycle only; see [Lifecycle](../../concepts/lifecycle/).

## Review and finish

[Inspect the diff and run relevant tests](../review-and-follow-up/). Keep corrections to this objective in the same item; file unrelated work separately.

Once intended changes are committed and the checkout is clean:

```bash
wi merge --item harden-token-refresh
wi close harden-token-refresh
```

Merge defaults to the registered integration branch and does not push or close. Closing retains the completed task record and its resolution. Delete only when you intend to permanently remove that closed record.
