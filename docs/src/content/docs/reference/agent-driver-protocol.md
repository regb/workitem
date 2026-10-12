---
title: Agent driver protocol
description: Integrate coding-agent runtimes with wi.
---

An agent definition is the user-selected coding agent. A driver is the executable adapter that starts a backend, resumes its native conversation, exposes controls, and reports compact semantic activity.

## Bootstrap

`wi` starts a driver with `WI_AGENT_DRIVER_BOOTSTRAP_SOCKET` set to a user-only Unix socket. The driver connects and sends a hello record:

```json
{"protocol_version":1,"driver":"pi","driver_version":"..."}
```

After checking the driver name and protocol version, `wi` sends a request envelope. Its `operation` selects a runtime launch or a conversation operation. The driver sends one response envelope.

The normative Go wire types are in `github.com/regb/workitem/agentdriver/protocol`. Other languages can implement the newline-delimited JSON exchange directly. Go driver implementations can use `github.com/regb/workitem/agentdriver/drivertest` for conversation-operation checks and live launch, capability, control, event, and shutdown checks.

## Runtime launch

A `launch` request contains the agent name, merged driver configuration, runtime identity and mode, checkout directory, conversation locator, runtime control socket, and runtime event socket. The driver replies with capabilities and its resolved backend executable before starting the backend. Standard input, output, and error remain attached for TUI mode.

The runtime mode is independent of the process protocol:

- `tui` asks the driver to run its interactive backend. `wi` composes that runtime with tmux.
- `headless` asks the same driver process to run its backend headlessly. The driver translates control commands into backend-specific requests and translates backend events into wi runtime events. Headless mode does not create or inspect tmux resources.

Both modes use the same conversation identity, runtime ownership checks, control socket, and event protocol. A headless driver may wrap any non-interactive backend interface; the backend itself does not need to implement RPC. The driver advertises `prompt`, `steering`, `follow_up`, `cancel`, and `streaming` separately and rejects commands it cannot translate.

## Conversation operations

Drivers also handle short-lived process requests:

- `conversation.create` returns the driver's locator and native format for a new conversation ID.
- `conversation.cwd` reads the checkout recorded by a native conversation.
- `conversation.fork` copies or rewrites a conversation for a reassigned checkout.
- `conversation.observe` returns compact status without returning prompts, responses, or tool arguments.
- `conversation.decode` converts batches of native records into compact facts for daemon indexing. Native lines use encoded byte payloads so malformed or partially written records can be reported instead of breaking protocol encoding.

`wi` starts the configured driver command for these operations. Driver-specific parsers and filesystem rules do not run in generic application or coordinator packages.

## Runtime controls and events

The driver owns the per-runtime control socket named by `runtime.control_socket_path`. Each connection carries one newline-terminated `ControlCommand`. The driver replies with `{"accepted":true}` or `{"accepted":false,"error":"..."}`. CLI controls travel through the daemon, which verifies current runtime ownership before forwarding prompt, steer, follow-up, abort, or shutdown commands. A driver must reject unsupported controls rather than simulate TUI keystrokes.

The `wi agent exec` host owns `runtime.event_socket_path`. A driver opens one connection per compact `RuntimeEvent` and receives the same accepted/error response shape. The host checks protocol, runtime, and work-item identity before relaying the event through the private coordinator API. Drivers never receive the coordinator socket or its protocol metadata. Prompts, responses, deltas, tool arguments, paths, and unrestricted errors do not belong in retained daemon events.

## Ownership boundaries

`wi` owns lifecycle policy, checkout materialization, runtime ownership, tmux composition, event relay, and endpoint authorization. The driver owns backend command construction, backend configuration validation, native conversation handling, capability negotiation, and translation between backend and wi events.
