---
title: Command map
description: Find the wi command that owns each operation.
---

Run `wi help <command>` or `wi <command> --help` for complete options, safety notes, environment variables, and examples.

## Common recipes

Capture detailed backlog work:

```bash
wi new --desc-file task.md --label backend "Fix token refresh"
```

Create and delegate a headless task:

```bash
wi new --agent-mode rpc --prompt "Run the audit and report findings." "Audit dependencies"
```

Start existing backlog work interactively:

```bash
wi start active-slug
```

`start` accepts a slug or unambiguous slug substring. It does not submit the stored description as a prompt.

Inspect and enter active work:

```bash
wi list
wi show auth
wi switch auth
```

Send a follow-up:

```bash
wi agent control send --follow-up --item auth \
  "Fix the review findings and rerun tests."
```

Pause or remove work from the active set:

```bash
wi state set waiting auth
wi resume auth
wi shelve auth
```

Integrate and archive verified work:

```bash
wi merge --item auth main
wi archive auth
```

See [Troubleshooting](../troubleshooting/) when a protected cleanup or runtime operation refuses to proceed.

## Durable items

| Command | Purpose |
| --- | --- |
| `wi new` | Create backlog work or create and delegate with `--prompt` |
| `wi show` | Read metadata and the durable description |
| `wi events` | Read append-only item history |
| `wi state show\|set` | Inspect or mutate lifecycle only |
| `wi label` | List, add, or remove labels |
| `wi deep` | Set or clear deep-work classification |

## Workspaces

| Command | Purpose |
| --- | --- |
| `wi workspace status` | Inspect checkout assignment and Git state |
| `wi workspace ensure` | Materialize or reclaim a checkout |
| `wi workspace release` | Release the item's checkout claim when safe |
| `wi workspace relocate` | Use a repository checkout after its directory moved |

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
| `wi start` | Activate backlog work, or create a minimal item with `--new` |
| `wi resume` | Reactivate waiting work |
| `wi shelve` | Clean up safely and return work to backlog |
| `wi archive` | Clean up safely and retain inactive history |
| `wi delete` | Permanently remove cleaned archived case files |
| `wi shutdown` | Stop runtimes, terminals, and the daemon |
| `wi merge` | Rebase and fast-forward a local target branch |

`wi archive` retains the item. To restore one, use its archived ID:

```bash
wi list --archived --ids
wi state set backlog --item <item-id>
```

The state change assigns a fresh active slug but does not recreate workspace or agent resources. Use `wi start --item <item-id>` when you are ready to resume work.

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
