---
title: Configuration
description: Configure capacity, labels, attention ranking, direnv trust, and display.
---

User configuration lives at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/wi/config.toml
```

Run `wi init` once on a new machine. Wi asks each bundled driver to discover installed agents on `PATH`, lists the detected profiles, and lets you select the default. It writes explicit profiles and absolute executable paths to this file without launching an agent or starting the coordinator. Discovery does not check authentication or make model requests.

For noninteractive setup, use `wi init --default pi` or `wi init --default claude`. Only detected profiles can be selected. If no agents are installed, setup reports an error without writing a config. Existing files are never overwritten; edit them directly.

A missing `default_agent`, or a reference to an undefined profile, is a configuration error. Agent profiles are not implicit defaults. A repository may also provide `.config/wi.toml` for supported project settings, but it cannot define user agent profiles.

## Inspect configuration

```bash
wi config agents
wi config get default_agent
wi config get deep_work.max_active
wi config get list.labels --json
wi config get agent.startup_message
wi config get agent.append_system_prompt
wi config drivers
wi config extensions
wi config get extensions.direnv.trust --origins
wi repo settings show workitem --json
wi config show --json
```

`wi config` defaults to `show`. These queries read global user configuration with built-in defaults, without contacting the coordinator or running drivers, extensions, or direnv. Repository policy belongs to daemon-owned entity settings, inspected with `wi repo settings show NAME`, not per-repository config files. Config queries do not evaluate checked-in project settings, environment label rules, or profile/driver merges. Invalid configuration reports an error.

`wi config extensions` displays registered extension options, defaults, types, and repository-scope permissions as JSON. `--origins` wraps a query result in `value` and `origins`, identifying global extension values supplied by a default or user configuration. Extension settings marked sensitive, and values belonging to unregistered extensions, are redacted unless `--show-sensitive` is supplied.

`agents` lists configured profiles sorted by name, their drivers, optional labels, and the default marker. Its JSON object contains `default_agent` and an `agents` array with `name`, `driver`, optional `label`, and `default`. A configured profile does not imply that its executable is installed or authenticated.

`get KEY` accepts dot-separated configuration keys or sections. Text scalars print directly; sections print sorted dotted settings. `--json` emits the selected JSON value, without a wrapper. An unknown key exits with code 3; invalid arguments exit with code 2.

Driver-specific profile settings, driver defaults, and custom driver commands may contain secrets. Wi hides their values by default, retaining the setting names. Use `--show-sensitive` only when you intend to reveal them, including in JSON output:

```bash
wi config get agents.pi.config --show-sensitive --json
wi config get drivers.pi.defaults --show-sensitive --json
```

`wi info config-file` prints the configuration path. `wi init` remains the first-time setup command; configuration queries never rewrite the file.

## Example

```toml
default_agent = "pi"

[deep_work]
max_active = 2

[item.defaults]
labels = ["team-one"]

[list]
repository_folders = 2
labels = ["+team-one", "-blocked"]

[attention]
priority = "recent-request"

[extensions.direnv]
load = true
trust = "prompt"

[agent]
startup_message = "Read this work item's description with `wi show`. Work on the task it describes. Run relevant checks and report the outcome."

[agent_status.markers]
busy = "agent"
idle = "review"
problem = "problem"

[drivers.pi.defaults]
executable = "/home/me/bin/pi"

[agents.pi]
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

## Direnv trust

Direnv is a bundled, in-process extension. Its registered options are `extensions.direnv.load` and `extensions.direnv.trust`.

- `load = true`, the default, loads approved project environments before TUI or headless runtime startup.
- `load = false` skips evaluation, but still removes inherited direnv state and revokes stale approval when reusing a slot.
- `trust = "prompt"`, the default, asks before approving an untrusted worktree `.envrc` during interactive runtime startup. Without approval, startup proceeds without that environment and reports a warning.
- `trust = "always"` automatically approves the current worktree `.envrc`, including changed contents. This is an explicit permission to execute shell code.
- `trust = "never"` does not prompt or grant new approval. Existing direnv approvals remain usable; disable `load` to prevent evaluation altogether.

Set repository policy through the daemon, using a name or ID:

```bash
wi repo settings show biskoui
wi repo settings set biskoui extensions.direnv.trust always
wi repo settings set biskoui extensions.direnv.load false
wi repo settings unset biskoui extensions.direnv.trust
```

