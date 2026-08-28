# WorkItem

`wi` is a human-first, productivity-oriented command-line utility for organizing your work items.

For each work item, you get:

- a Git worktree
- an associated durable [Pi](https://pi.dev) session
- a tmux session you can attach to

`wi` automatically tracks which work items need your attention and provides fast switching to actionable ones.

## Why wi?

Working directly in the terminal through tools like tmux, Vim, and general Unix
utilities is an incredibly productive way to get things done. Many people,
including myself, try to work exclusively in that environment for most
day-to-day tasks. When we are forced to leave the terminal, we feel the
productivity hit.

One typical reason to consult an external system is task management. Whether it
is GitHub Issues, Jira, Todoist, or another way to organize work, `wi` attempts
to unify task management with a native terminal experience built on tmux.

This context switching has always existed, but programmers have generally been
able to focus on a few large tasks and keep the active work in their heads.
Agentic engineering takes the need for context switching to a new level. `wi`
provides a tmux-native way to track that work explicitly.

Many agentic development tools are growing in popularity, including the labs
own applications such as Codex and Claude, as well as terminal-oriented options
such as Herdr. They organize work around agent sessions and threads, often with
isolated worktrees, but tend to redefine the wider development workflow rather
than augment the terminal and tmux ecosystem. They also tend to use a simple
one-session, one-context model. `wi` instead brings ideas from issue tracking
and GTD directly into the terminal and unify them with agentic workflows.

Read the [documentation](https://regb.github.io/workitem/) for installation,
concepts, workflows, and command reference.

## Project status

`wi` is pre-release software. The command set and stored data may change between
commits, and only the latest `main` branch is supported. For now, install from
source with `scripts/install-local`. `wi version` includes the Git commit so a
local build can be identified precisely.

Do not rely on `wi` for production use or as the only copy of important work.
Bugs may corrupt or delete work-item data, worktrees, or uncommitted files. Keep
important changes committed and maintain separate backups. Use this software at
your own risk.

## Quick start

Install the current checkout:

```bash
git clone https://github.com/regb/workitem.git
cd workitem
scripts/install-local
```

Create backlog work and start it interactively:

```bash
cd /path/to/git/repository
wi start --new "Add API retries"
```

Inspect, review, integrate, and archive the work:

```bash
wi list
wi show add-api-retries
wi agent status add-api-retries
wi workspace status add-api-retries
wi merge --item add-api-retries main
wi archive add-api-retries
```

Use `wi help <command>` for current flags and safety rules.

## Requirements

- Linux
- Go 1.24 or newer
- Git
- [Pi](https://pi.dev)
- tmux for interactive TUI runtimes
- fzf for the optional `wi switch` picker

Headless RPC runtimes do not require tmux or fzf.

## Tmux bindings

`wi` is designed primarily to work with `tmux`. Bind the commands you want
directly in `~/.tmux.conf`, we recommend the following:

```tmux
bind-key O display-popup -EE \
  -d '#{pane_current_path}' -w 90% -h 80% -T 'wi work items' \
  'WI_TMUX_CLIENT="#{client_name}" wi switch'

bind-key N run-shell -b -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next >/dev/null 2>&1 || tmux display-message "wi next failed"'
bind-key P run-shell -b -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --defer >/dev/null 2>&1 || tmux display-message "wi next --defer failed"'
bind-key W run-shell -b -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --wait >/dev/null 2>&1 || tmux display-message "wi next --wait failed"'
bind-key A run-shell -b -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --archive >/dev/null 2>&1 || tmux display-message "wi next --archive failed"'
```

## Development

```bash
go test ./...
go test -race -count=1 ./...
go vet ./...
```

The Astro documentation site lives in `docs/`:

```bash
cd docs
npm ci
npm run check
npm run build
npm run test:site
```

## License

`wi` is available under the [MIT License](LICENSE).
