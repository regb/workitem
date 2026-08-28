---
title: Install wi
description: Install the current development build and its runtime dependencies.
---

`wi` is currently distributed from source and follows `main`. It targets Linux first and requires Go 1.24 or newer. Expect development releases to change while the command set settles.

## Prerequisites

Install these programs before building `wi`:

- [Go](https://go.dev/doc/install) 1.24 or newer
- [Git](https://git-scm.com/)
- [Pi](https://github.com/badlogic/pi-mono)
- [tmux](https://github.com/tmux/tmux) for interactive TUI mode
- [fzf](https://github.com/junegunn/fzf) for the optional `wi switch` picker

Headless RPC agents do not require tmux or fzf. Pi must be installed and configured for the model provider you intend to use.

Check the prerequisites available for your workflow:

```bash
go version
git --version
pi --help
tmux -V
fzf --version
```

## Build from `main`

```bash
git clone https://github.com/regb/workitem.git
cd workitem
scripts/install-local
```

The installer uses `go install -buildvcs=true`. It installs `wi` into `GOBIN`, or the first GOPATH `bin` directory when `GOBIN` is empty, and prints the installed path.

Make sure the Go binary directory is on `PATH`, then confirm the installation:

```bash
command -v wi
wi version
wi info
```

Development versions include the Git revision and report a dirty checkout. This identifies the code behind a local installation without a manual development version number.

The daemon starts automatically when an ordinary command needs it. You do not need to start tmux before running `wi`; TUI operations create or reuse the required server and sessions.

## Update

Update the source checkout and install the new build:

```bash
cd /path/to/workitem
git pull --ff-only
scripts/install-local
wi version
```

If a command reports that the installed binary and running daemon do not match, stop the old daemon and retry:

```bash
wi daemon stop
wi daemon doctor
```

The next ordinary command starts the installed version.

## Uninstall

First stop wi-owned runtimes, terminals, and the daemon:

```bash
wi shutdown
```

Remove the path printed by `scripts/install-local`:

```bash
gobin="$(go env GOBIN)"
[ -n "$gobin" ] || gobin="$(go env GOPATH | cut -d: -f1)/bin"
rm -- "$gobin/wi"
```

This removes the executable but retains work items and conversations. Use `wi info` before uninstalling if you intend to locate or back up those files. Do not manually edit Pi JSONL sessions.

## Repository hooks

Contributors can opt into repository-local checks:

```bash
scripts/install-hooks
```

The pre-commit hook checks Go formatting and runs the short test suite. The post-commit hook installs a clean committed build. These hooks are optional and affect only this clone.

## Next step

Continue with the [Quick start](../quick-start/). It explains the important difference between starting a TUI and submitting a prompt.