Repository settings are stored in the private `wi.db`, keyed internally by stable repository identity. Rename and archive preserve them; reusing an old name does not transfer its policy. They are not configuration-file tables or files generated by Wi.

Values resolve in order: registered extension defaults, global user defaults, then repository overrides. `unset` removes an override and restores inheritance. Explicit `false` and empty values override inherited settings; arrays replace rather than append. Only registered options permitted at repository scope can be set. Boolean values use `true`/`false`; integers use decimal text; string arrays use JSON such as `'["one","two"]'`.

`show --json` includes the repository, settings revision, stored `overrides`, resolved `effective` values, `origins`, and warnings. Override and effective namespace keys use `extensions.NAME`. Values marked sensitive, and values for unavailable namespaces, are hidden unless `--show-sensitive` is supplied. Unavailable settings remain stored but inactive. Invalid known settings block their application and are reported so an operator can remove them.

The daemon checks schema permissions and a settings revision on each mutation, deduplicates retries, and records a compact change event without retaining values in event history. These settings endpoints are operator-only. Runtime hooks receive a resolved snapshot for the operation, not a database client. Existing agent environments are not changed in place.

For commands that consume project-scoped CLI settings, the extension reads only `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` from an already allowed `.envrc`. Explicit caller values win. It does not approve files during these reads. Slot reuse revokes previous approval before publishing the new claim; approvals are not shared by file hash.

Checked-in `.config/wi.toml` cannot configure extensions, enable executables, or grant trust. The former `[direnv] auto_trust_repositories` path allowlist and `[repositories.ID...]` user-config tables are rejected with actionable errors. Copy any intended overrides aside, remove those tables, then apply them explicitly using `wi repo settings set`. Wi does not infer or migrate approvals from files or paths.

## Initial agent message

`agent.startup_message` is the global message sent by `wi new --start`. It must be a non-empty quoted string. Escape newlines as `\n` within the string. `--prompt TEXT` overrides this message for that creation and implies `--start`. It conflicts with an explicit `--start=false`. Neither message changes the durable description supplied through `--desc-file`.

`wi resume` never sends a startup message. `wi start` for an existing item opens its runtime without submitting a message. Use `wi agent control send` to explicitly start a turn in an existing conversation.

## Appended system instructions

`agent.append_system_prompt` adds text to the backend's normal system prompt for every newly launched runtime, including resumed conversations. The bundled Pi and Claude drivers apply it in both TUI and headless modes. It is separate from `startup_message` and does not submit a user message.

The default identifies the conversation as a Wi item and directs the agent to read `wi show`. Inspect it with `wi config get agent.append_system_prompt`. Set a quoted string to customize it; escape newlines as `\n`. An empty or whitespace-only string disables Wi's addition. Values are literal text, not file paths. Changes apply to future launches, not existing runtimes.

```toml
[agent]
append_system_prompt = "Read the task with `wi show`. Ask before changing other work items."
```

## Coding agents and drivers

Every new item records a named agent profile. When `wi new` omits `--agent`, Wi resolves the top-level `default_agent` pointer and stores that profile name on the item. Changing `default_agent` affects future creation, not items already stored with named profiles. Editing the selected profile changes its settings without duplicating configuration. `wi init` creates named profiles for detected agents and writes the pointer; it does not create an `agents.default` profile. Missing or dangling pointers are configuration errors.

Older items recorded the special name `default`; those records still resolve through the configured pointer. Their records and native conversations are not rewritten. Keep the pointer on their original agent when resuming them. Wi bundles the Pi and Claude adapters, not implicit profiles.

`[agents.<name>]` defines a flat selectable agent profile and selects a registered driver. An optional `label` provides display text; clients should fall back to the profile name when it is blank. The nested `config` belongs to the driver, which can require fields and reject missing or invalid values. Wi passes this table as opaque JSON and checks it through the driver's stateless `config.validate` operation before starting a new owner. There is no generic required-field schema. Invalid configuration fails before startup creates resources; `new --start` also validates before creating the item. Plain backlog capture does not require a driver process, and existing owners are not reconfigured.

