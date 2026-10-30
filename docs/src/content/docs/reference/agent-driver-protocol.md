---
title: Agent driver protocol
description: Integrate coding-agent runtimes with wi.
---

An agent definition is the user-selected coding agent. A driver is the executable adapter that starts a backend, resumes its native conversation, exposes controls, and reports compact semantic activity.

## Bootstrap

`wi` starts a driver with `WI_AGENT_DRIVER_BOOTSTRAP_SOCKET` set to a user-only Unix socket. The driver connects and sends a hello record:

```json
{"protocol_version":3,"driver":"pi","driver_version":"..."}
```

The process protocol is version 3; version 3 named the reasoning setting `reasoning` for every driver. Runtime control and event sockets keep their independent version 1, so existing runtimes do not need restarting when conversation transport changes. External drivers must use the version-3 bootstrap and opaque record format.

After checking the driver name and protocol version, `wi` sends a request envelope. Its `operation` selects a runtime launch or conversation operation. The driver sends one response envelope.

The normative Go wire types are in `github.com/regb/workitem/agentdriver/protocol`. Other languages can implement the newline-delimited JSON exchange directly. Go driver implementations can use `github.com/regb/workitem/agentdriver/drivertest` for conversation-operation checks and live launch, capability, control, event, and shutdown checks.

## Runtime launch

A `launch` request contains the agent name, merged driver configuration, runtime identity and mode, checkout directory, conversation locator, runtime control socket, and runtime event socket. The driver replies with capabilities and its resolved backend executable before starting the backend. Standard input, output, and error remain attached for TUI mode.

The optional `launch.project_trusted` boolean authorizes project-local agent configuration and resources for this launch. An absent field means no host approval. The host sets it only after verifying the runtime directory is the current claimed managed slot of a registered repository and its Git common directory matches the owned store. Drivers may translate this authorization to their native project-resource approval mechanism. The bundled Pi driver passes `--approve` in both modes. This does not authorize direnv shell code, modify Pi's saved trust decisions, or grant blanket trust to a parent directory. Short-lived title and configuration operations do not receive this runtime authorization.

The additive `launch.append_system_prompt` string carries host-configured text to append to the backend\'s normal system prompt. It is not a replacement prompt or an initial conversation message. The bundled Pi and Claude drivers honor it in both modes, for creation and resume. Empty text means no host addition; drivers must not substitute their own Wi boilerplate. External drivers need to implement this field to apply the setting. It is launch configuration, not retained runtime-event content.

The runtime mode is independent of the process protocol:

- `tui` asks the driver to run its interactive backend. `wi` composes that runtime with tmux.
- `headless` asks the same driver process to run its backend headlessly. The driver translates control commands into backend-specific requests and translates backend events into wi runtime events. Headless mode does not create or inspect tmux resources.

Both modes use the same conversation identity, runtime ownership checks, control socket, and event protocol. Optional `live_content`, `idle_guard`, and `image_input` capabilities describe content snapshots, idle-only controls, and image-bearing prompts. An absent capability means unsupported; do not restart an existing owner implicitly to obtain it. A headless driver may wrap any non-interactive backend interface; the backend itself does not need to implement RPC. The driver advertises `prompt`, `steering`, `follow_up`, `cancel`, and `streaming` separately and rejects commands it cannot translate.

Pi's headless transport queues up to 32 control requests while waiting for native readiness. Requests beyond that limit are rejected. Requests still waiting when the runtime exits or is canceled are rejected rather than silently dropped.

## Driver configuration

Wi merges driver defaults with agent-profile overrides and sends the result as opaque JSON. Each driver owns its required fields, defaults, and value validation. Reject invalid configuration through the request's error response; do not claim launch readiness when required settings are missing.

`config.validate` is a stateless process request carrying the profile name and merged `config`, without a conversation or runtime. Success must include `config_validation: {"valid": true}` in the response; rejection uses `error`. Drivers own mandatory fields and must implement this operation for new startup requests. There is no generic configuration schema.

