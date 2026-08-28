---
title: Troubleshooting
description: Diagnose common startup, workspace, runtime, navigation, and cleanup failures.
---

Start with these commands:

```bash
wi list --ids
wi show ITEM
wi agent status ITEM
wi workspace status ITEM
wi daemon doctor
```

They separate durable lifecycle, runtime activity, and Git checkout condition. Fix the failing concern rather than changing lifecycle state until the error disappears.

## `wi start` cannot find the item

`wi start` accepts an active slug or an unambiguous substring of a slug. It does not accept an ID, title keyword, or implicit current item.

```bash
wi list
wi start exact-active-slug
```

Create missing work with `wi new`, then start the slug returned by that command.

## Deep-work capacity is full

Working and waiting deep items consume configured capacity. Inspect them:

```bash
wi list --all
```

Finish, shelve, archive, or clear deep classification on another item. Use `--force` only when exceeding the configured limit is deliberate:

```bash
wi start --force deep-item
```

## The TUI or picker does not start

Check dependencies:

```bash
command -v pi tmux fzf
pi --help
tmux -V
fzf --version
```

TUI mode requires Pi and tmux. The selector-less `wi switch` picker also requires fzf. Use an explicit item to bypass the picker, or RPC mode to avoid tmux:

```bash
wi switch item-slug
wi start --agent-mode rpc item-slug
```

## The agent did not receive the description

`wi start` starts or enters a runtime but does not submit `DESCRIPTION.md` as a prompt. In the TUI, ask the agent to run `wi show` and act on the description.

For creation and immediate delivery, use `wi new --prompt`. For an existing working item, use:

```bash
wi agent control send --item ITEM "Read `wi show`, implement the task, and report the result."
```

## An `.envrc` is not loaded

Interactive startup asks before trusting an unapproved `.envrc`. Declining, JSON mode, and non-interactive startup continue without it.

Run `wi switch ITEM` interactively to review the approval prompt. For repositories you fully control, add the absolute repository path to the user configuration under `direnv.auto_trust_repositories`.

Repository `.config/wi.toml` cannot approve its own commands.

## `wi next` says nothing needs attention

The attention queue contains working items whose agents are idle and whose agent and worktree observations do not report a problem.

```bash
wi attention queue
wi agent status --all
wi list
```

Busy, waiting, backlog, archived, label-filtered, and problem items are outside the queue. Resume waiting work or repair the reported problem rather than forcing navigation.

## TUI and RPC mode will not switch

One durable conversation can have only one active runtime owner. Pi cannot attach its TUI to a live RPC process.

```bash
wi agent runtime stop ITEM
wi agent runtime status ITEM
```

Wait until the runtime reports offline, then start the other mode:

```bash
wi switch ITEM
# or
wi agent runtime ensure --mode rpc ITEM
```

Use `--force` to abort a busy runtime before shutdown.

## Shelve or archive refuses cleanup

These operations protect dirty, branch-mismatched, active, or currently attached resources.

Inspect each concern:

```bash
wi agent runtime status ITEM
wi terminal status ITEM
wi workspace status ITEM
```

Review and commit, stash, or otherwise resolve worktree changes with Git. Stop an active runtime before shelving. `archive --force` may abort a busy runtime, but it does not discard a dirty checkout.

Do not use `wi state set archived` as a cleanup shortcut. That primitive changes lifecycle only and can leave resources present.

## Merge refuses the source checkout

`wi merge` requires a completely clean source checkout and an existing local target branch.

```bash
wi workspace status ITEM
git status --short
git branch --list main
```

Commit intended source changes before merging. If the target worktree is dirty, its changed paths must not overlap incoming paths. `wi merge` does not fetch or create the target branch.

## Agent status reports a problem

Inspect runtime status and recent events:

```bash
wi agent runtime status ITEM
wi agent monitor --item ITEM --limit 100
wi daemon doctor
```

A mismatched checkout, stale incomplete activity, failed runtime, or unavailable process may produce a problem observation. Stop a verified stale runtime before ensuring a replacement. Avoid deleting sockets, runtime records, or Pi JSONL manually.

## The daemon does not start

```bash
wi daemon status
wi daemon doctor
wi info
```

For foreground diagnostics:

```bash
wi daemon serve
```

Commands stop safely when the daemon is unavailable. Resolve the reported daemon or build error instead of editing the database or item files.

## Get more command detail

```bash
wi help start
wi help agent runtime stop
wi archive --help
```

Command help lists the accepted flags and current safety rules.
