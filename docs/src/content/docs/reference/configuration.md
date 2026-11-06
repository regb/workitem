---
title: Configuration
description: Configure capacity, labels, attention ranking, direnv trust, and display.
---

User configuration lives at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/wi/config.toml
```

The file is optional: without it every setting has its built-in default.

The daemon reads it once, when it starts, and applies it to every operation it runs: deep-work capacity, attention ranking, following the upstream, agent messages, drivers and plugins. Restart the daemon (`wi daemon stop`; the next command starts it again) after changing those. A few settings belong to clients and are read by each command as it runs: `list.labels`, `item.defaults.labels`, `daemon.managed`, and the plugin tables that enable plugin commands such as `wi usage`. Agent profiles are not configured here; Wi's daemon keeps them, managed with `wi agent profile` or in the web Settings → Agents page (see [Coding agents and drivers](#coding-agents-and-drivers)). A file that still sets `default_agent` or `[agents.NAME]` is refused with a message saying how to move them. A repository may also provide `.config/wi.toml` for supported project settings.

## Inspect configuration

```bash
wi config get deep_work.max_active
wi config get list.labels --json
wi config get agent.startup_message
wi config get agent.append_system_prompt
wi config drivers
wi config plugins
wi config get plugins.direnv.trust --origins
wi repo settings show workitem --json
wi config show --json
```

`wi config` defaults to `show`. These queries read the daemon's effective global configuration with built-in defaults (`SystemService.GetConfig`), without running drivers, plugins, or direnv. Repository policy belongs to daemon-owned entity settings, inspected with `wi repo settings show NAME`, not per-repository config files. Config queries do not evaluate checked-in project settings, environment label rules, or profile/driver merges. Invalid configuration reports an error.

`wi config plugins` displays registered plugin options, defaults, types, and repository-scope permissions as JSON. `--origins` wraps a query result in `value` and `origins`, identifying global plugin values supplied by a default or user configuration. Plugin settings marked sensitive, and values belonging to unregistered plugins, are redacted unless `--show-sensitive` is supplied.

`get KEY` accepts dot-separated configuration keys or sections. Text scalars print directly; sections print sorted dotted settings. `--json` emits the selected JSON value, without a wrapper. An unknown key exits with code 3; invalid arguments exit with code 2.

Driver defaults, plugin settings, and custom commands may contain secrets. Wi hides their values by default, retaining the setting names. Use `--show-sensitive` only when you intend to reveal them, including in JSON output:

```bash
wi config get drivers.pi.defaults --show-sensitive --json
```

`wi info config-file` prints the configuration path. Configuration queries never rewrite the file.

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

[repo]
follow_upstream = "fast-forward"

[plugins.direnv]
load = true
trust = "prompt"

[agent]
startup_message = "Read this work item's description with `wi show`. Work on the task it describes. Run relevant checks and report the outcome."

[agent_status.markers]
busy = "agent"
idle = "review"
problem = "problem"

[drivers.pi.defaults]
extensions = ["/home/me/tools/web-search.ts"]
```

## Deep-work capacity

`deep_work.max_active` limits active items marked as deep work, waiting or not. The daemon enforces it when an item becomes active. A value of `0` prevents starting deep backlog work unless you pass `--force`, which sends `override_capacity` with the request; only operator callers may override, never the agent endpoint.

## Daemon startup environment

An unmanaged daemon started automatically or by `wi daemon start` restores the environment from before the launching shell's direnv changes, including its original PATH. It drops direnv state, all `WI_*` context, `TMUX`, `TMUX_PANE`, and current-directory variables, and starts in its state directory. Normal login variables such as HOME, SSH_AUTH_SOCK, locale, and XDG roots remain unless direnv had changed them. Invalid `DIRENV_DIFF` state refuses startup rather than retaining project secrets.

The environment is captured once at startup, not refreshed by later clients. Foreground `wi daemon serve` and service-managed daemons keep the environment supplied by their launcher.