Wi checks configuration before `new --start` creates an item, before `start`/`resume` changes lifecycle when a new owner is needed, and before runtime creation allocates resources. Plain backlog capture and workspace-only operations remain driver-independent. Access to an existing owner does not revalidate future launch settings or restart it.

Validation must not launch a coding backend, create native content, allocate resources, or make a model request. Clients fail closed on rejection, missing acknowledgement, or unsupported operations, with a bounded request deadline. Validation is not a guarantee of later executable, authentication, network, or model availability; drivers must also validate launch requests. Error messages should identify missing fields without revealing secret values.

### Configuration discovery

`config.discover` is an optional, read-only process request without a runtime, conversation, or existing config. It returns `config_discovery`:

```json
{"profiles":[{"name":"pi","label":"Pi","config":{"executable":"/home/me/bin/pi"}}],"warnings":[]}
```

Each profile has a unique lowercase name using letters, digits, and hyphens, an optional label, and an opaque flat configuration object. Values must be strings, booleans, integers, or string arrays supported by Wi's config parser. `default` is reserved for the user's selection. Return an empty profiles list when no suitable installation exists. Rejection or an omitted discovery response means the driver does not support discovery.

Discovery must not launch a coding backend, authenticate, make model requests, create native content, or return credentials. Bundled drivers locate their executable on `PATH` and return an absolute launcher path, preserving installation symlinks. `wi init` validates each discovered profile through `config.validate`, asks the user to select a named default, writes the default_agent pointer, and publishes the config without overwriting an existing file. The operation and response field are additive within protocol version 3.

### Runtime modes

`runtime.modes` is a stateless request carrying the profile name and merged `config`. It reports, without launching anything, what each runtime mode of that profile offers:

```json
{
  "runtime_modes": {
    "modes": {
      "tui":      {"control": "emulated",   "terminal": true, "image_input": true},
      "headless": {"control": "structured", "image_input": true}
    },
    "settings": {"model": {"on_resume": true}, "reasoning": {"on_resume": true}}
  }
}
```

`control` is `structured` when the driver delivers and acknowledges commands, `emulated` when it can only type into a terminal UI, or absent when the mode takes no input from Wi. `terminal` marks a native TUI a person can attach to; `idle_guard` an atomic idle-only prompt; `image_input` image attachments. `settings` lists profile settings that may differ between successive runtimes of one conversation, when a runtime resumes it (`on_resume`) and in which modes while it runs (`live`).

**Resume contract:** every listed mode must be able to resume any conversation of the driver with the same profile settings. Wi relies on this to replace a runtime with one in another mode on the same conversation, for example when a browser needs structured control and the running TUI only accepts keystrokes. When no mode offers the channel a client needs, Wi refuses control with an explanation instead of restarting the runtime.

The request may check the installed backend version, but must not start an agent, authenticate, or create native content. The answer is an expectation: a runtime's `Ready` capabilities remain authoritative. Wi caches it per profile for the life of the process. Drivers that do not implement the operation are treated as offering structured control in headless mode and a terminal in TUI mode. The operation and response field are additive within protocol version 3.

### Model catalog

`config.models` is a stateless request carrying the profile name and merged `config`. It lists the models the profile can use and the reasoning levels the driver accepts:

```json
{"model_catalog": {"reasoning_levels": ["low", "medium", "high", "xhigh", "max"], "default": "claude-opus-5-5",
  "models": [{"value": "opus", "resolved": "claude-opus-5-5", "label": "Opus 5.5", "description": "…", "reasoning": true, "levels": ["low", "high"]}]}}
```

`value` is what the `model` setting takes. `resolved`, when `value` is an alias, is the model it currently names. `default` and `default_reasoning`, when the driver knows them, are the model and reasoning level the agent uses when none is set. A model's `levels` narrows the reasoning levels; `reasoning: false` means it has none. The driver may run its backend's own catalog command but must not start an agent session or make model requests: bundled Pi parses `pi --list-models`, and bundled Claude reads the `initialize` response of its headless control protocol in an empty directory without saving a session. Wi caches the answer for ten minutes.

## Bundled skills

