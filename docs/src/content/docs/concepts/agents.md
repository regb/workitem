---
title: Agents and conversations
description: Agent profiles, driver-owned conversations, and the agent that runs them.
---

An item selects an agent profile, such as `pi` or `claude`. Profiles choose a driver and its settings; several profiles can use the same driver, for example one per account. Wi's daemon keeps them, managed with `wi agent profile` or in the web Settings → Agents page; the first `wi` command adds one for each agent it finds installed. Items store the profile's name, so changing the default does not reassign existing items. If an item's profile is removed or renamed, its agent cannot start until `wi agent profile set PROFILE` moves it to another. The conversation belongs to its driver, so the new profile must use the same driver. See [Configuration](../../reference/configuration/#coding-agents-and-drivers).

An item can also choose its own model and reasoning level, which override its profile's every time its agent starts. Reasoning is one ladder for every driver (`off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`); drivers and models accept a subset.

```bash
wi new --model opus --reasoning xhigh --title "Plan the migration"
wi start --reasoning low item-slug
wi agent settings set model=sonnet
```

A running agent reports the model and reasoning level it uses, and `wi agent settings` and the web client show them. When neither the item nor its profile chooses a model, the item records the one its agent first runs with, so the conversation keeps it even if the agent's default changes. A model or reasoning level changed in the agent's own interface, such as Pi's `/model`, becomes the item's setting too. Resetting the item's settings returns it to the profile, until the next start records again.

Each item has at most one primary conversation, and its agent is that conversation, running or not. Starting the agent resumes the conversation; stopping it keeps the conversation for the next start. Each start gives the agent a new **generation**, which clients can pass back so their input never reaches an agent started since they looked. The daemon runs the agent: which process, and how it is hosted, is its business. Clients see whether the agent is online, its mode, its status, what it can do, and its generation. The conversation belongs to one bounded task, not an ongoing project chat: close the item when its objective is settled.

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

These modes are alternatives. Taking control from a client that needs the other mode switches it for you (see [Controllers and observers](#controllers-and-observers)). Starting an agent in the other mode restarts it in that mode when it is idle, and is refused while it is busy. To change modes by hand, stop the agent, then start it in the other mode:

```bash
wi agent runtime stop item-slug
wi agent runtime ensure --mode headless item-slug
wi agent runtime restart item-slug     # same mode, current settings and repository variables
```

Bundled Pi and Claude support both modes. Claude headless requires Claude Code 2.1.299 or later and keeps one persistent process for successive turns. Its private output uses API text deltas; the native TUI still uses line batches. Neither Claude mode offers atomic idle-only controls. Claude headless runs without its `AskUserQuestion` tool: nothing can answer a question there, so the agent decides on its own instead of stalling the turn. In the Claude TUI, a question, plan approval or permission prompt pauses the turn: Wi reports the agent as waiting for your answer, under NEEDS ATTENTION rather than NEEDS FIXING, until you answer it in the terminal. Drivers must advertise the requested mode. Force cleanup affects only verified Wi-owned processes.

## Channels

Each running agent exposes up to three channels, reported by its driver at startup:

| Agent | Observe | Control | Terminal |
| --- | --- | --- | --- |
| Pi headless | yes | structured, atomic idle guard | no |
| Pi TUI | yes | structured through its extension bridge, atomic idle guard | yes |
| Claude headless | yes | structured; idle-only prompts are checked against observed status | no |
| Claude TUI | yes | emulated with tmux keystrokes | yes |

Observing (history and live output) works in every mode. Clients differ in what they need to control: a browser client needs structured control; a tmux client needs the terminal channel; the command line (`wi agent control send`) uses whichever control channel the agent has, structured or emulated keystrokes, so it never restarts an agent in another mode and does not displace a terminal someone is watching. Its control expires on its own, like a browser's.

## The terminal

An agent in TUI mode runs its native interface in a tmux window, and reports where: the tmux socket, session and window, and how many clients are attached. `wi switch` and the other navigation commands read that and attach with tmux themselves; the daemon starts the terminal but does not mediate attaching. TUI mode exists for what the structured channel does not cover, such as answering the agent's own questions or permission prompts. Stopping an agent with attached clients disconnects them, so it needs confirmation.

## Controllers and observers

Each agent has at most one **controller**, the only client allowed to send input, and any number of **observers**. The controller holds a lease kept in the daemon's memory; leases never persist across a daemon restart. Lease holders are terminals and browser clients (holder `web`); a browser lease expires unless renewed.

- **Vacant control is invisible.** The first client that sends takes the lease. If the agent lacks the channel it needs and is idle, Wi restarts it in the right mode on the same conversation without asking: a browser client restarts a keystroke-driven TUI headless, and `wi switch` restarts a headless agent as a TUI.
- **Taking over is explicit.** When another client controls the agent, `wi switch --take` (or a browser client's takeover) asks first. Taking over aborts a turn in flight, and may restart the agent as above. The daemon first describes what taking over would do; confirming sends back a token from that description, and a takeover whose situation changed since is refused, so the client asks again.
- **Terminals become read-only.** While a browser client controls a TUI agent, Wi disables input to its tmux pane; attached terminals keep rendering. `wi switch` without `--take` enters read-only. When the browser lease expires, the next status check re-enables input.
- **A terminal counts as controlling** while a tmux client is attached to the item's session, even without an explicit lease.

Structured sends validate the current lease as well as terminal entry. Each client names itself to the daemon; a send carries the client's lease, and a lease that is no longer current is rejected. CLI commands identify as `cli`, or as `WI_CLIENT_ID` when it is set, and cannot send while a browser controls the agent. Browser leases default to 45 seconds and need renewal.

The lease is enforced for clients that go through Wi. A raw `tmux attach` that bypasses Wi can still type into an enabled pane.

## Observation and input

```bash
wi agent status item-slug
wi agent monitor --content --item item-slug
wi agent control send --item item-slug "Run the focused tests and explain failures."
```

`wi agent status` reports the agent: whether it is online, its mode, status, capabilities, generation, terminal and who controls it. `wi agent monitor --content` follows what the agent is doing now: the reply as it is written, shared reasoning, tool calls and activity; ending it never affects the agent. Status reports activity, not task completion. While busy, the default message uses steering semantics; `--follow-up` waits for the run to settle. Use `--file` or `--stdin` for longer messages. Delivery acknowledges handoff, not completion. Unsupported controls are rejected, never simulated with TUI keystrokes.

Task descriptions and startup messages are separate. See [Delegate work](../../guides/delegation/) and [Review agent work](../../guides/review-and-follow-up/).

## Bundled Wi skill

The Pi driver exports the skill embedded in the Wi executable to a private temporary directory and passes it explicitly with `--skill`. It cleans up after the agent exits and never modifies global skills or settings. No separate skill installation is needed; remove old manual copies to avoid duplicate names.

The Claude driver exports the same bundled skill as a temporary native plugin in both modes.

An already running agent keeps its existing skill and driver code. Installing a new Wi build affects agents started by a daemon of that build, not running ones. See the [driver protocol](../../reference/agent-driver-protocol/#bundled-skills).
