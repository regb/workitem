---
title: Use tmux
description: Enter TUI agents and navigate active work through tmux.
---

Tmux is optional. `wi` uses it as the human-access layer for native Pi TUI sessions. RPC mode remains tmux-independent.

## Enter an item

```bash
wi switch auth
```

With an explicit item, `switch` validates active work, ensures its worktree and TUI runtime, then enters its wi-owned tmux session.

Without an item, `switch` opens an `fzf` picker:

```bash
wi switch
```

The picker shows working and waiting items first, followed by backlog items in a distinct color. It initially selects the current item when that item is present. Typing a filter moves the cursor to the first match. Selecting a waiting item resumes it; selecting a backlog item starts it. Either choice then enters the item's TUI.

## Navigate by attention

```bash
wi next
wi next --defer
wi next --wait
```

`next` chooses from work that currently needs attention. It is not a static tmux window rotation.

## Low-level terminal commands

Use these when you need direct control over access without starting an agent or changing lifecycle:

```bash
wi terminal status --item auth
wi terminal ensure --item auth
wi terminal enter --item auth
wi terminal close --item auth
```

`terminal ensure` requires an existing workspace. It does not create a conversation or runtime.

## Add tmux bindings

Add the commands you want directly to `~/.tmux.conf`:

```text
# Open the picker in a popup.
bind-key O display-popup -EE \
  -d '#{pane_current_path}' -w 90% -h 80% -T 'wi work items' \
  'WI_TMUX_CLIENT="#{client_name}" wi switch'

# Run navigation in a popup. It closes on success and remains on error.
bind-key N display-popup -EE \
  -d '#{pane_current_path}' -w 80% -h 40% -T 'wi next' \
  'WI_TMUX_CLIENT="#{client_name}" wi next'
bind-key P display-popup -EE \
  -d '#{pane_current_path}' -w 80% -h 40% -T 'wi defer current' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --defer'
bind-key W display-popup -EE \
  -d '#{pane_current_path}' -w 80% -h 40% -T 'wi wait current' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --wait'
bind-key A display-popup -EE \
  -d '#{pane_current_path}' -w 80% -h 40% -T 'wi archive current' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --archive'
```

Reload tmux:

```bash
tmux source-file ~/.tmux.conf
```

These are ordinary tmux bindings, not a plugin. Change the keys, popup dimensions, or commands directly in your configuration. `WI_TMUX_CLIENT` tells `wi` which client initiated the command when more than one client is attached.

Two `-E` flags make tmux close a popup only when its command succeeds. On failure, the full `wi` error remains visible until you dismiss the popup with `Escape` or `Ctrl-C`. This is clearer than redirecting stderr and briefly displaying a generic status-bar message. If you prefer status-bar errors, remove the stderr redirect, increase tmux's `display-time`, and display the captured error rather than a fixed `wi next failed` string. The status bar is still a poor fit for multiline errors.

If your list filters come from a repository `.envrc`, wrap the command with `direnv exec .`. For example:

```text
bind-key O display-popup -EE \
  -d '#{pane_current_path}' -w 90% -h 80% -T 'wi work items' \
  'WI_TMUX_CLIENT="#{client_name}" direnv exec . wi switch'
```

## JSON does not attach

```bash
wi --json switch auth
wi --json next
```

JSON mode may select and ensure a target, but it never moves the invoking tmux client. This keeps structured commands safe for scripts.