The host embeds `skills/wi/` in the Wi executable at build time and includes trusted resources in the optional `launch.skills` array. Each skill has a `name` and `files`; each file has a portable relative `path`, base64-encoded `data`, and optional `executable` flag. No backend destination paths are supplied by the host. Payloads are limited to 16 skills, 64 files per skill, and 4 MiB of content in total. Absolute, hidden, escaping, duplicate, and file/directory-colliding paths are rejected.

A supporting driver validates and exports the resources before backend startup in both TUI and headless mode, then passes their absolute paths through its backend's explicit extra-skill mechanism. It advertises `capabilities.bundled_skills: true`. Export failures fail startup; the host warns if a driver lacks support. Optional launch fields and capabilities are additive within process protocol version 3. Skill content is not retained in daemon events.

The Pi driver creates a private temporary directory per runtime, writes each skill tree with private permissions, and passes a repeated `--skill <absolute-directory>` argument to Pi. The files remain available for the backend's lifetime and are removed when the driver returns, including handled startup failures and shutdown. Abrupt process termination can leave temporary directories for system cleanup. There is no global installation, ownership marker, update operation, or modification of Pi settings. Read-only and symlinked Pi skill directories do not block delivery.

These are rebuildable runtime resources, not work-item state. Descriptions, native conversations, prompts, credentials, and user-managed skills are untouched. Pi still advertises skill names/descriptions and loads instructions when needed. Normal skill discovery remains enabled; existing duplicate names follow Pi's collision rules.

## Conversation operations

Drivers also handle these short-lived conversation requests:

- `conversation.capabilities` returns optional `history`, `history_paging`, `subagents`, and `title` support without starting a backend or creating a conversation.
- `conversation.history` returns a bounded chronological branch view with stable native entry IDs, roles, text, timestamps, leaf identity, and a truncation flag. The host resolves the canonical locator; access clients must not choose native paths. An optional `leaf_id` selects an existing branch. Drivers advertising `history_paging` accept an exclusive native `before_id` cursor on that branch. Responses include `has_more` and the next `before_id`, empty at the beginning. Pin `leaf_id` when requesting older pages. Folded tool results expose `native_ids` aliases on their call message. `native_entry_ids` lists the page's raw entry IDs chronologically so clients can distinguish messages before and after a folded result. A `user` message may carry `tools`: shell commands the operator ran in the agent's own prompt, such as Pi's `!ls`. A `summary` message carries `summary.kind`: `compaction` for a summary that replaced the earlier conversation in the agent's context, with `tokens_before` when known, or `branch` for a summary of a branch the conversation left. Claude records a compaction as a boundary without text followed by the summary; clients show the two as one.
- Drivers advertising `subagents` link a tool call that delegated to a subagent with its `subagent`: `id`, `tool_id`, `type`, `description`, and `status` (`running`, `completed`, or `failed`). A `conversation.history` request with that `subagent` ID reads the subagent's own conversation as a history page, pageable with `before_id`; IDs are letters, digits, `-`, and `_`, at most 64. A message with role `subagent` reports a subagent's result to the agent: its `subagent` names it, and its text is the result. Other drivers must refuse a `subagent` request rather than return the main conversation.
- `conversation.title` suggests a title from a bounded `prompt`. It never acquires, edits, or creates a primary conversation. Persisting a title is a separate daemon command and does not rename the slug or implementation branch.
- `conversation.create` returns the driver's locator and native format for a new conversation ID.
- `conversation.cwd` reads the checkout recorded by a native conversation.
- `conversation.fork` copies or rewrites a conversation for a reassigned checkout.
- `conversation.observe` returns compact status without returning prompts, responses, or tool arguments.
- `conversation.decode` converts batches of native records into compact facts for daemon indexing. `lines` is an array of base64-encoded byte strings, not inline JSON objects. Malformed native records must reach the decoder unchanged. `decoded` reports success for each input line in order.

`wi` starts the configured driver command for these operations. Driver-specific parsers and filesystem rules do not run in generic application or coordinator packages.

