---
title: Command map
description: Find the wi command that owns each operation.
---

Run `wi help <command>` or `wi <command> --help` for complete options, safety notes, environment variables, and examples.

## Common recipes

Each item is one bounded task. `wi new` requires `--title TEXT` or `--untitled`. The latter uses an editable `Untitled task` title and an ID-derived slug. `--slug` overrides the default in either case. Outside a current item, select a registered `--repo NAME`.

`new --start` creates and delegates; existing-item `start` opens a runtime without submitting a message. `resume` never sends a prompt. See the [Quick start](../../getting-started/quick-start/), [Delegation](../../guides/delegation/), and [Review workflow](../../guides/review-and-follow-up/) for examples.

Finish and close items rather than continually expanding them. Merge, close, and push are separate decisions.

## Initial setup

| Command | Purpose |
| --- | --- |
| `wi init` | Discover installed agents and select the default, then write the initial user config |
| `wi init --default PROFILE [--json]` | Select a detected profile without prompting |

Setup refuses to overwrite an existing config and does not start agents or the coordinator. A missing default profile is a configuration error; run setup before repository or item commands.

## Configuration queries

| Command | Purpose |
| --- | --- |
| `wi config [show] [--json]` | Inspect global settings with built-in defaults |
| `wi config get KEY [--json]` | Query a scalar or section by dot-separated key |
| `wi config agents [--json]` | List configured agent profiles and mark the default |
| `wi config drivers [--json]` | Inspect driver definitions and defaults |
| `wi config plugins` | Inspect registered plugin options, defaults, types, and scopes |
| `wi config get KEY [--origins]` | Inspect a global value and optional extension-default provenance |

These commands are read-only and do not start a daemon, agent, or extension, load checked-in project overrides, or discover installations. Config queries are local. Repository policy is managed separately through `wi repo settings`. Opaque driver settings, custom commands, and sensitive extension values are redacted unless `--show-sensitive` is supplied. See [Configuration](../configuration/) for examples.

## Repositories

Single-repository commands default to the current item's registered repository when the selector is omitted. Explicit names or IDs override context. Outside an item, pass a selector. `rename NEW` renames the current repository; `upstream URL` configures its upstream. `list` remains global and `add` still requires a new name.

| Command | Purpose |
| --- | --- |
| `wi repo add NAME --from SOURCE [--branch BRANCH] [--local] [--upstream URL] [--upstream-branch BRANCH]` | Import an independent store, inferring integration upstream from a URL or local origin; `--local` disables inference |
| `wi repo list [--all]` | List names, integration branches, state, and upstream destinations; `--all` includes archived repositories |
| `wi repo show [NAME]` | Read registry metadata, live Git tracking, store path, and integration commit |
| `wi repo rename [OLD] NEW` | Change a unique name without moving resources |
| `wi repo settings show [NAME] [--json] [--show-sensitive]` | Inspect stored repository overrides and effective plugin settings |
| `wi repo settings set [NAME] plugins.PLUGIN.OPTION VALUE` | Set a registered repository-scoped option through the daemon |
| `wi repo settings unset [NAME] plugins.PLUGIN.OPTION` | Remove an override and restore global/default inheritance |
| `wi repo upstream [NAME] URL [--branch BRANCH]` | Edit origin and integration branch tracking in Git; `--clear` removes tracking but keeps origin |
| `wi repo status [NAME] [--fetch]` | Inspect cached ahead/behind history, optionally fetching without integrating |
| `wi repo sync [NAME]` | Fetch and fast-forward integration, refusing divergence; defaults to the current item’s repository |
| `wi repo sync [NAME] --merge` | Reconcile divergent histories in a dedicated checkout; finish with `--continue` or `--abort` |
| `wi repo sync [NAME] --reset-to-upstream --yes` | Replace integration with upstream, checking exact tips and retaining a recovery ref |
| `wi repo archive [NAME]` | Hide a repository and block new items, preserving all Git data and existing work |
| `wi repo restore [NAME]` | Make an archived repository available for new items again |

## List JSON

`wi list --json` returns an object with `deep_work`, `sections`, `warnings`, and `display_sections`. It is not a top-level array. Existing `sections` remain keyed by lifecycle: `working`, `waiting`, `backlog`, and optionally `archived`. Full IDs, slugs, and titles are available there regardless of text truncation.

