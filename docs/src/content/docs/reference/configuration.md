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

For commands that consume project-scoped CLI settings, `wi` also reads `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` from an already allowed `.envrc` in the current directory. Explicit caller values win. Other values from `.envrc` are not imported into CLI configuration.

Repository configuration cannot grant execution trust. This prevents a checked-in file from approving its own commands.

## Coding agents and drivers

Every item records an agent definition name. `default` is selected when `wi new` or `wi start --new` does not receive `--agent`; without configuration it uses the packaged `pi` driver.

`[agents.<name>]` selects a registered driver. The nested `config` table belongs to that driver and is validated by it. The Pi driver accepts `executable` and `extensions`. `[drivers.<name>.defaults]` provides settings shared by every agent using that driver; agent configuration overrides those defaults.

```bash
wi new --agent web-search "Research upstream behavior"
wi start --new --agent web-search "Investigate rendering"
```

External driver programs use wi's versioned bootstrap protocol. The standard Pi driver runs as the separate `wi internal-driver pi` process from the installed `wi` binary, so it is always present and has the same build version. A configured `drivers.<name>.command` selects a trusted external driver executable; repository configuration cannot register drivers or agent definitions.

## Docker and SSH executors

```toml
[executors.dev]
driver = "docker"
image = "wi-dev"
network = "none"
```

Select a profile with `wi new --executor dev` or `wi start --new --executor dev`. Docker network accepts `none`, the default, or `bridge`. For SSH, configure an existing host and a dedicated remote root instead:

```toml
[executors.builder]
driver = "ssh"
host = "build-server"
root = "/home/dev/wi"
```

An optional `ssh_config` selects an absolute host-side OpenSSH config path. SSH profiles cannot specify image/network; Docker profiles cannot specify SSH fields. Claims pin these settings when provisioning starts. Unknown profile fields are rejected. Executor work requires the bundled Pi driver.

See [Use executors](../../guides/executors/) for image requirements and limits.

## Environment variables

Use command help to see variables accepted by a specific command. Common variables include:

| Variable | Purpose |
| --- | --- |
| `WI_LIST_LABELS` | Additive default list and attention filters |
| `WI_ITEM_DEFAULT_LABELS` | Labels inherited by new items |
| `NO_COLOR` | Disable colored text output |
| `WI_ID` | Resolve the current item inside managed environments |
| `WI_AGENT_DAEMON_SOCKET` | Use the restricted agent-tool daemon endpoint |
