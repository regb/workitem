# wi daemon API

The contract is in `proto/wi/core/v1/`, package `wi.core.v1`. The daemon
serves Connect JSON, protobuf, gRPC and gRPC-Web over Unix sockets. The CLI
and web backend use generated clients; use cases and adapters run in the
daemon. Generated Go code is checked in under `gen/wi/core/v1/`.

Run generation from the repository root:

```sh
nix develop
scripts/generate-api
```

Without Nix, use Buf 1.57.2:

```sh
go run github.com/bufbuild/buf/cmd/buf@v1.57.2 build
go run github.com/bufbuild/buf/cmd/buf@v1.57.2 lint
go run github.com/bufbuild/buf/cmd/buf@v1.57.2 generate
git diff --exit-code -- gen/
```

The Go generation tools are pinned in `go.mod`. See
`docs/api-generation.md` for tool setup. The files in the table below live
under `proto/wi/core/v1/`.

## Two levels, like Git

- **Plumbing**: the domain services' small operations, such as changing an
  item's state, claiming, committing or releasing a checkout, starting or
  stopping an agent. The daemon enforces lifecycle invariants here (a closed
  item holds no checkout and no running agent; releasing refuses a dirty
  checkout; stopping refuses a busy agent without force), so no order of
  calls leaves an unsafe state.
- **Porcelain**: `LifecycleService` (create and start, start, shelve, close,
  reopen). Each operation is documented as the plumbing it performs, so a
  client can recompose it differently.

| File | Services |
| --- | --- |
| `common.proto` | Shared enums and messages: states, statuses, agent mode, approvals, partial outcomes |
| `items.proto` | `ItemService`: items, lists in the requested order, search, edits, attention state, blockers, notes, attachments, history |
| `lifecycle.proto` | `LifecycleService`: create and start, start, shelve, close, reopen |
| `workspaces.proto` | `WorkspaceService`: claim and setup, commit, release, uncommitted files, file search, reading and saving checkout files |
| `agents.proto` | `AgentService`, `ControlService`: agent state, start, stop, restart, messages, conversation, live content, settings, title suggestions, control leases |
| `repositories.proto` | `RepositoryService`: registration, upstream and sync, integrating an item, settings, checkout setup, files, variables |
| `profiles.proto` | `AgentProfileService`: agent profiles and drivers |
| `jobs.proto` | `JobService`: long operations, progress, approvals, cancellation |
| `events.proto` | `EventService`: the change feed |
| `system.proto` | `SystemService`: info, health, diagnostics, config, plugin registry, shutdown |
| `views.proto` | `ViewService`: composite reads, such as everything a conversation page shows for an item, at one point in time |

## Conventions

