---
title: Agents and conversations
description: Agent profiles, driver-owned conversations, and exclusive runtimes.
---

An item selects an agent profile, such as `pi` or `pi-deep`. Profiles choose a driver and its configuration; several profiles can use the same driver. Run `wi init` to discover installed agents and select the default. Profiles are explicit in the user config; `default_agent` must point to an existing named profile. Items store the resolved name rather than the pointer, so changing the default does not reassign existing named-profile items. See [Configuration](../../reference/configuration/#coding-agents-and-drivers).

Each item has at most one primary conversation and one active runtime owner. A runtime can stop and later resume that conversation. The conversation belongs to one bounded task, not an ongoing project chat: close the item when its objective is settled.

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

These modes are alternatives. Taking control from a client that needs the other mode switches it for you (see [Controllers and observers](#controllers-and-observers)). To change modes by hand, stop the current owner, wait for it to report offline, then start the other mode:

```bash
wi agent runtime stop item-slug
wi agent runtime status item-slug
```

Bundled Pi and Claude support both modes. Claude headless requires Claude Code 2.1.299 or later and keeps one persistent process for successive turns. Its private output uses API text deltas; the native TUI still uses line batches. Neither Claude mode offers atomic idle-only controls. Drivers must advertise the requested mode. Force cleanup affects only verified Wi-owned processes.

## Channels

Each running runtime exposes up to three channels, reported by its driver at startup:

| Runtime | Observe | Control | Terminal |
| --- | --- | --- | --- |
| Pi headless | yes | structured, atomic idle guard | no |
| Pi TUI | yes | structured through its extension bridge, atomic idle guard | yes |
| Claude headless | yes | structured; idle-only prompts are checked against observed status | no |
| Claude TUI | yes | emulated with tmux keystrokes | yes |

Observing (history and live output) works for every runtime. Clients differ in what they need to control: a browser client, such as a web UI built on wi's control API, needs structured control; a tmux client needs the terminal channel.

## Controllers and observers

Each agent has at most one **controller**, the only client allowed to send input, and any number of **observers**. The controller holds a lease kept in the daemon's memory; leases never persist across a daemon restart. Lease holders are terminals and browser clients (holder `web`); a browser lease expires unless renewed.

- **Vacant control is invisible.** The first client that sends takes the lease. If the runtime lacks the channel it needs and is idle, Wi restarts it in the right mode on the same conversation without asking: a browser client restarts a keystroke-driven TUI headless, and `wi switch` restarts a headless runtime as a TUI.
- **Taking over is explicit.** When another client controls the agent, `wi switch --take` (or a browser client's takeover) asks first. Taking over aborts a turn in flight, and may restart the runtime as above.
- **Terminals become read-only.** While a browser client controls a TUI runtime, Wi disables input to its tmux pane; attached terminals keep rendering. `wi switch` without `--take` enters read-only. When the browser lease expires, the next status check re-enables input.
- **A terminal counts as controlling** while a tmux client is attached to the item's session, even without an explicit lease.

Structured sends validate the current lease as well as terminal entry. Browser callers identify their holder, client, and lease generation; stale generations are rejected. Plain CLI sends share the terminal identity and cannot send while a browser controls the agent. Browser leases default to 45 seconds and need renewal.

The lease is enforced for clients that go through Wi. A raw `tmux attach` that bypasses Wi can still type into an enabled pane.

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