The Pi history reader snapshots the file size before reading, ignores an incomplete final append, and follows parent IDs on one branch. Malformed records, missing parents, cycles, and unsupported legacy records fail explicitly. It defaults to the latest page of up to 200 native entries, limits individual text/tool fields to 16 KiB, total text to 1 MiB, and image data to 12 MiB. Thinking content is omitted. There is no conversation-size limit; older entries are available through paging. Truncation preserves UTF-8. Oversized fields have a truncation flag. The reader indexes native IDs and offsets rather than retaining all transcript bodies, but each operation still scans the file. A live TUI snapshot can supply its in-memory leaf; `"-"` means an empty branch.

Pi title requests use a temporary working directory and non-persisted print mode with tools, extensions, skills, project instructions, and approval disabled. They retain the configured authentication/profile context but remove runtime identity and control variables. Cancellation terminates the short-lived driver process group, not a primary runtime.

## Runtime controls and events

The driver owns the per-runtime control socket named by `runtime.control_socket_path`. Each connection carries one newline-terminated `ControlCommand`. The driver replies with `{"accepted":true}` or `{"accepted":false,"error":"..."}`. Host-side application control resolves daemon-owned state, verifies current runtime ownership and capabilities, then submits prompt, steer, follow-up, abort, or shutdown commands to that owner. Prompt-like commands may carry generic base64 `images` with MIME types only when the driver advertises `image_input`; each driver translates that payload to its native API. A driver must reject unsupported controls. It may translate supported commands through backend APIs or its own verified TUI input transport. Terminal input belongs inside the driver, not application policy. For the bundled Claude driver, native acceptance is confirmed through TUI lifecycle hooks or headless replay/queue receipts after input delivery; an initial acknowledgement can instead mean a command entered its bounded queue. It does not advertise atomic idle control. Unknown delivery outcomes must not trigger automatic retyping.

`set` changes one profile setting of a running agent: it carries `setting: {"name": "model", "value": "sonnet"}` and no message. Names are `model` and `reasoning`; values are one line of at most 256 bytes. A driver lists the settings a runtime can change live in `Ready.capabilities.settings` and rejects others. It confirms or rejects the change through `command.accepted` or `command.rejected` with the command ID; Wi waits briefly for that verdict. Bundled Pi changes `model` (`provider/model`) and `reasoning` in both modes, through RPC `set_model`/`set_thinking_level` headless and its extension in the TUI. Bundled Claude changes `model` and `reasoning` in headless mode through its `set_model` and `apply_flag_settings` control requests; its TUI does not change them live.

Control messages are limited to 64 KiB of UTF-8 text and four images totaling at most 8 MiB decoded. Images require nonempty canonical base64 and JPEG, PNG, GIF, or WebP MIME types. Control sockets accept up to 12 MiB of serialized JSON to accommodate base64 and escaping; event sockets retain their 1 MiB limit. The host and bundled drivers validate image content before handing it off. Drivers can restrict the supported formats further; the Claude TUI driver currently accepts PNG and JPEG.

Claude headless uses a persistent native streaming-JSON process. Its initialized CLI must be version 2.1.299 or later. Native input UUIDs correlate receipts, merged results, and cancellation with Wi command/request IDs. Pending work is reserved immediately, up to 32 inputs; follow-ups wait for results, while steering uses native `next` priority. Its deduplication table holds at most 1024 commands. Input writes have three-second deadlines; missing receipts after 15 seconds report uncertainty without resending. Frames are limited to 16 MiB. Invalid or truncated frames fail the runtime with a fixed reason code, never native text. Native interrupt receipts confirm the interrupt request, not background-task termination. Shutdown is bounded by SIGTERM then SIGKILL after three seconds. Headless rejects interactive approval/input callbacks and all atomic idle-only operations.

`idle_guard` describes support for guarded operations, not the owner's current activity or readiness.

`require_idle: true` is an additive precondition, usable only when `idle_guard` is advertised. It includes work already submitted but not yet reflected in activity events. Refusal must not queue, steer, abort, or shut down the runtime. Duplicate accepted command IDs must not submit work twice. An acknowledgement is transport handoff, not completion or native prompt acceptance; command events provide later confirmation.

