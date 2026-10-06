---
title: Run wi on a server
description: Use an always-on Linux host as a Wi workstation and attach through SSH.
---

`wi` is local-first. One host owns a data root, its coordinator daemon, native conversations, coding-agent processes, Git checkouts, and tmux sessions. That host can be a laptop or an always-on Linux server.

When Wi runs on a server, SSH makes the server feel like a persistent terminal workstation. The server is still "local" from Wi's point of view. SSH is an access method, not part of work-item identity.

## Basic topology

```text
laptop terminal
  -> SSH
     -> server tmux
        -> wi work-item sessions
           -> Pi TUI and shells

server
  -> wi daemon
  -> wi.db and descriptions
  -> native Pi conversations
  -> repositories and managed worktrees
```

The server owns tmux. You do not need Wi or a Wi-managed tmux layout on the laptop.

If the SSH connection drops or the laptop sleeps, server tmux and Pi continue running. Reconnect from the same laptop or another terminal and attach to the existing server tmux.

## Install on the server

Install Git, Pi, tmux, fzf, Go, and Wi on the Linux host. Configure Pi and any model credentials for the account that runs Wi.

Use a dedicated Unix account unless you deliberately want Wi to share an existing development account. Protect its home directory and back up the Wi data and state roots.

See [Install wi](../../getting-started/installation/) for the source installation steps.

## Attach over SSH

A simple entry command is:

```bash
ssh -t wi.example.com 'tmux new-session -A -s workstation'
```

Run `wi` inside that tmux server:

```bash
wi list
wi switch
wi next
```

Install the bindings from [Use tmux](../tmux/) in the server account's `~/.tmux.conf`. The bindings and `wi` commands execute on the server.

You may run SSH inside a personal laptop tmux, but Wi does not depend on that outer tmux. Avoid duplicating Wi navigation bindings in both layers.

## Reconnect from another laptop

SSH authenticates the client. Wi state does not live on that client, so another laptop can attach to the same server account and resume work.

Tmux permits several attached clients. Two clients controlling the same Pi TUI can interfere with each other's input and terminal size. Until Wi has explicit controller and observer roles, use one controlling client per interactive agent session.

## Clipboard and scrollback

Server tmux owns the real scrollback. Enter copy mode there to inspect output produced before the current SSH connection.

OSC 52 can copy a server tmux selection to the laptop terminal clipboard. Configure the server tmux with:

```text
set-option -s set-clipboard on
```

The laptop terminal must accept OSC 52. If SSH runs inside a laptop tmux, that outer tmux must also accept clipboard updates.

## Repository placement

The simplest setup keeps repositories and managed worktrees on the server. Clone repositories there and run `wi new` from the server checkout.

A repository-home workspace refers to the primary checkout on the Wi host. In a hosted setup, that means the server checkout, not a checkout on the laptop.

Work created from uncommitted laptop files does not appear on the server automatically. Commit and push it, copy it deliberately, or wait for a future bundle-upload workflow.

## Optional remote workers

Remote executors keep Pi, conversations, tmux, and the coordinator on the Wi host while placing selected managed workspaces on worker machines:

```text
server Pi
  -> claim-fenced remote tools
     -> worker managed workspace
```

This lets one workstation use larger build machines, alternate operating systems, cloud development sandboxes, or several workers. The user-facing Pi TUI remains in server tmux.

Every operation is fenced with the current workspace claim. Remote tools preserve Pi's independent Bash-call semantics rather than injecting commands into a persistent shell. See [Remote executors](../remote-executors/) for setup and current limits.

## Web access

A future web application can use a restricted network API to display work items, attention, lifecycle, observations, history, and decoded conversation content. It should query canonical Wi state rather than scrape tmux panes.

An optional browser terminal could attach an `xterm.js` client to a constrained server PTY and server tmux. That is separate from the structured work-item API.

The daemon's local operator socket must not be exposed directly. A network service needs authentication, authorization, idempotent commands, bounded event streams, and audit records.

## Availability boundary

An always-on server survives laptop sleep, but it is still one Wi host. Its daemon, local database, native conversations, tmux server, and local runtime processes are not currently replicated.

Back up important data and keep Git changes committed or otherwise copied elsewhere. A future highly available deployment would need one fenced active coordinator writer, replicated durable content, and replaceable runtime hosts. Sharing the current SQLite database over a network filesystem is not a safe substitute.
