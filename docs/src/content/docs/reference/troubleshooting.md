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

## Repository context is missing

New work requires a registered repository. Inside a Wi item, it inherits that repository; outside one, pass `--repo NAME`:

```bash
wi repo list
wi repo add project --from /path/to/repository --branch main
wi new --title "Task" --repo project
```

External source checkouts can move without affecting the independent owned store. If the data root itself moved, Git worktree metadata may need repair; restore or repair from a verified backup rather than using symlinks. See [Repositories](../../concepts/repositories/) and [Backups](../data-and-diagnostics/#what-to-back-up).

## Deep-work capacity is full

Active deep items, waiting or not, consume configured capacity. Inspect them:

```bash
wi list --all
```

Finish, shelve, close, or clear deep classification on another item. Use `--force` only when exceeding the configured limit is deliberate:

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

TUI mode requires Pi and tmux. The selector-less `wi switch` picker also requires fzf. Use an explicit item to bypass the picker, or headless mode to avoid tmux:

```bash
wi switch item-slug
wi start --agent-mode headless item-slug
```

## The agent did not receive the description

`wi start` starts or enters a runtime but does not submit `DESCRIPTION.md` as a prompt. In the TUI, ask the agent to run `wi show` and act on the description.

For creation and immediate delivery, use `wi new --title "Task" --desc-file task.md --start`. `--prompt` implies `--start` and overrides only the startup message. Registered managed worktrees receive per-launch Pi project approval, while `.envrc` approval remains separate. A readiness timeout or refused prompt reports a retained partial outcome; it does not report successful delivery. For an existing active item, use:

```bash
wi agent control send --item ITEM 'Read the task with wi show, implement it, and report the result.'
```

## An `.envrc` is not loaded

Interactive startup asks before trusting an unapproved `.envrc`. Declining, JSON mode, and non-interactive startup continue without it.

Approve the checkout's `.envrc` with `direnv allow /path/to/checkout/.envrc`, or use interactive startup when a new runtime is needed. Existing runtime environments are not changed in place.

For automatic approval, run `wi repo settings set NAME plugins.direnv.trust always`. This is operator-owned repository policy and also approves changed contents. Inspect stored overrides and effective policy with `wi repo settings show NAME`. Use `wi repo settings unset NAME plugins.direnv.trust` to restore global/default inheritance.

The bundled extension still revokes stale approval on slot reuse. Checked-in `.config/wi.toml` cannot grant approval. See [Configuration](../configuration/#direnv-trust) for loading and trust options.

## `wi next` says nothing needs attention

The attention queue contains active items whose agents are idle, whose agent and worktree observations do not report a problem, and that are either not waiting or waiting with an unread response.

```bash
wi attention queue
wi agent status --all
wi list
```

Busy, waiting and read, backlog, closed, label-filtered, and problem items are outside the queue. Stop waiting with `wi wait --clear`, or repair the reported problem, rather than forcing navigation.

## TUI and headless mode will not switch

One durable conversation can have only one active runtime owner. Pi cannot attach its TUI to a live headless process.

```bash
wi agent runtime stop ITEM
wi agent runtime status ITEM
```

Wait until the runtime reports offline, then start the other mode:

```bash
wi switch ITEM
# or
wi agent runtime ensure --mode headless ITEM
```

Use `--force` to abort a busy runtime before shutdown.

## Shelve or close refuses cleanup

These operations protect dirty, branch-mismatched, active, or currently attached resources.

Inspect each concern:

```bash
wi agent runtime status ITEM
wi terminal status ITEM
wi workspace status ITEM
```

Review and commit, stash, or otherwise resolve worktree changes with Git. Stop an active runtime before shelving. `close --force` may abort a busy runtime, but it does not discard a dirty checkout.

Do not use `wi state set closed` as a cleanup shortcut. That primitive changes lifecycle only and can leave resources present.

## Merge refuses the source checkout

`wi merge` requires a completely clean source checkout and an existing local target branch.

```bash
wi workspace status ITEM
git status --short
git branch --list main
```

Commit intended source changes before merging. The registered integration branch must not be checked out in the owned store. For another explicit target, dirty paths in its worktree must not overlap incoming paths. `wi merge` does not create the target branch. When targeting the registered integration branch with an upstream configured, it fetches and fast-forward-synchronizes the base first. Use `--local` for deliberate offline integration. If histories diverge, inspect `wi repo status NAME --fetch`, then choose `wi repo sync NAME --merge` or `--reset-to-upstream --yes`. Patch-equivalent commits can have identical trees but incompatible histories; rebasing only the item onto `origin/main` does not repair stale local integration history.

## Repository tracking needs attention

Git configuration in the owned store is the current upstream authority. `wi repo show NAME` reports missing, incomplete, or unsafe tracking. Use `wi repo upstream NAME URL --branch BRANCH` to configure origin and integration branch tracking, or `--clear` to choose local-only operation without deleting origin.

A pending reconciliation keeps its own frozen destination and blocks configuration changes. Changing Git tracking during reconciliation prevents `--continue`; `--abort` remains available and retains manually committed resolution work under a recovery ref.

If a client says the daemon does not support Git-owned tracking, restart the daemon with the installed build before using these repository commands. Do not restart item agents just to update the daemon.

## Agent status reports a problem

Inspect runtime status and recent events:

```bash
wi agent runtime status ITEM
wi agent monitor --item ITEM --limit 100
wi daemon doctor
```

Stale incomplete activity, failed runtimes, or unavailable processes may produce a problem observation. Branch divergence in a managed slot is reported as `changed` rather than preventing inspection or navigation. Stop a verified stale runtime before ensuring a replacement. Avoid deleting sockets, runtime records, or Pi JSONL manually.

## A tmux server restart orphaned active runtimes

Restarting the tmux server (or `tmux kill-server`) removes every wi-owned session while the TUI agent processes keep running, orphaned and unreachable. The next `wi switch ITEM`, `wi next`, or picker selection on such an item detects the missing terminal and recovers automatically: it stops the verified orphaned runtime and starts a fresh terminal that resumes the same conversation. The result reports a warning that this happened.

If the orphaned process will not exit within a few seconds, `wi switch` fails with a recovery hint. Stop it explicitly and switch again:

```bash
wi agent runtime stop --force --item ITEM
wi switch ITEM
```

To recover every item at once, stop all runtimes and terminals:

```bash
wi shutdown --force
```

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
wi close --help
```

Command help lists the accepted flags and current safety rules.
