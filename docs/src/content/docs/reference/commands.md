---
title: Command map
description: Find the wi command that owns each operation.
---

Run `wi help <command>` or `wi <command> --help` for complete options, safety notes, environment variables, and examples.

## Common recipes

Each item is one bounded task. `wi new` requires `--title TEXT` or `--untitled`. The latter uses an editable `Untitled task` title and an ID-derived slug. `--slug` overrides the default in either case. Outside a current item, select a registered `--repo NAME`.

`new --start` creates and delegates; existing-item `start` opens a runtime without submitting a message. `resume` never sends a prompt. See the [Quick start](../../getting-started/quick-start/), [Delegation](../../guides/delegation/), and [Review workflow](../../guides/review-and-follow-up/) for examples.

Finish and archive items rather than continually expanding them. Merge, archive, and push are separate decisions.

## Repositories

| Command | Purpose |
| --- | --- |
| `wi repo add NAME --from SOURCE [--branch BRANCH]` | Import an independent bare store and register its integration branch |
| `wi repo list` | List names and integration branches; JSON includes IDs |
| `wi repo show NAME` | Read registry metadata, store path, and integration commit |
| `wi repo rename OLD NEW` | Change a unique name without moving resources |

## Durable items

| Command | Purpose |
| --- | --- |
| `wi new` | Create backlog work or create and delegate with `--start`; `--prompt` overrides only the startup message |
| `wi show` | Read metadata and the durable description |
| `wi edit` | Edit title, description, labels, and deep work together; open an editor when no edit flags are supplied |
| `wi events` | Read append-only item history |
| `wi state show\|set` | Inspect or mutate lifecycle only |

`wi edit` defaults to the current item. Select another with `--item SELECTOR` or a positional selector; use IDs for archived items.

```bash
wi edit --title "Clarified objective"
wi edit --desc-file task.md
wi edit --label backend --remove-label blocked
wi edit --deep
wi edit --no-deep
wi edit --item auth --title "Fix refresh" --label urgent
wi edit --item auth
```

With no edit flags, Wi opens a private title-and-description draft using `$VISUAL`, then `$EDITOR`, then `vi`. Keep the first line `Title: <text>`, a blank line, then the description. Editor commands may include arguments. A nonzero editor exit or invalid document changes nothing and retains the private draft for recovery.

Titles are non-empty single-line UTF-8, at most 512 bytes. Descriptions are UTF-8, at most 4 MiB; an empty file clears the description. Labels may be repeated but cannot be both added and removed in one edit. `--deep` and `--no-deep` are mutually exclusive.

All requested metadata changes are validated and committed together. Wi refuses stale item revisions or description digests instead of overwriting concurrent edits. Description bytes stay on disk, staged for durable recovery; the database contains only hashes. If native materialization fails after commit, Wi explicitly reports the retained edit and pending description rather than claiming rollback. Do not discard the staged file: reconcile the canonical description with either its expected old content or the committed staged content, then the daemon can finish recovery. Unrelated items remain usable.

Editing does not change lifecycle, slug, branch, checkout, conversation, or runtime, and sends no prompt. Use `wi show` to read metadata and description. JSON returns `work_item_id`, `changed`, `title`, `labels`, `deep_work`, `description_changed`, and `manifest`. No-op edits report `changed: false` without adding an event.

The former title, label, and deep metadata commands have been removed; there are no aliases. Lifecycle and workflow verbs remain separate.

## Workspaces

| Command | Purpose |
| --- | --- |
| `wi workspace status` | Inspect checkout assignment and Git state |
| `wi workspace ensure` | Materialize or reclaim a checkout |
| `wi workspace release` | Release the item's checkout claim when safe |

## Agents

| Command | Purpose |
| --- | --- |
| `wi agent runtime status\|ensure\|stop` | Manage the exclusive runtime owner |
| `wi agent status` | Inspect derived agent and worktree observations |
| `wi agent monitor` | Read normalized runtime events |
| `wi agent control send` | Send steering or follow-up input |
| `wi agent control abort` | Abort the current agent run |

## Attention and views

| Command | Purpose |
| --- | --- |
| `wi list` | Project work by actionability and apply filters |
| `wi attention activity` | Show prompt, completion, and defer facts |
| `wi attention defer` | Move ready work behind other candidates |
| `wi attention queue` | Read the ranked attention queue |

## Interactive access

| Command | Purpose |
| --- | --- |
| `wi terminal status\|ensure\|enter\|close` | Low-level tmux access |
| `wi switch` | Pick or enter active work |
| `wi next` | Select ready work and enter it |

## Workflow commands

| Command | Purpose |
| --- | --- |
| `wi start` | Activate an existing backlog item by active slug |
| `wi resume` | Reactivate waiting work |
| `wi shelve` | Clean up safely and return work to backlog |
| `wi archive` | Clean up safely and retain inactive history |
| `wi delete` | Permanently remove cleaned archived task records |
| `wi shutdown` | Stop runtimes, terminals, and the daemon |
| `wi merge` | Fast-forward a local target branch, rebasing when needed |

`wi archive` retains the completed record. For unfinished work archived by mistake, see [restoration](../../concepts/lifecycle/#composed-workflow-commands). File a new objective as a new item.

## Daemon and diagnostics

| Command | Purpose |
| --- | --- |
| `wi daemon start\|status\|doctor\|stop` | Diagnose or control the local daemon |
| `wi info` | Print resolved paths |
| `wi version` | Print build identity |

## Global JSON output

`--json` may appear before or after the command:

```bash
wi --json list
wi list --json
```

Terminal-entering commands do not attach in JSON mode.
