---
title: Troubleshooting
description: Diagnose common startup, workspace, agent, navigation, and cleanup failures.
---

Start with these commands:

```bash
wi list --ids
wi show ITEM
wi agent status ITEM
wi workspace status ITEM
wi daemon doctor
```

They separate durable lifecycle, agent activity, and Git checkout condition. Fix the failing concern rather than changing lifecycle state until the error disappears.

## `wi start` cannot find the item

Like other commands, `wi start` resolves its selector with the daemon: an ID, a unique ID prefix, an active slug, or an unambiguous keyword. An ambiguous selector exits with code 4 and lists the candidates.

```bash
wi list
wi start exact-active-slug
```

Create missing work with `wi new`, then start the ID or slug returned by that command.

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

The daemon refuses to activate another deep item beyond `deep_work.max_active`. Finish, shelve, close, or clear deep classification on another item. Use `--force` only when exceeding the configured limit is deliberate; it is available only to operator callers:

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

TUI mode requires Pi and tmux. The daemon runs tmux and the agents, so they must be found on the daemon's PATH, which an auto-started daemon took from the shell that started it; see [Daemon startup environment](../configuration/#daemon-startup-environment). The selector-less `wi switch` picker also requires fzf, which runs in the command itself. Use an explicit item to bypass the picker, or headless mode to avoid tmux:

```bash
wi switch item-slug
wi start --agent-mode headless item-slug
```

## The agent did not receive the description

`wi start` starts the agent but does not submit `DESCRIPTION.md` as a prompt. In the TUI, ask the agent to run `wi show` and act on the description.

For creation and immediate delivery, use `wi new --title "Task" --desc-file task.md --start`. `--prompt` implies `--start` and overrides only the startup message. Registered managed worktrees receive per-launch Pi project approval, while `.envrc` approval remains separate. A readiness timeout or refused prompt reports a retained partial outcome; it does not report successful delivery. For an existing active item, use:

```bash
wi agent control send --item ITEM 'Read the task with wi show, implement it, and report the result.'
```

## An `.envrc` is not loaded

Starting an agent asks before trusting an unapproved `.envrc`: the daemon's start job waits for an answer, which `wi` asks for on its terminal and `wi web` in the browser. Declining continues without it. With `--json` the command does not ask; it prints the job waiting for approval, which another client can answer with `JobService.AnswerApproval`. An unanswered question expires after ten minutes and the start fails.

Approve the checkout's `.envrc` with `direnv allow /path/to/checkout/.envrc`, or start the agent interactively. Existing agent environments are not changed in place; restart the agent to pick up a newly approved `.envrc`.

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

One durable conversation has at most one running agent. Pi cannot attach its TUI to a live headless process, so changing modes restarts the agent. Starting it in the other mode does that when it is idle, and is refused while it is busy:

```bash
wi agent runtime ensure --mode headless ITEM
# or, for a busy agent, abort its turn and stop it first:
wi agent runtime stop --force ITEM
wi switch ITEM
```

## Shelve or close refuses cleanup

These operations protect dirty, branch-mismatched, active, or currently attached resources.

Inspect each concern:

```bash
wi agent status ITEM
wi workspace status ITEM
```

`wi agent status` shows whether the agent runs and how many clients are attached to its terminal. Review and commit, stash, or otherwise resolve worktree changes with Git. Shelve and close stop an idle agent themselves. `close --force` may abort a busy agent's turn, but it does not discard a dirty checkout; `close --commit-changes` commits the changes to the item's branch instead. Stopping an agent with attached terminal clients needs `--confirm-attached-terminal`.

`wi state set closed` is not a cleanup shortcut: the daemon refuses it while the item still holds a checkout or a running agent.

## Merge refuses the source checkout

`wi merge` requires the expected source branch, no unfinished Git operation or unmerged index entries, and an existing local target branch. Staged, unstaged, untracked, and ignored source files do not need to be removed. Only committed work is integrated; a needed rebase uses a temporary checkout when local source data is present.

```bash
wi workspace status ITEM
git status --short
git branch --list main
```

Commit intended source changes before merging. The registered integration branch must not be checked out in the owned store. For another explicit target, dirty paths in its worktree must not overlap incoming paths. `wi merge` does not create the target branch. When targeting the registered integration branch with an upstream configured, it fetches and fast-forward-synchronizes the base first. Use `--local` for deliberate offline integration. If histories diverge, inspect `wi repo status NAME --fetch`, then choose `wi repo sync NAME --merge` or `--reset-to-upstream --yes`. Patch-equivalent commits can have identical trees but incompatible histories; rebasing only the item onto `origin/main` does not repair stale local integration history.

## Repository tracking needs attention

Git configuration in the owned store is the current upstream authority. `wi repo show NAME` reports missing, incomplete, or unsafe tracking. Use `wi repo upstream NAME URL --branch BRANCH` to configure origin and integration branch tracking, or `--clear` to choose local-only operation without deleting origin.

A pending reconciliation keeps its own frozen destination and blocks configuration changes. Changing Git tracking during reconciliation prevents `--continue`; `--abort` remains available and retains manually committed resolution work under a recovery ref.

Do not restart item agents just to update the daemon; `wi daemon stop` and the next command are enough.

## Agent status reports a problem

Inspect the agent and recent history:

```bash
wi agent status ITEM
wi events ITEM
wi daemon doctor
```

Stale incomplete activity, failed agents, or unavailable processes may produce a problem observation; the agent's `reason` explains it. Branch divergence in a managed slot is reported as `changed` rather than preventing inspection or navigation. Stop a verified stale agent before starting a replacement. Avoid deleting sockets, agent records, or Pi JSONL manually.

## A tmux server restart orphaned TUI agents

Restarting the tmux server (or `tmux kill-server`) removes every wi-owned session while the TUI agent processes keep running, orphaned and unreachable. Stop the verified orphaned agent and switch again, which starts a fresh terminal that resumes the same conversation:

```bash
wi agent runtime stop --force --item ITEM
wi switch ITEM
```

To recover every item at once, stop all agents and terminals:

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

Commands stop safely when the daemon is unavailable; none does the work itself instead. The daemon's log is `daemon.log` in its XDG state directory. A command that reports an unsupported daemon API version reached a daemon of an incompatible build: stop it with its own build, then run the command again. Resolve the reported daemon or build error instead of editing the database or item files.

## Get more command detail

```bash
wi help start
wi help agent runtime stop
wi close --help
```

Command help lists the accepted flags and current safety rules.
