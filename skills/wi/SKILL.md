---
name: wi
description: Use when the user wants to manage work items or use the wi CLI.
---

Wi manages short-lived development tasks in registered Git repositories. Each item should have one bounded objective and a clear completion condition. Its description, conversation, and history remain durable after completion; do not turn an item into a permanent project chat.

## Authority and context

- Read task context with `wi show --json`; inspect `WI_*` variables when needed.
- Create or update items only when explicitly asked.
- Do not navigate, start/stop runtimes, alter workspaces or terminals, merge, archive, or delete without explicit authorization. These operations affect the user's environment.
- Commands normally resolve the current item from its environment or checkout. Backlog items may have no checkout, conversation, or runtime.

## Repositories

```bash
wi repo list
wi repo list --all
wi repo archive NAME
wi repo restore NAME
wi repo add NAME --from GIT_CHECKOUT_PATH --branch INTEGRATION_BRANCH
```

Repositories must be registered explicitly. Onboarding imports committed state into an independent bare store without modifying or depending on the source checkout. Repository selection defaults to the current item's repository; outside an item, pass `--repo NAME`. Registration authorizes project-local agent resources in its verified managed worktrees. Pi receives per-launch approval, not a saved global trust decision; direnv `.envrc` approval remains separate.

Only archive or restore a repository when explicitly asked. Archiving hides it from the default list and blocks new items, including inherited selection. It preserves its reserved name, Git data, dirty worktrees, and existing items and does not stop agents. Restore allows new items again. There is no repository deletion command; do not delete Git stores or worktrees manually.

## File a task

```bash
wi new --title "Fix banner flicker" --desc-file task.md
```

This captures backlog work without starting it. For capture before naming, use `wi new --untitled`; it creates an editable `Untitled task` title and an ID-derived slug. `--title` and `--untitled` are mutually exclusive. Later title edits do not rename the active slug or branch.

Use a short recognizable title and a self-contained description with evidence, constraints, and completion criteria. A recipient may have none of the current conversation.

Creation defaults to the registered integration commit. Use `--base=HEAD` to build on the current claimed worktree's commit; dirty files are not copied.

When explicitly requested, `--start` activates the item and sends the configured startup message. `--prompt` implies `--start` and supplies that message, not the description. Use it only when agent startup is explicitly authorized. Existing-item `start` and `resume` send no prompt.

File unrelated discoveries as separate tasks. Correcting the original objective can stay in the same item.

## Review and finish

```bash
wi list --json
wi list --json --state working
wi list --json --label +backend --label -blocked
wi list --json --all
wi workspace status --json
```

An idle agent is not proof of completion. Review the diff and relevant tests. Once intended changes are committed and the checkout is clean, an explicitly authorized `wi merge` integrates into the registered branch; it does not push or archive.

Archive when the objective is settled and remaining work has separate follow-up items. Only do so on explicit user instruction. Archiving keeps the task record.

## Metadata and lifecycle

When asked to update an item:

```bash
wi edit --title "Clarified objective"
wi edit --label backend --label reliability --remove-label blocked
wi edit --desc-file revised-task.md
wi edit --deep
wi edit --no-deep
wi state show --json
wi state set waiting
```

`wi edit` applies metadata changes together without changing lifecycle, slug, branch, or runtime and without sending a prompt. With no edit flags, it opens title and description in `$VISUAL`/`$EDITOR`; use flags from an agent session rather than invoking an interactive editor. Use `--item SELECTOR` for another item and IDs for archived items. `wi show` reads metadata and description. The old title, label, and deep commands are removed, with no aliases. The restricted agent endpoint rejects edits; do not try to bypass it.

`state set` changes lifecycle only. It does not start/stop agents or clean workspace/terminal resources. Use `wi help <command>` for exact options and safety rules.
