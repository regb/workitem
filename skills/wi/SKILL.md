---
name: wi
description: Use when the user wants to manage work items or use the wi cli.
---

`wi` is a cli to manage short-lived work items for git-based projects.

- Each work item is isolated in its own worktree and is assigned an agent session.
- `wi` is primarly a cli designed for human operators and many commands are meant for human use.
- Avoid using any create/edit commands until explicitly asked to by the user.
- Do not perform navigation, workspace, terminal, or workflow cleanup operations directly as they can impact the currently visible item for the user.
- Commands automatically resolve the curent repo and item from the context where `wi` is ran.

## Manage repos

Git repositories are the top-level organizations for work items, a work item is always spawned within the context of a repo.
Wi keeps a global store of all repos that it manages, and a repo must be added explicitly before being able to create a work item in it.

```bash
wi repo list
wi repo add NAME --from GIT_CHECKOUT_PATH --branch DEFAULT_INTEGRATION_BRANCH
```

Use a local checkout of the repo you want to start managing, set the integration branch that `wi` will use to merge back the changes on each work item, if you omit it, it will try to discover it automatically and usually resolve main or master.

Usually you don't need to specify a repo as you will file items related to the current item which is already associated with the right repo.
A repo can have a remote upstream where it syncs its integration branch.

```bash
wi repo status
wi repo sync
```

## Work item context

An agent session is always running in the context of a work item.
You can inspect `WI_*` env variables if you need to understand in which work item context you are currently running.

To inspect the item description and the task:

```bash
wi show --json
```

## Notes and attachments

Each item keeps an append-only log of short notes, useful for handover and for other items. Note only important events, such as a decision, an abandoned approach, a blocker, or a milestone, in a few lines.

```bash
wi note "Chose X over Y because Z"
wi notes [ITEM]
```

Attach files that belong with the item but not in the repository, such as a task report or an evidence screenshot. These can be thought of evidences you'd attach to a PR for review, but don't want to pollute the repository long term. Attaching under an existing name replaces it.

```bash
wi attach REPORT.md
wi attachments [ITEM]
wi attachment cat REPORT.md
```

Adding an attachment automatically adds a corresponding note.

Notes and attachments are a log of the item's important activity, read without the session. Adding to it is always explicit and should reflect very high-level activity on the item. Prompts, session messages, and tool output are never added automatically. 

## Filing work items

Create a new work item with its description in a markdown file:

```bash
wi new --title "Item title" --desc-file DESC_FILE
```

It default to the repo of the current work item, use `--repo REPO_NAME` if you want to file the item in another repo.
The command will file the work item but not start it.

The title should be short (3 to 5 words ideally) and help quickly recognize the item.
Titles are ephemeral, they don't need to capture fully the task but contain enough hints that they can be differentiated among currently active items.
Good titles: `Fix flag xyz`, `Reproduce banner flicker`
Bad title: `issue follow-up`, `Fix the flag abc identified on banner 42 on June 16 when user click twice`

The description should be written in a descriptive style as you would do on a ticket tracking system.
It should be very actionable and include enough context to be self-contained.
If you file an item as a related work or follow up work, consider that the person that will work on it might have no context of your current session and all they will know is the repository at the chosen branch along with the item description that you sent.

An item is always created from the default integration branch of the repo; if you want to start it from the commit in the current worktree, use `--base=HEAD`.

A new item is added in the backlog and not acted on. You can start an agent automatically to tackle it with `--start`. You can choose an agent profile with `--agent`.
Using `--prompt` automatically starts the agent and passes a prompt directly instead of a definition.

```bash
wi config agents
wi new --title "Fix issue 23" --prompt "Please look up issue 23 and fix it" --agent claude
```

## Completing work items

When the work on the item is completed and commited on the local branch, use:

```bash
wi merge
```

to merge it to the default integration branch.
This command synchronizes a configured upstream before merging, but does not push.

Once an item is merged and all work scoped to the task is settled (or accounted for in follow-up items), an item can be closed.
Never close an item without explicit instructions from the user.
Usually this is rather ran as part of review and management of items, not directly within an item, so use:

```bash
wi close --item ITEM_NAME --resolution completed|abandoned|superseded
```

## Update item state

Avoid updating wi items unless explicitly asked to do so.

```bash
wi show --json
wi edit --label backend --label reliability
wi edit --remove-label reliability

wi state show --json
wi state set backlog
wi state set active
wi state set closed
```

Lifecycle is `backlog`, `active`, or `closed`. `state set` changes durable lifecycle only. It does not start/stop agents or clean terminal/workspace resources.

`wi state set` changes durable lifecycle only. It does not start/stop agents or clean terminal/workspace resources.
    
## Waiting

Waiting is separate from lifecycle. A waiting item leaves the user's attention queue until what it waits for happens. Only make an item wait when the user asks you to.
    
```bash
wi wait --until tomorrow          # snooze; also ends when the user next prompts the item
wi wait --on OTHER_ITEM           # until another item closes
wi wait --for "security review"   # until an external event, cleared explicitly
wi wait --clear                   # stop waiting
wi blocker list --json
```

Time and item waits clear on their own; events are cleared explicitly, by the user or by automation.

## Review work items

Use when asked to inventory, review, or triage current work:

```bash
wi list --json
wi list --json --state active
wi list --json --label +backend --label -blocked
wi list --json --all # To include archived items
```
