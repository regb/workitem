---
title: Agents and conversations
description: Separate agent definitions, drivers, durable conversations, and replaceable runtimes.
---

Each work item selects one agent definition and has at most one durable primary conversation. Only one runtime may own that conversation at a time.

An **agent definition**, or agent profile, is the flat configured agent selected by a user, such as `default`, `pi-deep`, or `claude-opus`. A **driver** integrates an agent family with wi. Multiple profiles may use the same driver with different models, thinking levels, extensions, and other settings. The implicit `default` profile uses the standard Pi driver when no configuration overrides it.

```bash
wi new --agent web-search --title "Research upstream behavior"
wi new --title "Investigate rendering" --agent web-search --start
```

Creation requires a [registered repository](../repositories/); outside a current item, add `--repo NAME`. Startup messages are separate from durable task descriptions; see [Delegate work](../../guides/delegation/).

The conversation is part of the item's history. The runtime is a replaceable driver and backend process that opens and controls it. Stopping a runtime does not discard the conversation.

## Built-in Wi skill

Wi includes the skill bundled with its host binary in every new runtime's launch request. The backend driver exports it as a temporary resource. Before TUI or headless startup, the Pi driver writes a private skill directory and passes its absolute path with `--skill`. No separate skill checkout or Pi configuration entry is required, and a read-only or symlinked global skills directory is fine.

The temporary files stay available while the backend runs and are cleaned up when the driver returns. Wi does not modify your global skills or settings. Remove old manually configured copies yourself to avoid duplicate `wi` names; Pi's normal collision rules still apply.

Rebuilding Wi packages the current skill source. Installing the executable alone does not change an already running agent; the next runtime startup exports the bundled version. Normal backend skill loading still applies, and resume never sends an extra prompt to force it. See the [driver protocol](../../reference/agent-driver-protocol/#bundled-skills) for optional driver support.

## TUI mode

TUI mode is the default:

```bash
wi start item-slug
wi switch item-slug
```

A driver advertising native TUI support runs its backend inside a wi-owned tmux session. You can leave and re-enter the session while the conversation continues to belong to the item. The standard Pi driver runs native interactive Pi this way.

## Headless mode

Headless mode does not use tmux:

```bash
wi start --agent-mode headless item-slug
```

A driver must advertise headless support to use this mode. Stop the current runtime before switching modes:

```bash
wi agent runtime stop item-slug
wi agent runtime status item-slug
wi switch item-slug
```

Wait until status reports that the runtime is offline before starting the other mode.

## Runtime observation and control

```bash
wi agent runtime status item-slug
wi agent status item-slug
wi agent monitor --limit 50 item-slug
wi agent control abort --item item-slug
wi agent runtime stop item-slug
```

Agent status reflects whether the current runtime appears busy, idle, or in need of inspection. It does not depend on how the prompt was submitted.

## Send input

Send a message to the active runtime:

```bash
wi agent control send --item item-slug "Run the focused tests and explain failures."
```

While the agent is busy, the default steering message runs after the current assistant turn and tool calls. Use `--follow-up` when the message must wait for the current run to settle. Use `--file` or `--stdin` for longer input.

Control delivery confirms only that the active driver accepted the message. Use `wi agent monitor`, or enter the TUI, to inspect the result. Drivers advertise which controls they support and must reject unsupported operations rather than emulate TUI keystrokes.

## One runtime at a time

TUI and headless runtimes cannot own the same conversation simultaneously. Stop the active runtime before starting another mode.

Force cleanup stops a process only after `wi` verifies that it still belongs to the item. A stale runtime record will not cause an unrelated process to be stopped.
