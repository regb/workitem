---
title: Configuration
description: Configure capacity, labels, attention ranking, direnv trust, and display.
---

User configuration lives at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/wi/config.toml
```

A repository may also provide `.config/wi.toml` for supported project settings.

## Example

```toml
[deep_work]
max_active = 2

[item.defaults]
labels = ["team-one"]

[list]
repository_folders = 2
labels = ["+team-one", "-blocked"]

[attention]
priority = "recent-request"

[direnv]
auto_trust_repositories = ["/home/me/src/trusted-project"]

[agent]
startup_message = "Read this work item's description with `wi show`. Work on the task it describes. Run relevant checks and report the outcome."

[agent_status.markers]
busy = "agent"
idle = "review"
problem = "problem"

[drivers.pi.defaults]
executable = "/home/me/bin/pi"

[agents.default]
driver = "pi"

[agents.web-search]
driver = "pi"

[agents.web-search.config]
extensions = ["/home/me/tools/web-search.ts"]
```

## Deep-work capacity

`deep_work.max_active` limits working and waiting items marked as deep work. A value of `0` prevents starting deep backlog work unless you pass `--force`.

## Default labels

New items combine labels from user configuration, `.config/wi.toml` read from the selected starting commit in the owned store, `WI_ITEM_DEFAULT_LABELS`, and explicit `--label` flags. Dirty edits in the external onboarding checkout are not configuration for new items. `--no-default-labels` suppresses configured and environment defaults.

```bash
WI_ITEM_DEFAULT_LABELS="jira,backend" wi new --title "Fix issue"
```

## List filters

List label rules use these forms:

- `label` or `+label` requires a label
- `-label` excludes a label
- `!` clears inherited rules

Configuration, `WI_LIST_LABELS`, and command-line flags apply in that order. Higher-precedence rules replace only a rule for the same normalized label.

```bash
WI_LIST_LABELS="!,+personal" wi list --json
```

## Attention ranking

`attention.priority` selects the ranking strategy used by `wi list`, `wi attention queue`, `wi switch`, and `wi next`. The current strategy is `recent-request`. Unknown names are rejected so a misspelled strategy cannot silently change queue behavior.

More strategies can be added without changing the configuration shape.

## Direnv trust

Interactive runtime startup asks before allowing an untrusted `.envrc`. The user configuration can automatically trust absolute paths for repositories you control.

For commands that consume project-scoped CLI settings, `wi` also reads `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` from an already allowed `.envrc` in the current directory. Explicit caller values win. Other values from `.envrc` are not imported into CLI configuration.

Repository configuration cannot grant execution trust. This prevents a checked-in file from approving its own commands.

## Initial agent message

`agent.startup_message` is the global message sent by `wi new --start`. It must be a non-empty quoted string. Escape newlines as `\n` within the string. `--prompt TEXT` overrides this message for that creation and requires `--start`. Neither message changes the durable description supplied through `--desc-file`.

`wi resume` never sends a startup message. `wi start` for an existing item opens its runtime without submitting a message. Use `wi agent control send` to explicitly start a turn in an existing conversation.

## Coding agents and drivers

Every item records an agent definition name. `default` is selected when `wi new` does not receive `--agent`; without configuration it uses the packaged `pi` driver.

`[agents.<name>]` defines a flat selectable agent profile and selects a registered driver. An optional `label` is safe presentation metadata used by clients such as the web new-item form. The nested `config` table belongs to that driver and is validated by it. The Pi driver accepts `executable`, `extensions`, `model`, and `thinking`; thinking may be `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. `[drivers.<name>.defaults]` provides settings shared by every agent using that driver; agent configuration overrides those defaults.

```toml
[agents.pi-fast]
driver = "pi"
label = "Pi · Fast"
[agents.pi-fast.config]
model = "openai-codex/gpt-5.4"
thinking = "low"

[agents.pi-deep]
driver = "pi"
label = "Pi · Deep"
[agents.pi-deep.config]
model = "openai-codex/gpt-5.4"
thinking = "xhigh"
```

Items retain the selected profile name. Runtime startup resolves the profile and passes its model and thinking configuration through the selected driver.

```bash
wi new --agent web-search --title "Research upstream behavior"
wi new --title "Investigate rendering" --agent web-search --start
```

External driver programs use wi's versioned bootstrap protocol. The standard Pi driver runs as the separate `wi internal-driver pi` process from the installed `wi` binary, so it is always present and has the same build version. A configured `drivers.<name>.command` selects a trusted external driver executable; repository configuration cannot register drivers or agent definitions.

## Environment variables

Use command help to see variables accepted by a specific command. Common variables include:

| Variable | Purpose |
| --- | --- |
| `WI_LIST_LABELS` | Additive default list and attention filters |
| `WI_ITEM_DEFAULT_LABELS` | Labels inherited by new items |
| `NO_COLOR` | Disable colored text output |
| `WI_ID` | Resolve the current item inside managed environments |
| `WI_DIR` | Native item directory |
| `WI_WORKTREE` | Current assigned checkout path |
| `WI_AGENT_CONVERSATION_DIR` | Driver-owned native conversation directory |
| `WI_REPOSITORY` | Owned bare Git store in Wi-created tmux shells; not guaranteed in every headless driver |
| `WI_AGENT_DAEMON_SOCKET` | Use the restricted agent-tool daemon endpoint |

Agent runtimes also receive `WI_AGENT_RUNTIME_ID`, `WI_AGENT_MODE`, `WI_AGENT_CONTROL_SOCKET`, and `WI_AGENT_EVENT_SOCKET`. Tmux panes receive `TMUX` and `TMUX_PANE`; headless runtimes do not require them. Driver-specific variables are implementation details.

Changing into a worktree does not inject these variables, and changing out of one does not clear them. Contextual base selection still validates the actual directory and Git store. Sandboxes may expose a narrower environment.