Pi headless mode reserves submitted work immediately and uses native idle-only prompt rejection instead of steering. Guarded shutdown is checked in Pi's own event loop by a bundled extension and acknowledged only after that check succeeds. The extension returns a separate private RPC result because Pi reports slash commands as handled even if their handler throws; that result is not native conversation content. The headless extension does not create sockets or duplicate the host's event relay. TUI control checks Pi's idle/pending state and reserves bridge-submitted work until settlement. Drivers may conservatively refuse when settlement cannot be established. Explicit abort and shutdown remain available to verified archived or stale-claim owners; archived items cannot receive new messages. Existing owners without the new capability retain their previous stop behavior.

The `wi agent exec` host owns `runtime.event_socket_path`. A driver opens one connection per compact `RuntimeEvent` and receives the same accepted/error response shape. The host checks protocol, runtime, and work-item identity before relaying the event through the private coordinator API. Drivers never receive the coordinator socket or its protocol metadata. Prompts, responses, deltas, tool arguments, paths, and unrestricted errors do not belong in retained daemon events.

Event replies may add `retryable: true` when the host cannot reach the coordinator or persist the event. Producers must retry the same event ID in order, without dropping later settlement events. Socket failures also require retry. A rejected reply with `retryable` absent or false is definitive, including invalid events and lost runtime or workspace ownership. These event-delivery rules do not authorize resending control commands.

Optional `runtime.status` events carry `readiness` and `reason_code`. Readiness is a closed vocabulary: `starting`, `ready`, `busy`, `awaiting_confirmation`, `waiting_input`, `blocked`, or `stopping`. It describes observed input access separately from process state. The coordinator updates only the current owner's cache, rejects stale observations, and retains compact status history. Command events can carry `delivery_state`: `queued`, `submitted`, `confirmed`, `not_sent`, `uncertain`, or `cancelled`. `reason_code` is also a closed safe vocabulary defined in `agentdriver/protocol/runtime_status.go`; unknown values are rejected rather than retained as native text. `queue.changed` is retained for delivery tracking. Fields are additive and omitted on older drivers.

A `settings.changed` event carries `settings`, the `model` and `reasoning` the agent runs with, as soon as the driver learns them and again on every change, whether wi or the agent's own interface made it. A key left out is unchanged or unknown; `model` names a concrete model rather than an alias where the backend says which. Claude records its reasoning effort only with each reply, so its driver reports the level after a turn unless one is configured. The coordinator keeps the latest values on the runtime record and every change in the event history; they are not agent activity. The first report of a runtime describes its launch: wi makes a reported value the item's own only when neither the item nor its profile chose one, so later runtimes keep the model the conversation started with. A later report is a change, made through wi or in the agent's own interface, and becomes the item's setting wherever it differs, so the next runtime continues with it.

Drivers may add `agent_state: active|idle|failed` and `leaf_id` to `runtime.status` when they can observe native run and branch state directly. The leaf is an opaque entry token of ASCII letters, digits, hyphens, or underscores, at most 200 bytes; `-` means an empty branch. Active observations require `readiness: busy`; idle or failed observations require `readiness: ready`. These snapshots can reconcile a selected branch after tree navigation or reload, but do not record a new completion or change read markers. Ordinary readiness events without these fields still cannot erase a previous failure. Ownership, workspace generation, event deduplication, and timestamp checks apply as usual.

Pi uses its extension context and selected session projection for these observations. TUI sends compact events through the host relay; headless sends private JSONL status records through the existing RPC transport. Startup, tree navigation, settlement, and compaction publish snapshots. A native-state check reconciles changed leaves or run state between notifications; elapsed time never proves completion. Reload closes the old sockets and stops its observer before starting a new extension runtime. Existing Pi processes keep their old extension until explicitly restarted with the updated driver.