Pi accepts optional `executable`, `extensions`, `model`, and `thinking`. The executable defaults to `pi`; omitted model/thinking settings leave the choice to Pi. Explicit thinking values must be `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. `[drivers.<name>.defaults]` provides settings shared by every agent using that driver; agent configuration overrides those defaults.

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

Items retain the selected profile name. Runtime startup resolves the profile and passes its configuration through the selected driver.

Select a profile with `wi new --agent NAME`.

External driver programs use wi's versioned bootstrap protocol. The bundled drivers run as separate `wi internal-driver pi` and `wi internal-driver claude` processes from the installed `wi` binary, so the adapters are always present and have the same build version. The underlying `pi` or `claude` executable must still be installed. A configured `drivers.<name>.command` selects a trusted external driver executable; repository configuration cannot register drivers or agent definitions.

### Claude Code

Claude accepts optional `executable`, `model`, and `effort`. The executable defaults to `claude`; omitted model and effort settings leave the choice to Claude. Effort accepts `low`, `medium`, `high`, `xhigh`, or `max`.

The bundled Claude driver always passes `--dangerously-skip-permissions`, on both creation and resume, to match Pi's execution without per-tool approvals. Permission mode is not configurable; `permission_mode` is rejected as an unknown field. Claude executes with the host account's access. A managed worktree is not a sandbox.

```toml
[agents.claude]
driver = "claude"
label = "Claude Code"
[agents.claude.config]
executable = "/Users/me/.local/bin/claude"
model = "sonnet"
effort = "high"
```

```sh
wi new --agent claude --title "Review authentication"
wi start ITEM
```

Enter prompts in Claude's native TUI. `wi start` and `wi resume` reopen the same conversation; items using Pi are unaffected. Claude uses the host's normal authentication, `CLAUDE.md` files, and settings, except that the driver enforces permission bypass. Wi exports bundled skills as a temporary Claude plugin.

The Claude adapter supports native TUI and persistent headless runtimes. Both use native conversation storage/resume, checkout-history forks, paged history with tools and inline images, generated titles, compact observations, and bounded shutdown. The TUI uses owned tmux panes and native `MessageDisplay` line batches, tested with Claude 2.1.296. Headless uses API text deltas, tested with Claude 2.1.299. Neither mode advertises atomic idle-only controls. API deltas are not guaranteed to contain exactly one model token.

#### Headless mode

Headless requires Claude Code 2.1.299 or later. The adapter also checks native lifecycle, consumed-message identity, interrupt receipt, and queued-cancellation capabilities; newer versions without those contracts fail closed. It runs one persistent installed CLI process with streaming JSON input/output and replayed user-message receipts. It has no tmux dependency and does not deliver terminal keystrokes:

```sh
wi start --agent-mode headless ITEM
wi agent control send --item ITEM "Explain the focused test failures."
wi agent control send --item ITEM --follow-up "Then summarize the changes."
wi agent control send --item ITEM --image picture.png "Describe this image."
wi agent control abort --item ITEM
```

Successive messages retain context in the same process. Prompts reserve work when submitted and refuse observed busy state. Steering uses native `next` priority, which can affect the next model request without cancelling the current tool. Follow-ups wait in Wi's queue until native results settle submitted work. The driver limits pending inputs to 32 and deduplication to 1024 command IDs. Text and image limits match the shared control protocol. Images go directly into structured native user messages, without temporary pathname attachments.

A native queue receipt or replayed input confirms delivery, not execution. Model-request/assistant activity reports execution; results settle only their consumed message IDs. Late results cannot settle newer reservations. API deltas and tool previews stay in bounded private snapshots. Native JSON frames are limited to 16 MiB; malformed, oversized, or truncated frames fail the runtime without forwarding their text to compact events. Missing confirmation leaves delivery uncertain and is never retried automatically.

Abort clears Wi's queued follow-ups and sends one native interrupt with queued-input cancellation. Its receipt does not guarantee cancellation of background tasks. Missing interrupt confirmation blocks further delivery. Shutdown sends SIGTERM and escalates to SIGKILL after three seconds. Atomic idle-only submission and shutdown remain unsupported, including when readiness reports `ready`.

Headless uses the installed CLI's existing authentication and settings, without `--bare`, a separate SDK dependency, or Wi-managed tokens. A normal local print invocation can use an existing subscription login; API keys bill separately. Wi does not implement OAuth, share accounts, or promise Anthropic approval for third-party subscription integrations. Resolve authentication/setup in Claude itself before launch. Headless cannot answer authentication/trust dialogs or interactive tool questions; native approval/input callbacks are denied rather than automatically answered. Ask and answer questions through ordinary conversation messages instead.

Stop the current owner and wait until it is offline before changing modes. Installing Wi never upgrades an existing owner's capabilities or starts a second writer against its conversation.

#### Native TUI controls

In an owned tmux pane with safely resolved Claude keybindings, the driver supports startup prompts, prompt submission, steering, follow-ups, abort, and PNG/JPEG attachments.

Startup prompt delivery waits for `SessionStart` and a 500-ms settling interval. The driver verifies pane ownership, then polls for up to three seconds for an empty, visibly bounded chat editor. Drafts and dialogs are not modified. After pasting once through a private bracketed-paste buffer, it polls pane readback for up to three seconds before sending the resolved submission shortcut once. Readback recognizes the rendered draft, including wrapping, or a collapsed paste marker with the expected newline count. This is supporting evidence only; a matching hook must still confirm the full prompt digest. Missing readback leaves the paste unsubmitted and reports uncertain delivery without retyping or sending more keys. A definite failure before paste sends no prompt input and does not reserve an uncertain submission slot. Prompt requests refuse observed busy state; steering submits the draft directly with the resolved `chat:sendNow` shortcut while busy. The default send-now binding is `Ctrl+X Ctrl+S`. This can background a running tool rather than cancel it, and its completion can trigger a later turn. Follow-ups stay in a bounded driver queue until a `Stop` or `StopFailure` hook, then submit as a separate turn. Native Enter-while-busy queueing is not used for follow-ups because it can change the current answer before Stop.

Abort cancels driver-queued follow-ups and sends the resolved interrupt shortcut once to stop active foreground work. It prefers `chat:cancel`, Escape by default, and falls back to Claude's reserved Ctrl+C interrupt if no portable cancel binding is available. It verifies pane ownership and refuses drafts, dialogs, or copy mode. An idle abort only clears the driver queue; it does not send an interrupt key or open Claude's rewind UI. Background tasks are not guaranteed to stop. An interruption record or a settled-turn hook confirms the abort. If confirmation is missing after 15 seconds, the driver reports uncertainty, blocks further queue draining until fresh idle evidence, and never repeats the interrupt shortcut automatically.

`wi agent runtime status` separates process `state` and `online` from observed `readiness`: `starting`, `ready`, `busy`, `awaiting_confirmation`, `waiting_input`, `blocked`, or `stopping`. `SessionStart` alone no longer means the editor is ready. Until the hook arrives, a running process reports `waiting_input` after three seconds with `session_start_pending`; inspect the TUI for setup dialogs or disabled hooks. The driver reports readiness changes through compact `runtime.status` events and never answers dialogs. Readiness observations are owner-fenced, timestamped, and rebuildable.

The control acknowledgement means the driver accepted the command. For prompt-like commands, `command.accepted`, with command/request IDs, follows a matching `UserPromptSubmit` hook. For active aborts it follows settlement confirmation; idle aborts are acknowledged immediately. The receipt hook forwards a prompt digest, never the prompt text. Receipt does not establish model execution: other `UserPromptSubmit` hooks can still block it. Readiness remains `awaiting_confirmation` with `execution_pending` until assistant display or tool activity proves execution. A native blocked-prompt record yields a failed `agent.settled` event with `prompt_blocked` and the originating Wi command/request IDs. Its native reason and original prompt are not forwarded. Claude may restore the blocked draft to the editor; Wi leaves it untouched.

Native `prompt_id` metadata fences late Stop, failure, and tool hooks from newer prompts. Hook-start timestamps provide a conservative fallback when the backend omits that identity. This does not make idle control atomic or reveal other hooks' future decisions. Later same-prompt assistant/tool activity can reactivate work after a Stop-hook continuation.

Monitor events carry optional `delivery_state` and `reason_code` fields. Delivery states distinguish `queued`, `submitted`, `confirmed`, `not_sent`, `uncertain`, and `cancelled`. Reason codes come from a fixed safe vocabulary rather than native error or UI text. Deferred failures therefore remain visible with command/request IDs after the initial control acknowledgement. Commands are deduplicated by ID within a runtime, and uncertain delivery is never retried automatically. Missing confirmation stops queue draining until another lifecycle hook establishes idle. The queue is limited to 32 entries, each prompt to 64 KiB, and the runtime deduplication table to 1024 commands. Queued follow-ups are not retained across runtime exit; compact rejection events identify undelivered commands. The driver reads `keybindings.json` under `CLAUDE_CONFIG_DIR`, merges relevant Chat, Global, and Task overrides with defaults, and resolves bindings by action. Unrelated customizations do not disable controls. Null unbindings, aliases, and portable multi-key chords are supported. Conflicting keys or chord prefixes are not used. Shortcuts requiring extended terminal modifiers, such as Cmd/Super, Ctrl+Enter, or Ctrl+Shift letters, are not considered portable. Missing submit bindings disable prompt and follow-up; missing send-now bindings disable steering. Malformed or ambiguous configurations disable all terminal controls. The file is bounded to 128 KiB. If it changes after launch, terminal controls refuse delivery until the runtime restarts; the driver does not change an existing owner's advertised capabilities or restart it implicitly. Drafts, dialogs, copy mode, and changed pane ownership cause explicit delivery errors.

History reads a bounded page from one native transcript branch, skips subagent sidechains, renders compaction boundaries and summaries, folds tool results into calls, and provides cursors for earlier pages. Results whose calls are on an earlier page recover bounded call context without duplicating older entries. It ignores an incomplete trailing record while the TUI writes. Native content stays in read-only history responses and private live snapshots, not daemon events.

Attach images with `wi agent control send --item ITEM --image picture.png "Describe this image"`. Repeat `--image` for up to four images totaling 8 MiB decoded; text is optional. Claude currently accepts PNG and JPEG only. The driver validates image formats and bounds dimensions to 10,000 pixels per side and 20 million pixels per image. It stages private temporary files outside the checkout, pastes each pathname through Claude's native image-attachment handling, and verifies a new image marker before submitting once. It does not use the system clipboard or ask the model to read files. Native image markers are included in the expected prompt digest. Staged files are removed after matching confirmation or normal runtime exit; uncertain attachment delivery retains them until exit and never retries. Images and paths do not enter compact daemon events. Queued images, like queued text, are runtime-local.

The runtime exposes a read-only content socket with owner-fenced, bounded snapshots. `MessageDisplay` sends assistant line batches to a separate private runtime endpoint, never the compact event socket. The hook returns no display replacement, so Claude's native UI remains unchanged. Wi replaces the current snapshot as batches arrive, checks prompt/message identities and batch indices, ignores duplicates and retired messages, and marks gaps or truncation explicitly. Text is bounded to 256 KiB; each incoming batch is bounded to 16 KiB. Optional transport failures time out after 500 ms without blocking native display or reporting native content as an error. This is whole-line streaming, not per-token streaming.

Completed transcript records still refresh every 250 ms for final history and tool results. A lagging previous record cannot overwrite an in-flight display snapshot. After native settlement, a complete record matched to that prompt recovers missing display batches even when Claude wrote it before the display hook started. Native display message IDs are not transcript IDs and are never substituted for history leaf IDs. Failed records arriving with the first native transcript write are handled as new outcomes rather than discarded as historical replay. Transcript replacement/truncation resets its tail reader. API failure hooks and native interruption markers report failed settled turns so attention remains actionable. Failed event flags are preserved by `wi agent monitor`. Shutdown requires observed readiness and idle state when requested; explicit shutdown sends SIGTERM and escalates to SIGKILL after three seconds if Claude does not exit. Inspection failures remain visible in the snapshot activity without forwarding native error text.

Title generation uses a separate Claude print request with tools, skills, hooks, MCP servers, and session persistence disabled. It runs outside the checkout, inherits normal user authentication, strips Wi runtime identity, and bounds the request and output. It does not acquire an item's conversation.

Claude writes its native JSONL directly into the item's `sessions/claude/` directory. A Wi-specific project namespace under the normal Claude configuration directory points there; it does not redirect unrelated Claude sessions or copy credentials. The adapter relies on Claude's project-directory namespace support, tested with Claude Code 2.1.296 for TUI and 2.1.299 for headless. TUI observations use lifecycle hooks; headless observations use native JSON frames. Disabling TUI hooks removes timely activity reporting; idle-required shutdown is refused until the runtime has reported readiness. A reported namespace mismatch stops startup rather than silently writing conversation history elsewhere.

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
