---
title: Agents and conversations
description: Separate the durable primary conversation from its replaceable runtime.
---

Each work item has one durable primary agent conversation. Only one Pi process may use it at a time.

The conversation is part of the item's history. The runtime is a replaceable process that opens and controls it. Stopping a runtime does not discard the conversation.

## TUI mode

TUI mode is the default:

```bash
wi start item-slug
wi switch item-slug
```

It runs native interactive Pi inside a wi-owned tmux session. You can leave and re-enter the session while the conversation continues to belong to the item.

## RPC mode

RPC mode is headless and does not use tmux:

```bash
wi start --agent-mode rpc item-slug
```

It runs Pi through its headless JSONL interface. This is useful for automation, background delegation, and environments without tmux.

Pi cannot attach its native TUI to a live RPC process. Stop the current runtime before switching modes:

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

Control delivery only confirms that Pi accepted the message. Use `wi agent monitor`, or enter the TUI, to inspect the result.

## One runtime at a time

TUI and RPC cannot use the same conversation at the same time. Stop the active runtime before starting the conversation in another mode.

Force cleanup stops a process only after `wi` verifies that it still belongs to the item. A stale runtime record will not cause an unrelated process to be stopped.