`agent.started` begins session-level work. Inner `turn.ended`, `message.completed`, and failed tool results do not close that work: retries, compaction, and queued continuations can follow. `agent.settled` is the final boundary. Send the additive compact `outcome: "completed"` or `outcome: "failed"` on settlement, and set the existing `failed` boolean for assistant errors. A turn the operator aborted or interrupted settles as completed: it is a normal end, the reply is unread like any other, and the next message continues as usual. History marks the stopped reply with `aborted: true`. An absent outcome from older drivers is not proof of success; Wi falls back to native history instead of masking a terminal failure. Do not report an intermediate model failure as `runtime.failed`. Runtime failure means the process or control path cannot continue. The bundled Pi TUI uses `agent_start`, `agent_before_settle`, and `agent_settled` hooks. Headless RPC waits for the same final settlement and carries the final assistant outcome. Only activity from the current runtime owner and workspace claim can drive the live observation. An active loop whose advertised control endpoint is missing or unreachable reports a problem, even if its host PID is alive. Elapsed time alone never settles work, and losing the bridge does not prove completion.

## Live content snapshots and streams

Content uses independent protocol version 1. It is optional, read-only, and ephemeral. `ContentSocketPath(controlPath)` derives a runtime-specific hashed filename beside the control socket. Drivers must not unlink a predecessor's endpoint. A crash can leave an old socket without blocking the next owner.

Each connection sends a newline-delimited `ContentRequest` with content protocol version, runtime ID, and work-item ID, and receives one `Content` snapshot. Both request and response identities must match the verified owner. The host rechecks ownership after reading. Sockets are user-only, reads have bounded deadlines, and slow consumers must not block turns.

A snapshot contains sequence, current assistant text, activity, optional leaf identity, and truncation information. Text is capped at 256 KiB, preserving UTF-8. `tools` lists the current message's tool calls in order, each with its ID, name, running/completed/failed status, and input/output previews limited to 64 KiB each; `tool` is the latest of them, kept for older readers. `thought` carries reasoning the agent shares, keeping the newest 64 KiB. A finished message keeps its text until the next message starts. `saved_as` lists the native history entries that hold the current text once the driver knows them; readers stop showing the text when history contains one of them. Final history remains authoritative. Content and unrestricted errors must never enter retained daemon events.

A request with `"stream": true` receives a `snapshot` event followed, on the same connection, by every later content event, one JSON object per line. Each event has a sequence one greater than the previous; a gap means events were lost, and the reader subscribes again. A reader that falls behind is disconnected rather than blocking the driver. Event types are `message.started`, `message.delta` (appends text), `message.replaced` (sets the whole text), `message.completed`, `message.saved` (native entry IDs for the current text), `thought.delta`, `prompt.saved` (links a prompt, by the request ID its sender gave the control command, to its native entries; the snapshot keeps the last eight in `prompts`), `tool.started`, `tool.updated`, `tool.completed` (each carrying the call's whole state), `tool.cleared`, `activity` (activity and leaf), and `turn.settled`. `ApplyContent` in `agentdriver/protocol` is the definition of how an event changes a snapshot; the shared Go content server computes its own snapshots with it, and Go and browser readers are tested against the same fixture. A server that predates streams answers with a bare snapshot and closes, and readers fall back to polling. The bundled drivers stream in both modes. The design and its mapping to ACP v2 are in `docs/design/live-content-stream.md`.

Claude TUI streaming uses `MessageDisplay` line batches through an additional private runtime-local endpoint. These are not compact runtime events or per-token deltas. `streaming` indicates incremental content availability, not a particular chunk size. Snapshots retain native history leaf IDs rather than native display-message IDs. Missing batches and upstream truncation set snapshot truncation; native history remains authoritative for complete messages.

Snapshots and streams do not provide lossless replay or a history watermark. Consumers must refresh history after completion, reconnect, or branch/owner changes. Sequence numbers are scoped to one runtime. Opening a content or history view never starts a runtime or changes its mode.

## Ownership boundaries

`wi` owns lifecycle policy, checkout materialization, runtime ownership, tmux composition, event relay, and endpoint authorization. The driver owns backend command construction, backend configuration validation, native conversation handling, capability negotiation, and translation between backend and wi events.
