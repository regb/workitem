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

# Run navigation silently. On failure, -E opens stderr in tmux view mode.
bind-key N run-shell -b -E -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next >/dev/null'
bind-key P run-shell -b -E -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --defer >/dev/null'
bind-key W run-shell -b -E -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --wait >/dev/null'
bind-key A run-shell -b -E -c '#{pane_current_path}' \
  'WI_TMUX_CLIENT="#{client_name}" wi next --archive >/dev/null'
```

Reload tmux:

```bash
tmux source-file ~/.tmux.conf
```

These are ordinary tmux bindings, not a plugin. Change the keys, popup dimensions, or commands directly in your configuration. `WI_TMUX_CLIENT` tells `wi` which client initiated the command when more than one client is attached.

For `run-shell`, `-E` redirects stderr to stdout so tmux displays failures in view mode. Redirecting ordinary stdout keeps successful navigation silent. The picker uses `display-popup -EE`; two `-E` flags close that popup on success but leave it open when selection or startup fails.

`wi` reads `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` from an allowed `.envrc` in the command's working directory. Tmux bindings do not need to invoke `direnv`. Explicit caller values still take precedence, and `wi` does not import paths, socket locations, or identity variables from `.envrc` into CLI configuration.

## JSON does not attach

```bash
wi --json switch auth
wi --json next
```

JSON mode may select and ensure a target, but it never moves the invoking tmux client. This keeps structured commands safe for scripts.
