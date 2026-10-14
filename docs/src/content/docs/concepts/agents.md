---
title: Agents and conversations
description: Agent profiles, driver-owned conversations, and exclusive runtimes.
---

An item selects an agent profile, such as `pi` or `pi-deep`. Profiles choose a driver and its configuration; several profiles can use the same driver. Run `wi init` to discover installed agents and select the default. Profiles are explicit in the user config; `default_agent` must point to an existing named profile. Items store the resolved name rather than the pointer, so changing the default does not reassign existing named-profile items. See [Configuration](../../reference/configuration/#coding-agents-and-drivers).

Each item has at most one primary conversation and one active runtime owner. A runtime can stop and later resume that conversation. The conversation belongs to one bounded task, not an ongoing project chat: archive the item when its objective is settled.

## TUI and headless modes

TUI is the default and runs the driver's interactive backend in tmux:

```bash
wi start item-slug
wi switch item-slug
```

Headless mode does not use tmux:

```bash
wi start --agent-mode headless item-slug
```

These modes are alternatives. To change modes, stop the current owner, wait for it to report offline, then start the other mode:

```bash
wi agent runtime stop item-slug
wi agent runtime status item-slug
```

Bundled Pi and Claude support both modes. Claude headless requires Claude Code 2.1.299 or later and keeps one persistent process for successive turns. Its private output uses API text deltas; the native TUI still uses line batches. Neither Claude mode offers atomic idle-only controls. Drivers must advertise the requested mode. Force cleanup affects only verified Wi-owned processes.

## Observation and input

```bash
wi agent status item-slug
wi agent runtime status item-slug
wi agent monitor --limit 50 item-slug
wi agent control send --item item-slug "Run the focused tests and explain failures."
```

Status reports activity, not task completion. While busy, the default message uses steering semantics; `--follow-up` waits for the run to settle. Use `--file` or `--stdin` for longer messages. Delivery acknowledges handoff, not completion. Unsupported controls are rejected, never simulated with TUI keystrokes.

Task descriptions and startup messages are separate. See [Delegate work](../../guides/delegation/) and [Review agent work](../../guides/review-and-follow-up/).

## Bundled Wi skill

The Pi driver exports the skill embedded in the Wi executable to a private temporary directory and passes it explicitly with `--skill`. It cleans up after the runtime exits and never modifies global skills or settings. No separate skill installation is needed; remove old manual copies to avoid duplicate names.

The Claude driver exports the same bundled skill as a temporary native plugin in both modes.

An already running agent keeps its existing skill and runtime code. Installing a new Wi build affects later runtimes, not existing owners. See the [driver protocol](../../reference/agent-driver-protocol/#bundled-skills).
