---
title: Use tmux
description: Enter TUI agents and navigate active work through tmux.
---

Tmux is optional. `wi` uses it as the human-access layer for native Pi TUI sessions. Headless mode remains tmux-independent.

## Enter an item

```bash
wi switch auth
```

With an explicit item, `switch` validates active work, ensures its worktree and TUI runtime, then enters its wi-owned tmux session. Switching marks the conversation read and takes [control](../../concepts/agents/#controllers-and-observers) when nobody else holds it. If another client, such as a browser, controls the agent, the TUI opens read-only: pane input stays disabled and a status message explains how to take over. `wi switch --take` aborts a turn in flight, replaces a headless runtime with a TUI on the same conversation, and re-enables input.

Without an item, `switch` opens an `fzf` picker:

```bash
wi switch
```

The picker shows active items first (including waiting ones), followed by backlog items in a distinct color. It initially selects the current item when that item is present. Typing a filter moves the cursor to the first match. Rows show **status, title, repository**, in that order. Status and repository widths come from the longest values in the full candidate snapshot and stay stable while filtering. The title takes the remaining list-pane width and truncates at the end rather than wrapping. Resizing the terminal or toggling the preview recalculates the title width without refreshing candidates or changing the selected item. Full titles remain searchable even when truncated. The slug stays in the details preview; older items without a title use it as a fallback. The preview uses 40% of the width on wide terminals and moves below the list on narrower terminals, adapting when resized. Press `Alt-P` to hide or show it and give the list the full window. Selecting a backlog item starts it; selecting an active item, waiting or not, is state-neutral. Unread titles are bold, and what a waiting item waits for follows its title in yellow. Either choice then enters the item's TUI.

## Navigate by attention

```bash
wi next
wi next --defer
wi next --wait --until tomorrow
wi next --wait --on -
wi next --close --resolution abandoned
```

`next` chooses from work that currently needs attention. It is not a static tmux window rotation. `--wait` makes the current item wait first, with the same conditions as `wi wait`; `--on -` picks the other item interactively. `--close` closes the current item once a distinct successor is open.

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
# Work-item navigation. Each binding runs in the current pane's directory so
# wi resolves the current item; WI_TMUX_CLIENT names the client to switch.
# Preserve tmux's built-in prefix+w and prefix+n actions.

# Pick an item in a popup.
bind-key O display-popup -EE -d "#{pane_current_path}" -w 90% -h 80% \
  -T "wi work items" "WI_TMUX_CLIENT=#{client_name} wi switch"

# Next item that needs attention; defer the current one first.
# Navigation stays silent on success; tmux shows stderr on failure.
bind-key N run-shell -b -E -c "#{pane_current_path}" \
  "WI_TMUX_CLIENT=#{client_name} wi next >/dev/null"
bind-key P run-shell -b -E -c "#{pane_current_path}" \
  "WI_TMUX_CLIENT=#{client_name} wi next --defer >/dev/null"

# Wait menu: snooze, or wait on another item, then go to the next item.
# A snooze ends on its own when the time passes or when you next prompt the item.
bind-key W display-menu -T "Wait, then go to next" \
  "Snooze 1 hour"           1 "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --wait --until 1h >/dev/null'" \
  "Snooze 4 hours"          4 "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --wait --until 4h >/dev/null'" \
  "Snooze until tomorrow"   t "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --wait --until tomorrow >/dev/null'" \
  "Snooze until Monday"     m "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --wait --until monday >/dev/null'" \
  "" \
  "Wait on another item…"   i "display-popup -EE -d '#{pane_current_path}' -w 90% -h 80% -T 'Wait on another item' 'WI_TMUX_CLIENT=#{client_name} wi next --wait --on -'" \
  "" \
  "Stop waiting"            c "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi wait --clear >/dev/null'"

# Close menu: close the current item with a resolution, then go to the next item.
bind-key X display-menu -T "Close, then go to next" \
  "Completed"   c "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --close --resolution completed >/dev/null'" \
  "Abandoned"   a "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --close --resolution abandoned >/dev/null'" \
  "Superseded"  s "run-shell -b -E -c '#{pane_current_path}' 'WI_TMUX_CLIENT=#{client_name} wi next --close --resolution superseded >/dev/null'"
```

| Key | Action |
| --- | --- |
| `prefix O` | Item picker |
| `prefix N` | Next item needing attention |
| `prefix P` | Defer the current item, then next |
| `prefix W` | Wait menu: `1` snooze 1 hour, `4` 4 hours, `t` until tomorrow 09:00, `m` until Monday 09:00, `i` wait on another item (picker), `c` stop waiting |
| `prefix X` | Close menu: `c` completed, `a` abandoned, `s` superseded, then next |

The wait and close entries use tmux menus (`display-menu`, tmux 3.0 or later): press the key, then the letter shown. A snooze ends on its own when its time passes or when you next prompt the item. Waiting on an event or a pull request is left to the CLI and to agents, for example `wi wait --for` or `wi github wait`.

Reload tmux:

```bash
tmux source-file ~/.tmux.conf
```

These are ordinary tmux bindings, not a plugin. Change the keys, popup dimensions, or commands directly in your configuration. `WI_TMUX_CLIENT` tells `wi` which client initiated the command when more than one client is attached.

For `run-shell`, `-E` redirects stderr to stdout so tmux displays failures in view mode. Redirecting ordinary stdout keeps successful navigation silent. Pickers use `display-popup -EE`; two `-E` flags close the popup on success but leave it open when selection or startup fails.

`wi` reads `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` from an allowed `.envrc` in the command's working directory. Tmux bindings do not need to invoke `direnv`. Explicit caller values still take precedence, and `wi` does not import paths, socket locations, or identity variables from `.envrc` into CLI configuration.

New Wi runtimes include the directory of the running Wi executable in PATH, even if the tmux server predates its installation. This preserves the clean server environment without copying another work item’s PATH or secrets.

## Create from a prompt popup

The external `scripts/wi-tmux-new` helper takes a single-line prompt, asks Pi for a short title, then calls `wi new --title=... --start --prompt=... --json` and enters the new TUI. It requires the current item’s `WI_ID`; repository selection stays implicit. The prompt is sent unchanged, the description stays empty, and Wi derives the slug. Blank input cancels.

Install the helper from the Wi source checkout. Python 3, Pi, and Wi must be on PATH:

```bash
install -Dm755 scripts/wi-tmux-new ~/.local/bin/wi-tmux-new
```

Add a binding:

```text
bind-key I run-shell -b \
  'tmux display-popup -EE -c #{q:client_name} -d #{q:pane_current_path} -w 90% -h 50% -e WI_ID=#{q:WI_ID} -e WI_TMUX_CLIENT=#{q:client_name} -T "wi new prompt" "$HOME/.local/bin/wi-tmux-new"'
```

Prefix+I leaves tmux's default prefix+C customize-mode binding intact. The naming call uses your global Pi model and credentials, with tools, extensions, MCP, project configuration, and context files disabled. Its conversation is not saved. Naming failures create nothing; later startup failures retain the work item and leave the error visible in the popup. Resume never repeats the naming call or prompt.

`run-shell` expands the current item and invoking client before calling `display-popup`. The `q:` modifiers quote those values for the shell. Do not put unexpanded formats directly in `display-popup -e` or its shell command; they are passed literally.

Test the harness without an agent or tmux server using `python3 scripts/test_wi_tmux_new.py`. Test the popup environment in an isolated tmux server with `python3 scripts/test_wi_tmux_popup.py`.

## JSON does not attach

```bash
wi --json switch auth
wi --json next
```

JSON mode may select and ensure a target, but it never moves the invoking tmux client. This keeps structured commands safe for scripts.