This environment matters more than it used to: the daemon runs every operation, so Git, setup commands, plugins' hooks and agents run with it, plus a repository's variables, never with the environment of the shell that ran a `wi` command. Variables an agent needs belong in [repository variables](../../concepts/workspaces/#environment-variables), the checkout's `.envrc`, or the daemon's own environment. See [Where the rest of the environment comes from](../../concepts/workspaces/#where-the-rest-of-the-environment-comes-from).

## Managed daemon

By default, wi starts its daemon when a command needs it. A command uses
any running daemon that speaks the same major API version, even one of
another build, so after installing a new build stop the old daemon with
`wi daemon stop`. To let a service manager such as systemd or launchd own
the daemon instead, set:

```toml
[daemon]
managed = true
```

wi then never starts, stops or replaces the daemon itself. A command fails
with an explanation when the daemon is not running; `wi daemon start` and
`wi daemon stop` refuse as well. The daemon
run with this setting refuses to be shut down by any client, including older
wi builds that ignore the setting. `wi shutdown` still stops agents and
terminals, and leaves the daemon running. Upgrade by installing the new build
and restarting the service.

Run the daemon in the foreground under the service manager, as the user who
owns the wi data, never as root. A systemd user unit:

```ini
# ~/.config/systemd/user/wi-daemon.service
[Unit]
Description=wi coordinator daemon

[Service]
ExecStart=/path/to/wi daemon serve
Restart=on-failure
# Agents started by wi live in the service's control group. Stop only the
# daemon itself, so a restart leaves running agents alone.
KillMode=process

[Install]
WantedBy=default.target
```

Enable it with `systemctl --user enable --now wi-daemon`, and let it run
without a login session with `loginctl enable-linger USER`. On macOS, a
LaunchAgent running `wi daemon serve` with `KeepAlive` and
`AbandonProcessGroup` set plays the same role.

## Default labels

Default labels are client policy. `wi new` combines labels from `item.defaults.labels` in user configuration, `WI_ITEM_DEFAULT_LABELS` in its own environment, and explicit `--label` flags, and sends the result; the daemon adds no labels of its own, and a repository's `.config/wi.toml` no longer supplies any. Items created in `wi web` get none. `--no-default-labels` suppresses configured and environment defaults.

```bash
WI_ITEM_DEFAULT_LABELS="jira,backend" wi new --title "Fix issue"
```

## List filters

List label rules use these forms:

- `label` or `+label` requires a label
- `-label` excludes a label
- `!` clears inherited rules

Configuration, `WI_LIST_LABELS`, and command-line flags apply in that order. Higher-precedence rules replace only a rule for the same normalized label. The command combines them and sends the resulting rules to the daemon with the list request.

```bash
WI_LIST_LABELS="!,+personal" wi list --json
```

## Attention ranking

`attention.priority` selects the strategy of the daemon's built-in ranking, the attention order of `ListItems` used by `wi list`, `wi attention queue`, `wi switch`, `wi next` and `wi web`. The current strategy is `recent-request`. Unknown names are rejected so a misspelled strategy cannot silently change queue behavior. Other clients may rank items differently from the same state; see [Attention](../../concepts/attention/#state-in-the-daemon-ranking-in-clients).

## Following the upstream

`repo.follow_upstream`, read by the daemon, says what creating an item does with the integration branch of a repository that has an upstream. `fast-forward`, the default, fetches and fast-forwards it first, like `wi repo sync`, and warns instead of failing when it cannot. `off` creates items from the local integration branch as it is. See [Follow the upstream](../../concepts/repositories/#follow-the-upstream).

## Direnv trust

Direnv is a bundled plugin that runs inside the daemon. Its registered options are `plugins.direnv.load` and `plugins.direnv.trust`.

- `load = true`, the default, loads approved project environments before an agent starts, in TUI or headless mode.
- `load = false` skips evaluation, but still removes inherited direnv state and revokes stale approval when reusing a slot.
- `trust = "prompt"`, the default, asks before approving an untrusted worktree `.envrc` when an agent starts. The daemon's start job waits for an answer: `wi` asks on its terminal, `wi web` in the browser, and other clients answer with `JobService.AnswerApproval`. Declining starts the agent without that environment and reports a warning; an unanswered question expires after ten minutes and the start fails.
- `trust = "always"` automatically approves the current worktree `.envrc`, including changed contents. This is an explicit permission to execute shell code.
- `trust = "never"` does not prompt or grant new approval. Existing direnv approvals remain usable; disable `load` to prevent evaluation altogether.

Set repository policy through the daemon, using a name or ID:

```bash
wi repo settings show biskoui
wi repo settings set biskoui plugins.direnv.trust always
wi repo settings set biskoui plugins.direnv.load false
wi repo settings unset biskoui plugins.direnv.trust
```

Repository settings are stored in the private `wi.db`, keyed internally by stable repository identity. Rename and archive preserve them; reusing an old name does not transfer its policy. They are not configuration-file tables or files generated by Wi.

Values resolve in order: registered plugin defaults, global user defaults, then repository overrides. `unset` removes an override and restores inheritance. Explicit `false` and empty values override inherited settings; arrays replace rather than append. Only registered options permitted at repository scope can be set. Boolean values use `true`/`false`; integers use decimal text; string arrays use JSON such as `'["one","two"]'`.

`show --json` prints the `GetRepositorySettingsResponse`: `settings`, one entry per option with its `key` (`plugins.NAME.OPTION`), typed `effective` value, `origin` (`default`, `global` or `repository`) and `sensitive` flag, plus the settings `revision` and `warnings`. Values marked sensitive, and values for unavailable namespaces, are hidden unless `--show-sensitive` is supplied. Unavailable settings remain stored but inactive. Invalid known settings block their application and are reported so an operator can remove them.

The daemon checks schema permissions and a settings revision on each mutation, deduplicates retries, and records a compact change event without retaining values in event history. These settings endpoints are operator-only. Hooks receive a resolved snapshot for the operation, not a database client. Existing agent environments are not changed in place.

Commands do not load `.envrc` files: `WI_LIST_LABELS` and `WI_ITEM_DEFAULT_LABELS` come only from the command's own environment. Slot reuse revokes previous approval before publishing the new claim; approvals are not shared by file hash.

Checked-in `.config/wi.toml` cannot configure plugins, enable executables, or grant trust. It may declare a setup command under `[workspace.setup] run`, which runs only once the operator enables it, as described below. The former `[direnv] auto_trust_repositories` path allowlist and `[repositories.ID...]` user-config tables are rejected with actionable errors. Copy any intended overrides aside, remove those tables, then apply them explicitly using `wi repo settings set`. Wi does not infer or migrate approvals from files or paths.

## Workspace setup

Two more bundled plugins set up each new checkout claim before its agent starts; see [Repository files and setup](../../concepts/workspaces/#repository-files-and-setup).

- `plugins.files.skip`: repository files (from `wi repo files`) not copied into checkouts. Setup still reads them from `$WI_FILES`.
- `plugins.setup.run`: the setup command, run with `/bin/sh` in the checkout. It replaces a command the repository declares.
- `plugins.setup.trust = "never" | "always"`: whether to run the `[workspace.setup] run` command declared in the repository's `.config/wi.toml` on its default branch. `never`, the default, only reports that one is declared.
- `plugins.setup.timeout_seconds`: how long setup may take, 600 by default.

All are repository-scoped, so set them with `wi repo settings set NAME plugins.setup.run '...'`.

## Initial agent message

`agent.startup_message` is the global message the daemon sends when an item starts with a message but none is given, as `wi new --start` does. It must be a non-empty quoted string. Escape newlines as `\n` within the string. `--prompt TEXT` overrides this message for that creation and implies `--start`. It conflicts with an explicit `--start=false`. Neither message changes the durable description supplied through `--desc-file`.

`wi resume` never sends a startup message. `wi start` for an existing item starts its agent without submitting a message. Use `wi agent control send` to explicitly start a turn in an existing conversation.

## Appended system instructions

`agent.append_system_prompt` adds text to the backend's normal system prompt every time an agent starts, including resumed conversations. The bundled Pi and Claude drivers apply it in both TUI and headless modes. It is separate from `startup_message` and does not submit a user message.

The default identifies the conversation as a Wi item and directs the agent to read `wi show`. Inspect it with `wi config get agent.append_system_prompt`. Set a quoted string to customize it; escape newlines as `\n`. An empty or whitespace-only string disables Wi's addition. Values are literal text, not file paths. Changes apply to agents started after the daemon restarts, not running ones.

```toml
[agent]
append_system_prompt = "Read the task with `wi show`. Ask before changing other work items."
```

## Coding agents and drivers

An agent profile names a coding agent setup: a driver (`pi` or `claude`), an optional display label, and the settings the driver launches with. Wi's daemon keeps the profiles and the default one; they are not part of `config.toml`. Several profiles can use the same driver, for example one per account. Profiles hold no credentials: each agent logs in on its own.

```sh
wi agent profile list
wi agent profile add pi-work --driver pi --label "Pi (work)" executable=/usr/local/bin/pi model=openai-codex/gpt-5.4
wi agent profile edit pi-work reasoning=high model=
wi agent profile default pi-work
wi agent profile remove pi-work
```

`KEY=VALUE` sets a driver setting and `KEY=` removes it. Before a profile is saved, the driver checks its settings through its stateless `config.validate` operation, and `executable` must be an absolute path to an executable file. The web Settings → Agents page edits the same list. When the daemon has no profiles, the first `wi` command adds one for each agent the bundled drivers find installed on `PATH`, with its absolute executable path; Pi's comes first and becomes the default. If none is found, commands that need an agent say how to add one.

Every new item records a named profile: the default unless `wi new --agent NAME` chooses another. Changing the default affects future items only. Removing a profile does not change items that use it: they keep its name and, when they next start, ask to move to another with `wi agent profile set`. The default profile cannot be removed while another exists. Older items that recorded the special name `default` resolve to the current default.

Every driver takes the same two choices: `model`, and `reasoning`, one level of `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. Each driver maps `reasoning` onto its backend, as Pi's thinking level or Claude's effort, and accepts a subset of the levels. A profile's values are defaults; an item chooses its own with `wi new --model M --reasoning L`, `wi start`, or `wi agent settings set`, so a profile per model or level is not needed.

Pi accepts optional `executable`, `extensions`, `model`, and `reasoning`. The executable defaults to `pi`; omitted model and reasoning settings leave the choice to Pi, which accepts every level. `[drivers.<name>.defaults]` in `config.toml` provides settings shared by every profile using that driver; a profile's own settings override them.

```sh
wi agent profile add pi --driver pi --label Pi model=openai-codex/gpt-5.4 reasoning=high
```

Items retain the selected profile name. Starting the agent resolves the profile, applies the item's own model and reasoning, and passes the configuration through the selected driver.

Select a profile with `wi new --agent NAME`.

External driver programs use wi's versioned bootstrap protocol. The daemon starts them. The bundled drivers run as separate `wi internal-driver pi` and `wi internal-driver claude` processes from the daemon's `wi` binary, so the adapters are always present and have the same build version. The underlying `pi` or `claude` executable must still be installed. A configured `drivers.<name>.command` selects a trusted external driver executable; repository configuration cannot register drivers or agent profiles.

### Claude Code

Claude accepts optional `executable`, `model`, and `reasoning`. The executable defaults to `claude`; omitted model and reasoning settings leave the choice to Claude. Reasoning is passed as Claude's effort and accepts `low`, `medium`, `high`, `xhigh`, or `max`.

The bundled Claude driver always passes `--dangerously-skip-permissions`, on both creation and resume, to match Pi's execution without per-tool approvals. Permission mode is not configurable; `permission_mode` is rejected as an unknown field. Claude executes with the host account's access. A managed worktree is not a sandbox.

```sh
wi agent profile add claude --driver claude --label "Claude Code" executable=/Users/me/.local/bin/claude model=sonnet reasoning=high
```

```sh
wi new --agent claude --title "Review authentication"
wi new --agent claude --model opus --reasoning xhigh --title "Plan the migration"
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

Changing modes replaces the running agent: starting it in the other mode restarts it when idle and is refused while it is busy. Installing Wi never upgrades an existing owner's capabilities or starts a second writer against its conversation.

#### Native TUI controls

In an owned tmux pane with safely resolved Claude keybindings, the driver supports startup prompts, prompt submission, steering, follow-ups, abort, and PNG/JPEG attachments.

Startup prompt delivery waits for `SessionStart` and a 500-ms settling interval. The driver verifies pane ownership, then polls for up to three seconds for an empty, visibly bounded chat editor. Drafts and dialogs are not modified. After pasting once through a private bracketed-paste buffer, it polls pane readback for up to three seconds before sending the resolved submission shortcut once. Readback recognizes the rendered draft, including wrapping, or a collapsed paste marker with the expected newline count. This is supporting evidence only; a matching hook must still confirm the full prompt digest. Missing readback leaves the paste unsubmitted and reports uncertain delivery without retyping or sending more keys. A definite failure before paste sends no prompt input and does not reserve an uncertain submission slot. Prompt requests refuse observed busy state; steering submits the draft directly with the resolved `chat:sendNow` shortcut while busy. The default send-now binding is `Ctrl+X Ctrl+S`. This can background a running tool rather than cancel it, and its completion can trigger a later turn. Follow-ups stay in a bounded driver queue until a `Stop` or `StopFailure` hook, then submit as a separate turn. Native Enter-while-busy queueing is not used for follow-ups because it can change the current answer before Stop.

Abort cancels driver-queued follow-ups and sends the resolved interrupt shortcut once to stop active foreground work. It prefers `chat:cancel`, Escape by default, and falls back to Claude's reserved Ctrl+C interrupt if no portable cancel binding is available. It verifies pane ownership and refuses drafts, dialogs, or copy mode. An idle abort only clears the driver queue; it does not send an interrupt key or open Claude's rewind UI. Background tasks are not guaranteed to stop. An interruption record or a settled-turn hook confirms the abort. If confirmation is missing after 15 seconds, the driver reports uncertainty, blocks further queue draining until fresh idle evidence, and never repeats the interrupt shortcut automatically.

The daemon tracks the agent's observed `readiness` separately from whether its process is online: `starting`, `ready`, `busy`, `awaiting_confirmation`, `waiting_input`, `blocked`, or `stopping`. `wi agent status` shows the result as the agent's `online`, `status`, `reason` and `asking`. `SessionStart` alone no longer means the editor is ready. Until the hook arrives, a running process reports `waiting_input` after three seconds with `session_start_pending`; inspect the TUI for setup dialogs or disabled hooks. The driver reports readiness changes through compact `runtime.status` events and never answers dialogs. Readiness observations are owner-fenced, timestamped, and rebuildable.

The control acknowledgement means the driver accepted the command. For prompt-like commands, `command.accepted`, with command/request IDs, follows a matching `UserPromptSubmit` hook. For active aborts it follows settlement confirmation; idle aborts are acknowledged immediately. The receipt hook forwards a prompt digest, never the prompt text. Receipt does not establish model execution: other `UserPromptSubmit` hooks can still block it. Readiness remains `awaiting_confirmation` with `execution_pending` until assistant display or tool activity proves execution. A native blocked-prompt record yields a failed `agent.settled` event with `prompt_blocked` and the originating Wi command/request IDs. Its native reason and original prompt are not forwarded. Claude may restore the blocked draft to the editor; Wi leaves it untouched.

Native `prompt_id` metadata fences late Stop, failure, and tool hooks from newer prompts. Hook-start timestamps provide a conservative fallback when the backend omits that identity. This does not make idle control atomic or reveal other hooks' future decisions. Later same-prompt assistant/tool activity can reactivate work after a Stop-hook continuation.

Command events the driver reports carry optional `delivery_state` and `reason_code` fields. Delivery states distinguish `queued`, `submitted`, `confirmed`, `not_sent`, `uncertain`, and `cancelled`. Reason codes come from a fixed safe vocabulary rather than native error or UI text. Deferred failures therefore remain visible with command/request IDs after the initial control acknowledgement. Commands are deduplicated by ID within a runtime, and uncertain delivery is never retried automatically. Missing confirmation stops queue draining until another lifecycle hook establishes idle. The queue is limited to 32 entries, each prompt to 64 KiB, and the runtime deduplication table to 1024 commands. Queued follow-ups are not retained across runtime exit; compact rejection events identify undelivered commands. The driver reads `keybindings.json` under `CLAUDE_CONFIG_DIR`, merges relevant Chat, Global, and Task overrides with defaults, and resolves bindings by action. Unrelated customizations do not disable controls. Null unbindings, aliases, and portable multi-key chords are supported. Conflicting keys or chord prefixes are not used. Shortcuts requiring extended terminal modifiers, such as Cmd/Super, Ctrl+Enter, or Ctrl+Shift letters, are not considered portable. Missing submit bindings disable prompt and follow-up; missing send-now bindings disable steering. Malformed or ambiguous configurations disable all terminal controls. The file is bounded to 128 KiB. If it changes after launch, terminal controls refuse delivery until the runtime restarts; the driver does not change an existing owner's advertised capabilities or restart it implicitly. Drafts, dialogs, copy mode, and changed pane ownership cause explicit delivery errors.

History reads a bounded page from one native transcript branch, skips subagent sidechains, renders compaction boundaries and summaries, folds tool results into calls, and provides cursors for earlier pages. Results whose calls are on an earlier page recover bounded call context without duplicating older entries. It ignores an incomplete trailing record while the TUI writes. Native content stays in read-only history responses and private live snapshots, not daemon events.

Attach images with `wi agent control send --item ITEM --image picture.png "Describe this image"`. Repeat `--image` for up to four images totaling 8 MiB decoded; text is optional. Claude currently accepts PNG and JPEG only. The driver validates image formats and bounds dimensions to 10,000 pixels per side and 20 million pixels per image. It stages private temporary files outside the checkout, pastes each pathname through Claude's native image-attachment handling, and verifies a new image marker before submitting once. It does not use the system clipboard or ask the model to read files. Native image markers are included in the expected prompt digest. Staged files are removed after matching confirmation or normal runtime exit; uncertain attachment delivery retains them until exit and never retries. Images and paths do not enter compact daemon events. Queued images, like queued text, are runtime-local.

The runtime exposes a read-only content socket with owner-fenced, bounded snapshots. `MessageDisplay` sends assistant line batches to a separate private runtime endpoint, never the compact event socket. The hook returns no display replacement, so Claude's native UI remains unchanged. Wi replaces the current snapshot as batches arrive, checks prompt/message identities and batch indices, ignores duplicates and retired messages, and marks gaps or truncation explicitly. Text is bounded to 256 KiB; each incoming batch is bounded to 16 KiB. Optional transport failures time out after 500 ms without blocking native display or reporting native content as an error. This is whole-line streaming, not per-token streaming.

Completed transcript records still refresh every 250 ms for final history and tool results. A lagging previous record cannot overwrite an in-flight display snapshot. After native settlement, a complete record matched to that prompt recovers missing display batches even when Claude wrote it before the display hook started. Native display message IDs are not transcript IDs and are never substituted for history leaf IDs. Failed records arriving with the first native transcript write are handled as new outcomes rather than discarded as historical replay. Transcript replacement/truncation resets its tail reader. API failure hooks and native interruption markers report failed settled turns so attention remains actionable. Shutdown requires observed readiness and idle state when requested; explicit shutdown sends SIGTERM and escalates to SIGKILL after three seconds if Claude does not exit. Inspection failures remain visible in the snapshot activity without forwarding native error text.

Title generation uses a separate Claude print request with tools, skills, hooks, MCP servers, and session persistence disabled. It runs outside the checkout, inherits normal user authentication, strips Wi runtime identity, and bounds the request and output. It does not acquire an item's conversation.

Claude writes its native JSONL directly into the item's `sessions/claude/` directory. A Wi-specific project namespace under the normal Claude configuration directory points there; it does not redirect unrelated Claude sessions or copy credentials. The adapter relies on Claude's project-directory namespace support, tested with Claude Code 2.1.296 for TUI and 2.1.299 for headless. TUI observations use lifecycle hooks; headless observations use native JSON frames. Disabling TUI hooks removes timely activity reporting; idle-required shutdown is refused until the runtime has reported readiness. A reported namespace mismatch stops startup rather than silently writing conversation history elsewhere.

## Plugins

Native hooks and executable plugins share one catalog and typed option registry. Plugins are fully trusted operator code, not a sandbox. They run outside the daemon's API and take part in three ways:

- **Hooks** take part in core operations the daemon runs: the bundled direnv, files and setup plugins prepare a checkout and an agent's environment. The daemon calls them.
- **Automation**, such as the GitHub plugin's scheduled job, is a client of the [API](../api/): it watches or reads state, calls the API, and keeps its own credentials. The daemon schedules declared jobs.
- **Commands**, such as `wi github` and `wi usage`, run on the client: the `wi` command runs them, and they may call the API themselves.

The daemon keeps the registry and the options plugins declare; `SystemService.ListPlugins` lists them.

Each plugin has one `[plugins.NAME]` table. `enabled`, `command`, and `manifest` describe installation and activation. Every other key is one of the plugin's registered options, with schema validation, defaults, redaction, and repository overrides:

```toml
[plugins.github]
enabled = true
# command = ["/path/to/wi-github"] # optional executable override
interval_seconds = 300             # global only, 30 through 86400
ignore_authors = ["codecov"]        # repository overrides allowed
```

For registered plugins, unknown keys and nested tables are rejected, so a misspelled option cannot silently change behavior. No option may use a registration field's name. Settings-only tables for unavailable plugins remain inactive and inspectable, with their values redacted by default. They do not install a plugin; enabling an external plugin or supplying an executable override requires its declarative manifest. The former `[extensions.NAME]` tables are rejected with a pointer to `[plugins.NAME]`; move their keys into the plugin's table. The coordinator renames stored repository overrides from `extensions.NAME.OPTION` to `plugins.NAME.OPTION` when it upgrades its database to schema 8. Nothing is enabled implicitly.

```bash
wi config plugins
wi plugin list
wi repo settings set biskoui plugins.github.ignore_authors '["codecov","vercel"]'
wi plugin skill github
wi plugin run github
```

Direnv remains registered for required cleanup, so `[plugins.direnv]` accepts options but not `enabled = false` or a `command`. Set `plugins.direnv.load = false` to disable evaluation without removing sanitation or reused-slot approval revocation.

The daemon's scheduler consumes declared jobs from the catalog. Repository jobs receive fresh effective settings per repository on each run, plus `WI_PLUGIN_REPOSITORY_ID`. GitHub filters its listing to that ID. Jobs do not overlap within a job/scope; at most four jobs run concurrently, and each run is limited to two minutes. Shutdown cancels and joins workers. Global registration and interval changes require an explicit daemon restart; repository policy is read on subsequent runs. Existing agents are unchanged.

Commands must be declared and cannot shadow built-ins. `wi NAME SUBCOMMAND` runs on the client, preserves caller context, and reads the selected item's repository settings from the daemon, including for an explicit `--item`. Executable jobs and commands receive `WI_EXECUTABLE`, `WI_PLUGIN`, and effective JSON settings in `WI_PLUGIN_CONFIG`. Explicit restricted endpoint routing is preserved. Manual scheduled-job runs remain operator-only.

`wi plugin list` shows native and executable entries together. Schema-sensitive options, unknown values and commands are hidden unless `--show-sensitive` is supplied. Instructions come from static assets, never an executable `skill` lookup during startup. Enabled plugin instructions are added only to agents started afterwards.

### Third-party registration

Register a declarative JSON manifest explicitly. Wi does not launch programs to discover metadata:

```toml
[plugins.example]
enabled = true
manifest = "/home/me/tools/example/plugin.json"
message = "hello"
```

A minimal manifest:

```json
{
  "version": 1,
  "name": "example",
  "command": ["bin/example"],
  "commands": ["greet"],
  "options": [
    {"name": "message", "type": "string", "default": "hello", "repository": true}
  ],
  "instructions": "instructions.md"
}
```

Names must match registrations. Relative executables and assets resolve against the manifest directory. Instruction assets must be regular UTF-8 files inside the package without symlinks, at most 16 KiB. Manifests are limited to 64 KiB and reject unknown fields. Inspection never launches code.

Optional `jobs` entries declare `name`, `args`, `interval_option` and `repository`. The interval option must be a registered global integer; the catalog enforces 30 through 86400 seconds. External preparation hooks are deferred; direnv uses the native adapter.

### GitHub

`wi-github`, installed next to `wi`, makes items wait on GitHub pull requests. It needs the [`gh` CLI](https://cli.github.com) and its login.

```bash
wi github wait https://github.com/acme/api/pull/42
wi github wait acme/api#42 --item auth
```

`wi github wait` checks that the pull request exists and is open, then adds one external blocker named `activity on acme/api#42`, labelled `#42` in lists, with `github.pr` metadata. Every scheduled run checks each tagged pull request once and clears its blockers on the first activity after the item started waiting. A run for a repository with no tagged blockers makes no GitHub API calls. Activity is:

- a comment or a review, approvals included, by anyone other than you (your `gh` login), bots included;
- a merge or close.

Commits, labels, and CI status do not count. To ignore noisy authors, such as a coverage bot, list them in the plugin's table; a `[bot]` suffix is optional:

```toml
[plugins.github]
enabled = true
ignore_authors = ["codecov", "vercel"]
```

The plugin acts only on blockers carrying `github.pr` metadata, never on event text, so a plain `wi wait --for` event that mentions a pull request is left alone. `wi wait --clear` stops waiting early. Each clearance is recorded in the item's history, and the plugin log names the post, merge, or close that cleared it. While enabled, the plugin also teaches agents to use `wi github wait`, only when you ask them to make an item wait.

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
| `WI_CLIENT_ID` | Client name the CLI gives the daemon for control leases (default `cli`) |

Agents also receive `WI_AGENT_RUNTIME_ID`, `WI_AGENT_MODE`, `WI_AGENT_CONTROL_SOCKET`, and `WI_AGENT_EVENT_SOCKET`. Tmux panes receive `TMUX` and `TMUX_PANE`; headless agents do not require them. Driver-specific variables are implementation details.

The `wi` command reads these from its own environment and sends what they select; the daemon never sees the caller's environment. Changing into a worktree does not inject these variables, and changing out of one does not clear them. Contextual base selection still validates the actual directory and Git store. Sandboxes may expose a narrower environment.