`display_sections` adds the text display grouping and ordering without duplicating items. Each entry contains `key`, `title`, and an ordered `item_ids` array referencing items in `sections`. With agent observations, keys include `needs_attention`, `needs_fixing`, `in_progress`, `waiting`, and `backlog`. Without observations, including `--no-agent`, grouping follows lifecycle. Archived visibility follows `--archived` and `--all`. Empty display groups have empty ID arrays; text omits their headings.

```bash
# All visible items, using stable IDs instead of truncated text selectors.
wi list --json | jq -r '.sections[][]? | [.id, .slug, .title] | @tsv'

# Items in the same NEEDS ATTENTION group and order as the text listing.
wi list --json | jq -r '.display_sections[] | select(.key == "needs_attention") | .item_ids[]'
```

Use a JSON parser rather than matching serialized whitespace. A working lifecycle item can appear in NEEDS ATTENTION, NEEDS FIXING, or IN PROGRESS without changing its lifecycle state.

## Durable items

| Command | Purpose |
| --- | --- |
| `wi new` | Create backlog work or create and delegate with `--start`; `--prompt` starts an agent with that message, without changing the description |
| `wi show` | Read metadata and the durable description, with the repository, its upstream, and how many commits the branch is ahead of and behind it (as of the last fetch) |
| `wi search` | Find items by title, labels, description, or notes, closed ones included |
| `wi edit` | Edit title, description, labels, and deep work together; open an editor when no edit flags are supplied |
| `wi events` | Read append-only item history |
| `wi note` / `wi notes` | Add to or read the item's log of short progress notes |
| `wi attach` / `wi attachments` / `wi attachment cat` | Attach a file to the item, list attachments, print one |
| `wi state show\|set` | Inspect or mutate lifecycle only |

`wi edit` defaults to the current item. Select another with `--item SELECTOR` or a positional selector; use IDs for closed items.

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

### Notes and attachments

```bash
wi note "Dropped the cache approach: invalidation races with merge"
wi notes
wi attach REPORT.md
wi attach --as login-bug.png -m "Login fails after redirect" /tmp/shot.png
wi attachments
wi attachment cat REPORT.md
```

Notes are append-only, timestamped, and at most 2 KB. While the item has a worktree, each note records its `HEAD`. Attaching copies a file of at most 16 MB into the item and adds a note; attaching under an existing name replaces it, and earlier content stays readable with `wi attachment cat DIGEST`, using the digest its note recorded. Options go before the note text. Notes never mark an item unread, raise attention, or reach the agent. The agent endpoint may add notes and attachments. JSON output returns the full records.

### Search

```bash
wi search cache
wi search --state closed "session cache"
wi --json search otto deploy
```

Search finds items whose title, slug, labels, description, or notes contain every word of the query, ignoring case; a quoted phrase is one term, and attachment names count as notes. Results rank by the strongest field matched (title, labels, description, then notes), then by most recent update, and a description or notes match shows an excerpt around it. All states are searched unless `--state backlog|active|closed` narrows it; `--limit` caps the results (default 50). Search reads descriptions and notes from the item directories on each query, which stays fast at the size of a personal work list. Conversations are not searched yet.

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
| `wi agent monitor` | Read normalized runtime events; `--content` follows live message text, tool calls and activity |
| `wi agent settings [show]` | Show an item's model and reasoning level and the available models |
| `wi agent settings set [--restart] NAME=VALUE...` | Change them for the item, live where the running agent supports it |
| `wi agent profile set PROFILE [NAME=VALUE...]` | Move a stopped item to another profile of its conversation's driver |
| `wi agent control send` | Send steering or follow-up text and optional repeatable `--image` attachments |
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
| `wi resume` | Ensure an active item's agent runtime |
| `wi wait` | Wait for a time, another item, or something external; `--clear` stops waiting |
| `wi blocker` | Add, list, or clear individual typed blockers with metadata |
| `wi plugin` | List configured plugins or run one once |
| `wi shelve` | Clean up safely and return work to backlog |
| `wi close` | Clean up safely and retain history with a resolution |
| `wi delete` | Permanently remove cleaned closed task records |
| `wi shutdown` | Stop runtimes, terminals, and the daemon |
| `wi merge [--local]` | Synchronize a configured upstream base, then integrate the item; `--local` skips remote preflight |

`wi close` retains the record and its resolution. For unfinished work closed by mistake, see [restoration](../../concepts/lifecycle/#composed-workflow-commands). File a new objective as a new item.

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
