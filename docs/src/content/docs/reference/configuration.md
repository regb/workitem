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

[agent_status.markers]
busy = "agent"
idle = "review"
problem = "problem"
```

## Deep-work capacity

`deep_work.max_active` limits working and waiting items marked as deep work. A value of `0` prevents starting deep backlog work unless you pass `--force`.

## Default labels

New items combine labels from user configuration, repository configuration, `WI_ITEM_DEFAULT_LABELS`, and explicit `--label` flags. `--no-default-labels` suppresses configured and environment defaults.

```bash
WI_ITEM_DEFAULT_LABELS="jira,backend" wi new "Fix issue"
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

Repository configuration cannot grant execution trust. This prevents a checked-in file from approving its own commands.

## Environment variables

Use command help to see variables accepted by a specific command. Common variables include:

| Variable | Purpose |
| --- | --- |
| `WI_LIST_LABELS` | Additive default list and attention filters |
| `WI_ITEM_DEFAULT_LABELS` | Labels inherited by new items |
| `NO_COLOR` | Disable colored text output |
| `WI_ID` | Resolve the current item inside managed environments |
| `WI_AGENT_DAEMON_SOCKET` | Use the restricted agent-tool daemon endpoint |
