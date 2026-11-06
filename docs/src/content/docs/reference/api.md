---
title: Daemon API
description: The wi.core.v1 API the daemon serves, how to call it, and its conventions.
---

Wi's daemon serves one public API, `wi.core.v1`, and runs every operation behind it: creating and changing items, claiming and setting up checkouts, starting and stopping agents, Git integration, plugins' hooks, and tmux. The CLI, the `wi web` backend, plugins' automation and any other tool are clients of this API and only of this API. None of them reads Wi's storage or runs its adapters.

The contract is protobuf, in [`proto/wi/core/v1/`](https://github.com/regb/workitem/tree/main/proto/wi/core/v1); the comments there are the reference for every message and field. The daemon serves it with [Connect](https://connectrpc.com/), so one endpoint speaks Connect (JSON or binary protobuf over HTTP), gRPC and gRPC-Web. JSON uses the proto field names (`api_version`, not `apiVersion`), enum values by name (`ITEM_STATE_ACTIVE`), timestamps as RFC 3339 strings and bytes as base64; fields at their default value (empty, zero, false) are left out.

## Endpoints

The daemon listens on Unix sockets only, each with its own access level. Access is decided by the socket a request arrives on, after checking that the peer runs as the same user; no request header can raise it.

| Socket | Path | Access |
| --- | --- | --- |
| Operator | `$XDG_RUNTIME_DIR/wi/<data-root-key>.sock` | Everything |
| Web adapter | `web-<data-root-key>.sock` next to the operator socket | Operator access, identified as the web adapter; `wi web` uses it |
| Agent | `$XDG_RUNTIME_DIR/wi-agent/<data-root-key>/daemon.sock` | Restricted, for sandboxed agents |

On macOS, where socket paths are limited to 104 bytes, a path that would be too long moves under `/tmp/wi-<uid>/`. `wi info operator-socket` prints the operator socket's path. The daemon also has a private endpoint for the agent processes it starts; it is not part of this API.

The agent endpoint allows reading items, repositories and agent profiles (`GetInfo`, `ResolveItem`, `GetItem`, `ListItems`, `SearchItems`, `ListNotes`, `ReadAttachment`, `ListRepositories`, `ListAgentProfiles`), creating backlog items without title generation (`CreateItem`), and adding notes and attachments (`AddNote`, `Attach`). Everything else fails with `PERMISSION_DENIED`. A sandbox selects it with `WI_AGENT_DAEMON_SOCKET`; see [Delegate work](../../guides/delegation/#agent-authority).

## Call it

For a program, generate a client from the protos with [Buf](https://buf.build/) and the Connect or gRPC plugins for its language. Wi's own Go client is generated this way; [`docs/api-generation.md`](https://github.com/regb/workitem/blob/main/docs/api-generation.md) in the repository explains how to regenerate it and check a contract change.

For a quick look, unary calls take Connect JSON with curl over the socket. The path is `/<package>.<service>/<method>`, and the request body is the request message:

```bash
socket="$(wi info operator-socket)"
curl --silent --unix-socket "$socket" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  --data '{}' http://localhost/wi.core.v1.SystemService/GetInfo
```

```json
{"version":"devel+3f2a91c04b7e","api_version":"1.0","data_root":"/home/me/.local/share/wi","default_agent_profile":"pi","started_at":"2026-11-03T08:12:44.512Z","caller":{"access":"CALLER_ACCESS_OPERATOR","kind":"CALLER_KIND_LOCAL_OPERATOR"}}
```

The items that need attention, in the daemon's ranking:

```bash
curl --silent --unix-socket "$socket" \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  --data '{"order":"LIST_ORDER_ATTENTION"}' \
  http://localhost/wi.core.v1.ItemService/ListItems |
  jq -r '.items[] | select(.attention_group == "ATTENTION_GROUP_NEEDS_ATTENTION") | [.id, .title] | @tsv'
```

A failed call answers with an HTTP error status and a JSON body such as `{"code":"failed_precondition","message":"agent is still running"}`. Streaming methods (`WatchChanges`, `WatchJob`, `WatchLiveContent`, `ReadAttachment`) use Connect's enveloped framing; call them with a generated client or a gRPC tool rather than plain curl.

`--json` on CLI commands prints the response message of the call the command makes, in the same JSON form; see [Global JSON output](../commands/#global-json-output).

## Services

| Service | Covers |
| --- | --- |
| `ItemService` | Items: creation, selector resolution, lists in the requested order, search, edits, lifecycle record, attention state, blockers, notes, attachments, history |
| `LifecycleService` | Porcelain: create and start, start, shelve, close, reopen |
| `WorkspaceService` | Checkout claims: claim and set up, set up again, commit, release, uncommitted files, file search |
| `AgentService` | The agent: state, start, stop, restart, messages, abort, settings, title suggestions, history, live content |
| `ControlService` | Control leases: who may send input, what taking control would do, acquire, renew, release |
| `RepositoryService` | Repositories: registration, upstream and synchronization, integrating an item, settings, checkout setup, copied files, environment variables |
| `AgentProfileService` | Agent profiles, the default profile, drivers |
| `JobService` | Long operations: progress, approvals, cancellation |
| `EventService` | The change feed |
| `SystemService` | Daemon information, health, diagnostics, effective configuration, plugin registry, shutdown |
| `ViewService` | Composite reads, such as everything a conversation page shows for an item |

`SystemService.GetInfo` reports `api_version`. Clients refuse a daemon whose major version differs from theirs.

## Plumbing and porcelain

Like Git, the API has two levels. Plumbing is the domain services' small operations: `ItemService.SetItemState`, `WorkspaceService.EnsureWorkspace` and `ReleaseWorkspace`, `AgentService.StartAgent` and `StopAgent`. Porcelain is `LifecycleService`, whose operations move an item and its resources together, each documented as the plumbing it performs:

| Porcelain | Plumbing, in order |
| --- | --- |
| `StartItem` | `SetItemState(ACTIVE)`, `EnsureWorkspace` (unless `skip_setup`), then `SendMessage` with the prompt or the startup message, or `StartAgent` with `send_nothing` |
| `CreateAndStartItem` | `CreateItem`, then `StartItem` |
| `ShelveItem` | `StopAgent`, `ReleaseWorkspace`, `SetItemState(BACKLOG)` |
| `CloseItem` | `StopAgent` (with `force`), `CommitWorkspace` (with `commit_changes`), `ReleaseWorkspace`, `SetItemState(CLOSED)` |
| `ReopenItem` | `SetItemState(BACKLOG)`, then `StartItem` with `send_nothing` when `start` is set |

The daemon enforces lifecycle invariants in the plumbing itself, so no order of calls leaves an unsafe state, even interleaved with another client's: a backlog or closed item holds no checkout claim and no running agent, so `SetItemState` refuses to leave `ACTIVE` while either is held; releasing refuses a checkout with uncommitted changes; stopping refuses a busy agent without `force`; a closed item returns to work through the backlog. A client that wants different behavior composes the plumbing itself.

## Conventions

**Exact IDs.** Requests name items and repositories by exact ID. Clients resolve what people type: `ItemService.ResolveItem` turns an ID, ID prefix, slug or a path inside an item's checkout into an ID, and fails listing the candidates when the selector is ambiguous. Client-side policy, such as default labels, label filters or a custom ranking, is applied by the client and sent explicitly. Files and standard input arrive as content; times are absolute, so clients parse "tomorrow" themselves.

**Fencing tokens.** A mutation that depends on what the caller read carries `expected_revision` (items, settings, profiles, environment) or `expected_generation` (an agent, which gets a new generation each time it starts). Where the token is optional, such as on items and messages, zero accepts any.

**Errors.** `NOT_FOUND`: no such item, repository or profile. `INVALID_ARGUMENT`: a malformed request. `FAILED_PRECONDITION`: the current state does not allow the operation, such as releasing a checkout while the agent runs; nothing changed. `ABORTED`: a fencing token, lease or confirmation is stale; re-read and retry. `ALREADY_EXISTS`: a name or slug is taken. `PERMISSION_DENIED`: the caller's endpoint may not do this.

**Idempotency keys.** Creations that must not happen twice take a key: `CreateItemRequest.request_id` returns the item a first attempt created, and `client_message_id` keeps a message from being delivered twice to the same item. The ID is also echoed on the live content stream, so the client can match the message to its history entry.

**Jobs.** Operations that can outlive the request, such as setting up a checkout, starting an agent or an item, restarting an agent, shelving, closing, reopening, integrating an item and synchronizing a repository, return a `Job`. A job keeps running when its client disconnects; `JobService.WatchJob` streams its state, phase, log tail and outcome, starting with its current state. A job that needs a person's answer, such as trusting a checkout's `.envrc`, waits in `JOB_STATE_WAITING_FOR_APPROVAL` until `AnswerApproval`; the question expires after ten minutes and is bound to the file's content, so a changed file asks again. The final state is read from the domain services, such as `ItemService.GetItem`. Job summaries are saved in the daemon's database: a job still running when the daemon stops ends as failed with "interrupted by a daemon restart", keeping its last progress and partial outcome, and stays readable. The daemon keeps the latest 500 finished jobs; older IDs return `NOT_FOUND`. `CreateItem` stays synchronous.

**Partial outcomes.** When a durable step succeeded before a later one failed, such as an item created whose agent did not start, the error carries a `PartialOutcome` detail naming the item and what was kept. A job reports the same in its `partial` field. Clients show it rather than assume nothing happened.

**Caller identity.** The daemon knows the caller from the endpoint, and `GetInfo` returns it as `caller`: `access` (`CALLER_ACCESS_OPERATOR` or `CALLER_ACCESS_AGENT`) and `kind` (local operator, web adapter or agent). It grants nothing by itself. Within an endpoint, the `Wi-Client-Id` header names the client holding a control lease (the CLI sends `cli`, or `WI_CLIENT_ID` when set), and `Wi-Client-Kind` how its lease is shown (`terminal` or `web`); neither changes access.

**Control.** Sending input to an agent, or changing a running one's settings, needs the item's control lease (`ControlService`). `GetControl` returns the lease holder and a `plan` of what taking control would do: abort a turn, restart the agent in another mode, make a terminal read-only. Taking control from another holder needs `confirm` and the plan's `plan_token`; a token from a plan that no longer holds fails with `ABORTED`, so the caller re-reads and asks again.

**Pages and cursors.** Page tokens (`ListItems`, `ListItemEvents`) are best effort: concurrent changes can shift later pages. Stream positions, for the change feed and live content, are opaque cursors clients only send back. Counters and revisions are 32-bit, so JSON clients get plain numbers.

**Change feed.** `EventService.WatchChanges` reports which items, repositories or profiles changed after a cursor, so clients re-read what they show instead of polling. When a cursor cannot be resumed, for example after a daemon restart, the first message says that anything may have changed.

## What stays in clients

The CLI and `wi web` keep only what belongs to their medium: argument parsing and selector resolution (through `ResolveItem`), default labels and label filters from configuration and environment, reading files and standard input, the editor for `wi edit`, fzf selection in `wi switch`, `wi next` and `wi pick`, attaching to tmux with the terminal the daemon reports, rendering and exit codes, `wi daemon start|stop`, and plugin commands such as `wi usage`, which [is not part of the API](../provider-usage/).
