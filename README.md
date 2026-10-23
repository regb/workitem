# WorkItem

`wi` is a human-first, productivity-oriented command-line utility for organizing your work items.

A work item records a title, task description, repository, lifecycle, and history. When work starts, Wi can materialize:

- a managed Git worktree from an independent registered repository
- a selected coding agent and durable primary conversation
- a tmux session for TUI access, or a tmux-independent headless runtime

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
source with `scripts/install-local`. `wi version` reports build identity and includes the Git revision when Go provides VCS metadata. Some managed-worktree builds report `devel` without a revision.

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

Register a repository, then create and start work from a task description:

```bash
wi init
wi repo add project --from /path/to/git/repository --branch main
# Upstream is inferred from the checkout’s origin, if available.
# Use --local during onboarding for local-only integration.
wi repo status project --fetch
wi repo sync project
wi new --title "Add API retries" --repo project --desc-file task.md --start
wi switch add-api-retries
```

Supply `--title` or explicitly use `--untitled`; Wi derives the slug unless `--slug` is supplied. Omit both `--start` and `--prompt` to file backlog work only. With `--start`, a configurable message asks the agent to read the description using `wi show`. `--prompt` implies `--start` and supplies that message, never the description. Resume never sends a prompt.

Registering a repository authorizes project-local agent resources in its verified managed worktrees. Pi receives per-launch project approval; direnv `.envrc` approval remains separate.

Inside a Wi item, repository selection is implicit. Outside one, pass a registered `--repo NAME`. External source checkouts are onboarding inputs, not ongoing dependencies. See [Repositories](https://regb.github.io/workitem/concepts/repositories/) and the [Quick start](https://regb.github.io/workitem/getting-started/quick-start/).

Inspect, review, integrate, and close the work:

```bash
wi list
wi show add-api-retries
wi agent status add-api-retries
wi workspace status add-api-retries
wi merge --item add-api-retries main
wi close add-api-retries
```

Use `wi help <command>` for current flags and safety rules.

## Browser access over Tailscale

`wi web` serves a work list and agent chat to a browser. It binds only to
loopback, so to reach it from your other devices put Tailscale in front of it.

### Install on the machine

From source, with Go and Node.js 24 or newer (Node.js only builds the browser
client into the binary):

```bash
git clone -b web/frontend https://github.com/regb/workitem.git
cd workitem
scripts/install-local --clean
```

With Nix, which builds the client from its lockfile and embeds it:

```bash
nix profile install github:regb/workitem/web/frontend
# or pin a revision: github:regb/workitem/<commit>
```

A plain `go install` embeds only a placeholder page. Then run `wi init` and
register repositories as in the quick start, as the user whose agents and Git
credentials Wi should use, never as root.

### Expose it to your tailnet

Serve on a fixed loopback port, declare the exact address browsers will use,
and keep a persistent access secret in a private file:

```bash
wi web --port 8787 \
  --access-url https://HOST.TAILNET.ts.net:8448 \
  --token-file ~/.local/state/wi/web-token
tailscale serve --bg --https=8448 http://127.0.0.1:8787
```

Open `https://HOST.TAILNET.ts.net:8448/#token=` followed by the contents of
the token file. The page keeps the secret for that tab.

- The secret grants operator authority: reading conversations, starting
  agents, closing work. Keep the token file private; delete it to rotate.
  `wi web` never prints a persistent secret.
- Use `tailscale serve`, never `tailscale funnel`: the page must not be on the
  public Internet. Restrict which tailnet users and devices can reach the
  machine with Tailscale access controls.
- Do not open port 8787 in a firewall or bind it elsewhere; only Tailscale
  forwards to it.
- `--no-token` drops the secret and relies on the tailnet alone: any device
  allowed through, and any local process, then has operator authority.

### Keep it running

- **Simply:** run `wi web` in a terminal or tmux window. Commands start the
  daemon on demand, and a newer `wi` replaces an older daemon automatically.
  This suits a workstation you use directly.
- **As services:** on an always-on machine, let systemd (or launchd) own both
  the daemon and `wi web`. Set `[daemon] managed = true` in `config.toml`, so
  that no `wi` command starts or replaces the supervised daemon, and run both
  with `KillMode=process`, so restarts leave running agents alone. The
  [web chat guide](docs/src/content/docs/guides/web-chat.md#run-as-a-service)
  has the unit files.

Stopping `wi web` or the daemon never stops running agents.

## Requirements

- Linux
- Go 1.24 or newer
- Git
- [Pi](https://pi.dev) for the default packaged agent driver
- tmux for interactive TUI runtimes
- fzf 0.71+ for the optional `wi switch` picker and `@` file mentions in `wi web`
- Node.js 24+ and npm to build the `wi web` client from source (not to run it)

Headless runtimes do not require tmux or fzf.

## Tmux bindings

The [tmux guide](https://regb.github.io/workitem/guides/tmux/) is the canonical binding recipe for navigation, the picker, and the optional prompt-only creation popup. The popup uses Pi to generate a title before calling Wi; title generation is not part of `wi new`.

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