- Exact IDs in requests; clients resolve selectors (`ItemService.ResolveItem` for prefixes, slugs and checkout paths). Client-side policy, such as default labels, label filters or a custom ranking, is applied by the client and sent explicitly.
- `GetInfoResponse.caller` reports listener-assigned access and identity kind. Local operator and web-adapter listeners have operator access; the restricted agent listener has agent access. Unix peer checks authenticate the local UID. The web adapter uses a separate daemon-owned Unix listener so its identity kind does not depend on an untrusted header. Caller information is descriptive and never grants permissions.
- `Wi-Client-Id` is the lease client ID, defaulting to `cli`. `Wi-Client-Kind` selects lease holder presentation, `terminal` or `web`; it does not select authorization or caller kind. Browser tabs use distinct client IDs. There is no `Wi-Endpoint-Access` response header.
- `expected_revision` and `expected_generation` fence mutations based on an earlier read.
- Non-idempotent creations take a key: `CreateItemRequest.request_id`, and `client_message_id` for messages, so retries never create or send twice.
- No 64-bit integers: counters, sizes and revisions are 32-bit, so JSON clients get plain numbers. Stream positions (the change feed, live content) are opaque cursor strings clients only send back.
- Long operations return a `Job`; their final state is read through domain services. `RestartAgent` and every lifecycle response can carry a job. Fast no-op or metadata-only operations may finish synchronously.
- `CreateItem` stays synchronous. With the default base, failed upstream synchronization falls back to the local integration branch and returns a warning. An explicit base does not synchronize.
- `override_capacity` on `SetItemStateRequest` and `StartOptions` bypasses deep-work capacity only for operator callers. CLI `--force` sets it for state changes and starts. Busy-agent force-stop remains a separate option.
- A failure after a durable step returns an error with a `PartialOutcome` detail.
- Times are absolute (`Timestamp`); clients parse human input such as "tomorrow".
- Files and stdin arrive as content; `checkout_path` only records where an attachment came from.
- An agent is a durable conversation that is running or not. How it runs is the daemon's business: clients see its state, mode, capabilities and a generation that changes on each start. `SendMessage` starts it when needed; `StartAgent`, `StopAgent` and `RestartAgent` are the explicit controls.
- An agent in TUI mode reports its `Terminal` (tmux socket, session, window, attached clients). Clients attach with tmux themselves; the daemon doesn't mediate attaching. TUI mode exists because native TUIs have interactions the API doesn't model (the agent's questions, permission prompts). The item's own tmux session with shell windows is a client feature, not core.

## Plumbing rules

Each plumbing call checks its preconditions atomically in the daemon, so a sequence of calls interrupted or interleaved with another client's never leaves an unsafe state. Clients composing plumbing pass fencing tokens and idempotency keys; the porcelain does the same internally, under the daemon's per-item lock.

Errors: `NOT_FOUND` (no such item, repository or profile), `INVALID_ARGUMENT` (malformed request), `FAILED_PRECONDITION` (the state does not allow it; nothing changed), `ABORTED` (a fencing token is stale; re-read and retry), `ALREADY_EXISTS` (a name or slug is taken), `PERMISSION_DENIED` (the caller's identity may not do it). A failure after a durable step carries a `PartialOutcome`.

### Item lifecycle

The states and what each requires of the item's resources:

| State | Checkout claim | Agent |
| --- | --- | --- |
| `BACKLOG` | none | not running |
| `ACTIVE` | optional (claimed by `EnsureWorkspace`) | optional |
| `CLOSED` | none | not running |

Transitions through `SetItemState`: backlog → active, active → backlog, active → closed (with a resolution), backlog → closed (with a resolution), closed → backlog. Closed → active goes through backlog. A transition whose target state's requirements are not met fails with `FAILED_PRECONDITION`, naming what is still held.

### Calls

| Call | Preconditions (else `FAILED_PRECONDITION`) | Token (else `ABORTED`) | Idempotent |
| --- | --- | --- | --- |
| `ItemService.CreateItem` | repository exists and is not archived; base resolves; agent profile exists | none | with `request_id` |
| `ItemService.UpdateItem` | none | `expected_revision` (optional) | yes: same values, same result |
| `ItemService.SetItemState` | target state's requirements above; closing requires a resolution | `expected_revision` (optional) | yes: already in the state succeeds without change |
| `ItemService.DeleteItem` | closed, no claim, agent not running; `confirm` | none | no: a second call finds nothing (`NOT_FOUND`) |
| `ItemService.MarkRead`, `DeferItem` | none | none | yes |
| `ItemService.AddBlocker` | item not closed; a blocking item exists and is not this one | none | no: each call adds a blocker |
| `ItemService.ClearBlocker` | none | none | yes: an absent blocker succeeds |
| `ItemService.AddNote`, `Attach` | item exists | none | no: each call adds a note |
| `WorkspaceService.EnsureWorkspace` | item active | none | yes: an existing claim is returned; a claim whose setup failed is set up again |
| `WorkspaceService.SetupWorkspace` | item active with a claim; agent not running | claim generation (implicit: results of a setup started for an older claim are discarded) | yes: a running setup is returned |
| `WorkspaceService.CommitWorkspace` | claim held; agent not running | none | yes: nothing to commit returns no commit |
| `WorkspaceService.ReleaseWorkspace` | agent not running; checkout clean after removing unchanged attached copies | none | yes: no claim succeeds |
| `WorkspaceService.SaveWorkspaceFile` | item active; claim held; checkout on the item's branch; file exists as UTF-8 text | claim ID and generation, SHA-256 of the content read | no: a retry with the same digest finds the saved content and fails with `ABORTED` |
| `AgentService.StartAgent` | item active with a claim whose setup succeeded (or was skipped); the profile's driver is configured | none | yes: running in the requested mode returns it; running in another mode restarts it when idle, else `FAILED_PRECONDITION` |
| `AgentService.StopAgent` | not busy, unless `force`; attached terminal clients need `confirm_attached_terminal` | none | yes: not running succeeds |
| `AgentService.RestartAgent` | running and idle | `expected_generation` (optional) | no: each call restarts |
| `AgentService.SendMessage` | item active with a claim; `PROMPT` needs an idle agent; `STEER` and `FOLLOW_UP` need a running, busy agent; the caller holds the control lease, or it is vacant and taken | `expected_generation` (optional) | with `client_message_id` |
| `AgentService.AbortTurn` | caller holds the control lease | none | yes: no turn running succeeds |
| `AgentService.UpdateAgentSettings` | values valid for the driver; changing a running agent needs the control lease | none | yes |
| `ControlService.AcquireControl` | taking from another holder needs `confirm` | `plan_token` required with `confirm`; stale or missing is `ABORTED` | yes: the caller\'s own lease is renewed |
| `ControlService.RenewControl`, `ReleaseControl` | the lease is the caller's | the lease ID | yes |
| `RepositoryService.IntegrateItem` | item's branch exists; no reconciliation pending; target branch exists | none | yes: an already-integrated branch succeeds without change |
| `RepositoryService.SyncRepository` | upstream configured; `RESET_TO_UPSTREAM` needs `confirm`; `CONTINUE` and `ABORT` need a pending reconciliation | none | yes for fast-forward |
| `RepositoryService` settings, setup, environment | values valid for the option schemas | `expected_revision` | yes |
| `RepositoryService.PutRepositoryFile`, `DeleteRepositoryFile` | path valid | none | yes |
| `AgentProfileService` changes | the default profile cannot be deleted | `expected_revision` | yes |

### Porcelain as plumbing

| Porcelain | Plumbing, in order |
| --- | --- |
| `StartItem` | `SetItemState(ACTIVE)`, `EnsureWorkspace` (unless `skip_setup`), `SendMessage` with the prompt or startup message (starting the agent), or `StartAgent` with `send_nothing` |
| `CreateAndStartItem` | `CreateItem`, then `StartItem` |
| `ShelveItem` | `StopAgent`, `ReleaseWorkspace`, `SetItemState(BACKLOG)` |
| `CloseItem` | `StopAgent` (with `force`), `CommitWorkspace` (with `commit_changes`), `ReleaseWorkspace`, `SetItemState(CLOSED)` |
| `ReopenItem` | `SetItemState(BACKLOG)`, then `StartItem` with `send_nothing` when `start` is set |

## Jobs, confirmation and reads

Job summaries are persisted in the daemon database before publication. A
disconnected client does not stop its job. Execution does not resume after
a daemon restart: every unfinished summary becomes `FAILED` with error
`interrupted by a daemon restart`. Its last recorded progress and partial
outcome remain available, but approvals are cleared. Explicit cancellation
remains `CANCELLED`. History keeps the latest 500 finished summaries plus
all unfinished jobs. Evicted IDs return `NOT_FOUND`.

Every returned control plan carries an opaque `plan_token`. A confirmed
`AcquireControl` must echo the plan the caller saw. The daemon checks it
again at the controller-locked decision point and in queued acquisitions.
Changed ownership, runtime, interruption effects or relevant intent makes
it stale; `ABORTED` requires a new read and confirmation. Lease renewal
alone does not invalidate an unchanged plan. Tokens do not survive daemon
restart. The browser sends its displayed token rather than silently
fetching a different plan after confirmation.

List page tokens are offset-based and best-effort under concurrent changes.
Later pages may shift; there is no retained pagination snapshot.

`GetItemView` checks the item revision before and after assembling the
view. A changed revision causes a retry, then `ABORTED` if it stays
unstable. This is not an atomic snapshot of Git, processes, jobs or native
driver files. External observations can advance independently.

## Attention

The core holds the facts: each item's activity (requested, completed, read, unread, deferred), unread, blockers, observations, and `needs_attention`. It also holds ranking knobs that people or automation set: `priority`, `pinned`, `snoozed_until`. `ListItems` with `LIST_ORDER_ATTENTION` applies the daemon's built-in ranking and sets each item's `attention_group`. Any client can instead request another order and rank items itself from the same state, to suit its medium or the user. Customization works through state, never through code inside the daemon: automation such as auto-defer is a client that watches events and updates knobs.

## Plugins

Plugins run outside the daemon. The daemon keeps the registry and the options plugins declare.

- **Hooks** take part in core operations (checkout setup, agent environment). They are executables the daemon calls, through a declared contract.
- **Automation** (GitHub sync, auto-defer) is a client of this API: it watches events, calls the API, and keeps its own credentials. The daemon may supervise it, as scheduled jobs work today.
- **Commands** (`wi github`) run client-side. Subscription usage is a plugin both `wi usage` and the daemon run: the daemon checks every five minutes while its API is in use, serves the result through `SystemService.GetUsage`, and sets usage blockers from it.

## Web exposure

`wi web` serves the page and forwards its API calls, unchanged, to the daemon's web-adapter socket, under the tab's identity (`Wi-Client-Id`) and the user its proxy vouches for (`Wi-Client-Label`). The page uses the generated clients like any other client; `wi web` adds nothing to the API. It serves only what a browser needs at a URL: item attachments and checkout documents, so the browser can show or download them.

What the web listener may call is a table in the daemon, `internal/daemonapi/exposure.go`, default deny: a test fails when a procedure is in neither list, so exposing a call to browsers is a reviewed line. Locally the browser is the operator and the allowed calls can already run code on the machine, so the table reduces surface and keeps secrets and irreversible operations out of a page bug's reach; with callers that are not the operator it becomes the authorization boundary.

| Service | The page may call | Refused, and why |
| --- | --- | --- |
| `ItemService` | `CreateItem`, `GetItem`, `ListItems`, `SearchItems`, notes and attachments, blockers, `MarkRead` | `DeleteItem` (irreversible); `SetItemState`, `UpdateItem`, `DeferItem`, `ResolveItem`, `ListItemEvents` (plumbing the page does not use) |
| `LifecycleService` | all: create and start, start, shelve, close, reopen | |
| `AgentService` | messages, `AbortTurn`, `AnswerQuestion`, history, live content, settings, `RestartAgent`, `SuggestTitle` | `StartAgent`, `StopAgent` (plumbing; the page uses the porcelain) |
| `ControlService` | all | |
| `ViewService`, `EventService` | all: `GetItemView`, `Watch`, `WatchChanges` | |
| `JobService` | `GetJob`, `ListJobs`, `WatchJob`, `CancelJob` | `AnswerApproval` (no job asks any more) |
| `WorkspaceService` | files, documents, uncommitted changes, `CommitWorkspace` | `EnsureWorkspace`, `GetWorkspace`, `ReleaseWorkspace`, `SetupWorkspace` (plumbing) |
| `ReviewService` | everything the review panel does | `GetRevisionPatch` (unused) |
| `RepositoryService` | list, add, files, checkout setup, environment, `IntegrateItem` | `ReadRepositoryFile` (stored files can hold secrets); `ArchiveRepository`, `RestoreRepository` (irreversible for the page's purposes); renaming, upstream, sync and raw settings (operator commands) |
| `AgentProfileService`, `ScheduleService` | all | |
| `SystemService` | `GetInfo`, `GetHealth`, `GetUsage` | `GetConfig` (configuration can hold secrets), `ListPlugins`, `RunDiagnostics`, `Shutdown` (operator's, from a terminal) |

The table in code is the reference; this summary groups it.

## CLI mapping

| CLI | API |
| --- | --- |
| `new`, `show`, `search`, `edit`, `events`, `state` | `ItemService` (`edit`'s editor stays client-side; `new` merges default labels from config and environment itself); `new --start` uses `LifecycleService.CreateAndStartItem`; `show` adds `WorkspaceService.GetWorkspace` for ahead/behind |
| `note(s)`, `attach`, `attachment(s)` | `ItemService` notes and attachments |
| `workspace status\|ensure\|setup\|release` | `WorkspaceService` |
| `agent runtime`, `agent status`, `agent settings`, `agent control`, `agent monitor` | `AgentService` (`runtime ensure\|stop` become `StartAgent`, `StopAgent`) |
| `agent profile` | `AgentProfileService` |
| `list`, `attention queue\|activity\|defer` | `ItemService.ListItems`, the item's attention activity, `DeferItem` |
| `terminal status\|ensure\|enter\|close` | `AgentService.GetAgent` reports the TUI agent's terminal and `StartAgent` starts one; attaching runs client-side with tmux. The item's shell session is a client feature |
| `switch`, `next`, `pick` | Client-side selection over `ListItems`, then attaching |
| `start`, `resume`, `shelve`, `close`, `reopen` | `LifecycleService`, `AgentService.StartAgent` |
| `merge` | `RepositoryService.IntegrateItem` |
| `wait`, `blocker`, `delete` | `ItemService` |
| `repo ...` | `RepositoryService` |
| `info`, `version`, `daemon status\|doctor`, `shutdown`, `config` | `SystemService` |
| `daemon start\|stop`, plugin commands, `usage` | Client-side: service control, and plugin commands that may call the API |

## Monitoring and future work

`wi agent monitor` follows live content. The separate normalized event-monitor
mode and its selection options are removed; live content and the change feed
cover monitoring. No `WatchAgentEvents` RPC is planned by this revision.

Scoped agent credentials remain future work. The current agent listener
identifies a restricted local endpoint, not an item-scoped principal.
Credential issuance, item binding and revocation need their own design.

A gated terminal relay, structured native-agent questions/permissions and
a separately versioned executable-hook protocol also remain future work.
Event payloads and effective configuration retain their existing JSON
string fields. Jobs report progress and outcome; domain getters provide
final results.
