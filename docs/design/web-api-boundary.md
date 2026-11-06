# The web client's API boundary

Status: accepted, being implemented in the order of [Migration](#migration). Written from commit `3d325cb`. Step 0 is done: the web listener's default-deny exposure (`internal/daemonapi/exposure.go`), the caller's label from `Wi-Client-Label`, upstream URLs without credentials (`internal/remoteurl`), and one admission path for the proxy with bounded calls and streams. Step 1 is done: settings, repositories, schedules, usage, search, notes, file mentions, history paging and the review panel call the API with the generated client. Step 2 is done: `ItemSummary.section`, `SendMessage` starting an item without a running place (with `StartOptions.images`), `ControlStatus` as a detail on control refusals, vacant control for `AbortTurn` and `UpdateAgentSettings`, `ReleaseControlRequest.item_id`, and a failed start as an agent problem until the next start. Step 3 is done: every action is a direct call, with `awaitJob` following a job over `WatchJob`; creation receipts are gone, since the daemon shows an item starting, or its failed start, itself. Step 4 is done: `EventService.Watch` carries changes, jobs, live content and heartbeats, and the daemon observes while anyone watches; the page reads the dashboard with `ListItems` and the conversation with `GetItemView`, applies live content from the stream, and `awaitJob` follows jobs on it. The SSE stream, `content_feed.go`, the list and chat routes and the snapshot arbitration are gone. Step 5 is done: item settings and profile models are read with `GetAgentSettings` and `GetProfileModels`, `wi web` serves only attachments and checkout documents besides the page and the proxy, `api.ts` became `views.ts` (view models only), and "Web exposure" in `docs/design/api/README.md` replaces the backend mapping. The page renews its control lease with `RenewControl` every 15 seconds, and an `UNIMPLEMENTED` stream says the daemon is older than the page. The label filter is a page preference (Settings → General, kept in the browser), and closing asks from `LifecycleService.PlanClose`, the daemon's dry run, instead of guessing from a list row.

## Recommendation in short

Continue to a thin proxy and a generated client, and finish it. The page calls `wi.core.v1` directly; `wi web` stays a separate process that only does what a browser needs (admission, static files, URL-addressable media); the daemon gains the few behaviors that today exist only in the adapter and that other clients need too.

Three rules decide where each piece of client-facing behavior goes:

1. If two clients could disagree about it and the disagreement would be a bug, it lives in the daemon, as API. Examples: which dashboard section an item is in, whether a message starts an item, whether a start failed.
2. If it exists because the client is a browser, it lives in `wi web`. Examples: Origin checks, CSP, serving a PDF at a URL, the six-connection limit.
3. Everything else lives in the page: shapes, wording, confirmation dialogs, the order of interactive steps.

The adapter's own HTTP API, its Go types and their TypeScript mirrors are deleted. Seven daemon additions make that possible; the most important is one multiplexed watch stream (`EventService.Watch`), because a browser tab on plain HTTP can afford only one long-lived connection.

The rest of this document gives the evidence, the alternatives and the ordered plan.

## Where we are

### Sizes

| What | Size |
| --- | --- |
| Adapter Go (`internal/web`, `internal/cli/web_*.go`, `internal/apiview`) | about 3,900 lines, plus 1,800 lines of tests |
| Adapter HTTP routes | 71 (`server.go` 47, `review.go` 20, `schedules.go` 4) |
| `Backend` interface (`internal/web/server.go:274`, with `ReviewBackend` and `ScheduleBackend`) | 74 methods |
| Hand-written exported TypeScript types outside `web/src/gen` | 103, of which 42 in `web/src/lib/api.ts` |
| Page files importing `lib/api` | 48, with 66 call sites of `api()`, `itemAction()` or `fetch()` |
| Generated TypeScript (`web/src/gen`) | 12,000 lines, used so far by `lib/rpc.ts` and `settings/agent-settings.tsx` |

### What the adapter adds beyond translation

I checked the list in the task against the code and completed it. Most of the 74 backend methods are one API call and a reshape. The rest:

| # | Addition | Where | What it really does today |
| --- | --- | --- | --- |
| 1 | Event stream `/api/events` | `internal/web/events.go`, `content_feed.go` | One SSE response per visible tab carrying five kinds of frame (list, creations, chat, live snapshot, content events). `listHub` holds one `WatchChanges` subscription for all tabs, re-reads the list once per change, and drops identical lists. `contentFeeds` holds one `WatchLiveContent` subscription per item and keeps events since the last snapshot (2,048 events or 4 MiB) so a late tab can catch up. Keep-alive every 15 s, stream ends after 10 min, 5 s write deadline, 16 streams at most. |
| 2 | Observation beat | `events.go:94-128` | When 10 s pass without a change, the hub calls `ListItems` with `inspect: true` and tells every tab that everything may have changed. This is how the browser learns of changes that produce no event: worktrees, process exits, lease expiry. |
| 3 | Background starts | `internal/web/background.go`, `/api/creations` | Creation receipts kept in memory. `Create` already calls `CreateAndStartItem`, so the daemon job is running before a "worker" takes the receipt; `StartCreated` only looks the job up and watches it (`internal/cli/web_create.go:35-46`). The 4 workers bound watching, not starting; the 32 slots refuse a 33rd unfinished start. |
| 4 | Job waiting | `webBackend.job`, `web_backend.go:289` | Ten routes start a job, watch it to the end, re-read the item and answer one `Outcome` (result, error, warnings, observed item). This is why routes have 45 s and 10 min timeouts (`server.go:635-648`). |
| 5 | Start or send | `webBackend.Send`, `web_backend.go:309` | A backlog item, or one without a checkout claim, gets `StartItem` with the text as prompt; otherwise `SendMessage`. |
| 6 | View assembly | `web_backend.go`, `web_settings.go` | Dashboard sections from `attention_group` plus a problem rule; "next item"; the chat view from `GetItemView`; repositories with their files (N+1 calls); item settings merged with agent state. |
| 7 | Control sequences | `web_backend.go:340-377`, `web_settings.go:122` | Stop is `AcquireControl` then `AbortTurn`; release is `GetControl` then `ReleaseControl`; changing settings takes control first when the agent runs. |
| 8 | Admission | `server.go:558-652`, `connect_proxy.go` | Host, Origin, `Sec-Fetch-Site`, content type, identity header, tab identity, request and stream slots, per-route body limits and timeouts. |
| 9 | Browser media | `server.go:933-976`, `documents.go` | Attachments and checkout PDFs served at URLs, with rules about what may render inline (never HTML or SVG; a PDF must start with `%PDF-`), `Content-Disposition`, caching and range requests. |
| 10 | Client policy from the server's environment | `web_backend.go:84-93` | The list applies label rules from the operator's config and the environment `wi web` was started in. |
| 11 | Display data | `internal/web/remote.go` | Strips credentials from the upstream URL and derives the hosting page's URL. |
| 12 | Input validation | `server.go:782-1090` | Length and format checks before the daemon is called; most repeat the daemon's own. |

### Drift found while reading

These are not fixed here (the task is read-only). They are the same kind of bug that motivated the move, and each one disappears under the recommendation.

- Takeover on send is unreachable. The page asks to take control when a refused action carries `outcome.control` (`web/src/lib/actions.ts:265`), but the adapter never sets `Outcome.Control` on an action; the daemon answers `FAILED_PRECONDITION "taking control requires confirmation"` (`internal/daemonapi/control.go:62`) and the page shows it as a plain error.
- After a takeover, `sendMessage` reads `taken.result?.runtime?.id` (`actions.ts:324`). The result is now an `AcquireControlResponse` or a `Job`, neither of which has that field, so the generation fence is always empty.
- Images sent to a backlog item are dropped: the start path passes only `message.Text` (`web_backend.go:314-316`), and `StartOptions` has no images.
- The content feed's polling fallback tests `errors.Is(err, driverprotocol.ErrContentStreamUnsupported)` (`content_feed.go:86`). The error now arrives as a Connect error, so the test cannot match.
- Sections are computed three times and differ. The browser checks the waiting group before the problem rule (`web_backend.go:66-82`); `wi list` and the picker check a checkout problem first (`internal/cli/list_render.go:188-246`). As written, a waiting item with a broken checkout is WAITING in the browser and NEEDS FIXING in the terminal.
- "Next item" ignores the label rules the list applies (`web_backend.go:153` against `:93`), so it can open an item the dashboard hides.
- Review routes send generated Go structs through `encoding/json` (`internal/web/server.go:1105`), a third wire shape next to protojson and the adapter's own, mirrored by hand in `web/src/lib/review.ts`.
- The daemon declares `Upstream.web_url` and `host` but never fills them, and returns the upstream URL unredacted (`internal/daemonapi/repositories.go:33`, `:58`). The adapter redacts; the proxy does not, so the page can now read credentials embedded in a remote URL.
- `WatchLiveContentRequest.after` is documented but not read by the handler (`internal/daemonapi/agents.go:640`); `WatchChangesResponse.repository_ids` is never set and `profiles_changed` is only set with `all` (`internal/daemonapi/views.go:32`).

## Alternatives

### A. Thin proxy and generated client (recommended)

The page uses the generated clients for everything that is an RPC. Cost: the daemon API must cover what the adapter composed, and the page takes over sequencing that used to be hidden behind one synchronous route.

For: one contract, one vocabulary, a compiler on both sides of it. A field is added in the proto and used in the page: two edits instead of four. Behavior that moves into the daemon is shared with the CLI, the popups and any later client. Against: the daemon grows client-facing surface, which needs a rule to stay small (the three rules above), and the page becomes sensitive to version skew with the daemon.

### B. A typed backend-for-frontend

Define web-specific services in proto (say `wi.web.v1`: `GetWorkList`, `GetChat`, `WatchTab`) and serve them from `wi web`.

For: no daemon changes; the fan-out stays where it is; web-shaped messages. Against: the mapping code stays, and the mapping code is where the bugs came from. Types being generated removes misspelled fields, not wrong behavior: seven of the nine findings above are behavior. A field still needs three edits (core proto, web proto, mapper). There is a second contract to version. A phone app or a TUI popup wanting the same view must go through `wi web` or reimplement it.

The useful part of this idea already has a home: `ViewService` is "what a client shows for one subject", served by the daemon and open to every client. Composite reads belong there, not in a second API.

Verdict: rejected as the general shape. `wi web` keeps two routes of its own, for media, which are not RPCs.

### C. The daemon serves the page itself

No `wi web` process: the daemon listens for browsers.

For: one process and one hop less; the page is there whenever the daemon is. Against:

- The daemon's trust boundary today is small and easy to audit: Unix sockets and a peer UID check (`internal/apitransport/transport.go`). Browser admission (Host, Origin, CSP, MIME rules, identity headers from a proxy) would move into the process that owns all state and every adapter.
- Exposure settings are per listener (`--access-url`, `--identity-header`, `--socket` or `--port`). Two exposures, say loopback and a Tailscale socket with an identity header, are two `wi web` processes today. In the daemon they become listener configuration and reload logic.
- Restarting the web server would mean restarting the daemon, which fails running jobs as interrupted.
- It contradicts the model in `AGENTS.md`: the browser adapter is a client, and clients use only the API.

Verdict: not now. After the migration `wi web` is a generic proxy with no domain logic, so hosting it in the daemon later is a small, reversible change if always-on access turns out to matter.

### D. Keep the hand-written adapter

Rejected by the motivation: four edits per field, and the findings above.

## Design

### What stays in `wi web`

About 600 lines instead of 3,900:

- The listener and its flags (`internal/cli/command_web.go`).
- The embedded client, CSP and response headers.
- Admission, written once. Today `admitAPI` and `serveConnect` duplicate it, and the Connect path skips the identity header's length and control-character check.
- The Connect proxy, with three additions: a deadline on writes to a stalled tab and a maximum stream lifetime (the SSE handler has both, the proxy has neither); an upper bound on unary calls; and refusals in Connect's error format, so the page shows "too many requests" instead of a bare status.
- Two media routes, attachments and checkout PDFs. A browser needs a URL for `<a href>`, `<img src>` and its PDF viewer, and the inline-rendering rules are browser security. They call `ItemService.ReadAttachment` and `WorkspaceService.GetWorkspaceDocument`.

Nothing else. In particular the page learns nothing from `wi web` itself: who the caller is comes from the API (`GetInfoResponse.caller`, which gains a `label`), the build version is compiled into the bundle, and the label filter becomes a page preference (see "Cloud hosting and remote agents" for why). Today the list applies the label rules of the config and environment `wi web` was started with; that stops, unless the daemon later exposes a typed default.

### What moves into the daemon

Each item names the proto change. All are additive within `v1`.

**1. Sections.** `items.proto`:

```proto
// ListSection is where the built-in work list shows an item.
enum ListSection {
  LIST_SECTION_UNSPECIFIED = 0;
  LIST_SECTION_NEEDS_ATTENTION = 1;
  LIST_SECTION_NEEDS_FIXING = 2;
  LIST_SECTION_IN_PROGRESS = 3;
  LIST_SECTION_WAITING = 4;
  LIST_SECTION_BACKLOG = 5;
  LIST_SECTION_CLOSED = 6;
}

message ItemSummary {
  // ...
  // section is set when the list is in attention order.
  ListSection section = 16;
}
```

A new field rather than a new `AttentionGroup` value, because existing clients switch on the group. `wi list`, the picker and the page all read it; `webSection`, `workDisplaySections` and the page's own reordering go. Names, colors and folding stay presentation. "Next item" needs no RPC: the page already holds the list in attention order and takes the first row with `needs_attention` among the selected repositories.

**2. One way to say something to an item.** `AgentService.SendMessage` accepts any item that is not closed. For a backlog item or one without a claim it does what `LifecycleService.StartItem` does with the message as prompt, and returns the job. `lifecycle.proto` gains `repeated Image images = 7` in `StartOptions` so the images survive. `require_online` remains for callers that want the strict form. The alternative is a new porcelain RPC; I prefer relaxing `SendMessage` because it already starts agents, and clients should not have to choose between two calls for one intention.

**3. Control refusals carry the plan.** Calls that need control (`SendMessage`, `AbortTurn`, `AnswerQuestion`, `UpdateAgentSettings`, `AcquireControl`) attach the current `ControlStatus` as an error detail when they refuse for lack of control, as `PartialOutcome` is attached today. No new message. The page's takeover flow works again, typed: read the detail, confirm, call `AcquireControl` with the `plan_token`, retry.

Two smaller changes remove the remaining control sequences: `AbortTurn` and `UpdateAgentSettings` take a vacant lease as `SendMessage` already does, and `ReleaseControlRequest` gains `string item_id = 2` to release the caller's lease on an item in one call (the page releases on tab close with a keep-alive request and cannot chain two).

**4. One watch stream.** `events.proto`:

```proto
service EventService {
  // Watch carries everything one client view follows on a single stream:
  // the change feed, the jobs, and one item's live content.
  rpc Watch(WatchRequest) returns (stream WatchResponse);
}

message WatchRequest {
  // after is a change-feed cursor, as in WatchChanges.
  string after = 1;
  // item_id also follows this item's live content.
  string item_id = 2;
  // content_after resumes live content; empty starts with a snapshot.
  string content_after = 3;
}

message WatchResponse {
  oneof event {
    WatchChangesResponse changes = 1;
    WatchLiveContentResponse content = 2;
    JobsChanged jobs = 3;
    Heartbeat heartbeat = 4;
  }
}

// JobsChanged reports jobs as they change. The first one lists every
// unfinished job.
message JobsChanged {
  repeated Job jobs = 1;
  bool snapshot = 2;
}

message Heartbeat {}
```

Why one stream and not `WatchChanges` plus `WatchLiveContent` plus `WatchJob` per tab: `wi web` on its default loopback port is plain HTTP, which browsers speak as HTTP/1.1 with six connections per origin, shared by all tabs. Two streams per visible tab would hang every call once three tabs are visible. Today's SSE design exists for this reason (`web/src/lib/events.ts`, "Hidden tabs release it"). The multiplexer must therefore exist somewhere, and in the daemon every client gets it.

Why heartbeats: Connect streams have no keep-alive of their own, and proxies such as Cloudflare close idle responses. Every 15 s, as the SSE stream does.

Why jobs on the stream: it replaces both the creation receipts and the synchronous job waiting (below) without a stream per action.

Reconnect and ordering: the page reconnects with its change cursor and gets either the changes since, or `all`. Live content restarts with a snapshot. Events of one kind arrive in order on one stream, so the page needs no cross-stream ordering. A client too slow to read is dropped and reconnects to a snapshot, which is how the content feed already treats a dropped reader (`content_feed.go:88-91`).

**5. The daemon observes while someone watches.** While at least one `Watch` or `WatchChanges` stream is open, the daemon re-observes agents and worktrees on the 10 s beat the hub drives today, and records changes only for items whose observation changed. Clients no longer pass `inspect: true` on a timer, and a quiet beat no longer makes every tab re-read everything.

**6. A failed start is item state.** Today only the browser's in-memory receipt says "this start failed", until `wi web` restarts. When the latest start job of an item failed and no agent runs, the daemon reports the agent as `AGENT_STATUS_PROBLEM` with the job's error as `agent_reason`, until the next start. The item then shows under NEEDS FIXING in every client. This has to be fitted into the single place that decides agent status (commit `324f00e`); I have not designed that part.

**7. Exposure and identity.** In `internal/daemonapi/server.go`, next to `agentAllowed`:

- `webAllowed(procedure)`, default deny, applied to the web-adapter listener. A test walks the service descriptors and fails when a procedure is in neither list, so exposing a new RPC to browsers is a reviewed line, not a side effect of adding it.
- `GetConfigRequest.show_sensitive` is refused for web callers.
- The lease label comes from a `Wi-Client-Label` header that `wi web` sets from the identity header, never from the page. `Caller` gains `string label = 3` so the page can show who it acts as.
- `Upstream.url` is returned without credentials, and `web_url` and `host` are filled in (move `internal/web/remote.go` into the daemon).

How much the allowlist protects today should be stated honestly. On one operator's machine it is not a boundary against a hostile page or user: the routes that already exist let the browser register a repository from a path on the machine, set a setup command with trust "always", edit checkout files and prompt agents, which is code execution as the operator. The allowlist reduces surface and keeps secrets and irreversible operations out of reach of a page bug: `Shutdown`, `DeleteItem`, `ArchiveRepository`, `SyncRepository` with reset, `ReadRepositoryFile`, `GetConfig` with secrets, `SetItemState` and the other plumbing the page never calls. It belongs in the daemon rather than the proxy because the listener's identity is assigned there and the agent endpoint is the precedent. Write it as a table of who may call what, not as a list of what the page happens to use: with remote sandboxes it becomes the real authorization boundary.

The proxy keeps admitting only `POST` with an Origin. The protos mark reads `NO_SIDE_EFFECTS`, which lets Connect serve them as `GET`, and a cross-site page can issue a `GET` without the Origin check applying; the proxy must keep refusing them.

**Later.** Move `contentFeed` (190 lines) into the daemon's `WatchLiveContent`: it gives one driver subscription per item whatever the number of watchers, and implements the `after` cursor the proto already promises. Locally it is not needed for correctness, since the driver's content socket accepts several subscribers; with agents on remote hosts it is, see below. Also optional: a dry run for closing, in the manner of `ControlPlan`, would do for `closeCheck` (`actions.ts:74`) what item 3 does for takeovers.

### What moves into the page

- **Types.** Generated messages are used directly. No hand-written type restates a proto message; a view model is a function over generated types. The API has no 64-bit integers, so there are no `bigint`s to handle.
- **Reads.** `ListItems` for the dashboard, `GetItemView` for a conversation, both re-read when the watch stream names the item, coalesced to a few per second during a turn. This is simpler than today: each query has one source, so the `read_at` and `noteStreamed` arbitration between stream snapshots and the page's own reads (`web/src/lib/queries.ts:66-118`) is deleted.
- **Jobs.** An action is a unary call that returns at once, with a `Job` when work continues. A helper, `awaitJob(job)`, resolves when the stream reports the job finished, and shows `phase` meanwhile. Nothing waits on an open request, so the long route timeouts go, and a reload loses nothing: the stream's first `JobsChanged` lists what is still running, whichever tab started it. If a bound on concurrent starts is still wanted, it belongs in the daemon's job runner, not in a client. `Outcome` is replaced by the job (`error`, `partial`, `warnings`) and by `ConnectError` with its details.
- **Idempotency.** Unchanged: the page generates `request_id` for creations and `client_message_id` for messages, and never retries other mutations by itself.
- **Timeouts.** Per call, with Connect's `timeoutMs`, which the daemon enforces.
- **Control.** The takeover dialog; renewing the lease with `RenewControl` on a timer while the tab is visible and holds it (the lease lasts 45 s, `app.WebLeaseTTL`). Today renewal is a side effect of re-reading the chat on every beat; I did not confirm that the daemon's `GetItemView` still renews, and an explicit call removes the question.
- **Small compositions.** Repositories with their files, the environment page, item settings. All are data the page has or can fetch in parallel.
- **Live content.** `web/src/lib/content.ts` applies the API's `WatchLiveContentResponse` events instead of the driver's.
- **Version skew.** The page is embedded in the `wi` binary that runs `wi web`, and the daemon may be an older build. At load the page calls `GetInfo` and compares versions; an `UNIMPLEMENTED` answer shows "restart the daemon" rather than a broken view. The additive rule for `v1` covers the opposite case.

### What is deleted

- `internal/web`: the 71 routes, the `Backend` interface, `Outcome`, the request and response types, `events.go`, `content_feed.go`, `background.go`, `review.go`, `schedules.go`, `settings.go`, `remote.go`, and the validators that repeat the daemon's.
- `internal/cli/web_*.go`, except the two media readers.
- `internal/apiview`: `Manifest`, `Runtime`, `Lease`, `Control`, `History`, `Content`, `ContentEvent`, `Settings`, `Attachment`, `Note`. The enum-to-text helpers the CLI renders with stay.
- `web/src/lib/api.ts` (types and `api()`), the SSE parser in `events.ts`, the mirrors in `review.ts`.
- About 1,500 of the 1,800 test lines, which test the adapter's contract with a fake backend.
- `webSection`, and `workDisplaySections`' own bucketing in the CLI.

## Consistency across clients

| Behavior | Today | After |
| --- | --- | --- |
| Attention order, `needs_attention` | Daemon | Daemon |
| Dashboard section | Daemon group, then CLI and adapter rules, then page reordering | Daemon (`ItemSummary.section`) |
| Start or send | Adapter; CLI has its own commands | Daemon (`SendMessage`) |
| Failed start | Browser memory only | Daemon (item observation) |
| Takeover plan and confirmation token | Daemon, but lost on refusal | Daemon, attached to the refusal |
| Job progress and outcome | Daemon; adapter flattens it | Daemon; clients read `Job` |
| Observation cadence | Whichever client polls | Daemon, while watched |
| Upstream URL redaction | Adapter | Daemon |
| Label filters | Client | Client, sent explicitly |
| Section names, colors, toasts, dialogs | Each client | Each client |

## Testing

Generated types move the question from "do both sides agree on the shape" to "does the page do the right thing with what the daemon really sends". Four layers:

1. **Contract fixtures written by Go, read by TypeScript**, following `agentdriver/protocol/testdata/content_events.json`. A daemon API test writes protojson of real handler output under `WI_UPDATE_FIXTURES`; page tests parse it with `fromJson` and the generated schema. The first one to build is the live-content fixture at API level: the stream messages and the `LiveContent` expected after each, which the page's reducer must reproduce.
2. **Typed fakes in page tests.** Connect's in-memory router transport lets a component test implement a service with generated types, so the compiler checks the fake. Today's tests mock `fetch` with hand-written JSON, which is how tests came to mirror a wrong contract.
3. **End to end through the proxy.** `cmd/wi/e2e_test.go` has seven calls to `/api/` routes. They become Connect calls through `wi web` with the generated Go client, which exercises admission, the allowlist and the daemon together.
4. **Exposure completeness**, the descriptor walk described above, and a proxy test per admission rule.

## Migration

Each step is shippable and deletes what it replaces in the same change. Sizes are relative: S is one small change, L is several.

| # | Step | Needs | Deletes | Size |
| --- | --- | --- | --- | --- |
| 0 | Harden the proxy and the exposure: one admission path, stream deadline and lifetime, unary bound, Connect-format refusals, `webAllowed` with its test, `Wi-Client-Label`, upstream redaction | nothing | `remote.go` | M |
| 1 | Leaf areas that are only translation: general, repository, environment, setup, schedule and usage settings; search; notes; file mentions; history paging; the whole review panel | step 0, `Caller.label` | about 45 routes, their backend methods and types, `review.go`, `schedules.go`, `settings.go`, three `web_review*.go` files | L, mechanical |
| 2 | Daemon behavior: `ItemSummary.section` (CLI and picker adopt it at once), `SendMessage` for backlog items with `StartOptions.images`, `ControlStatus` on refusals, vacant-lease rules, `ReleaseControl` by item, failed start as observation | nothing | `webSection`, CLI bucketing | M |
| 3 | Actions as direct calls: lifecycle, control, blockers, read marks, item settings, create. `awaitJob` uses `JobService.WatchJob` for now (short streams) | step 2 | `Outcome`, `webBackend.job`, `background.go`, `/api/creations`, the long timeouts | M |
| 4 | `EventService.Watch` with heartbeats, jobs and the daemon-side beat; the page reads the dashboard from it, then the conversation and live content; `awaitJob` switches to it | steps 2 and 3, the live-content fixture | `events.go`, `content_feed.go`, the SSE parser, the snapshot arbitration | L, the risky one |
| 5 | Remove what is left: `Backend` down to two media readers, `api.ts`, unused `apiview`, the adapter tests; rewrite the "Web backend mapping" table in `docs/design/api/README.md` as the exposure table (it is already stale: it says usage is not in the API) | all | the rest | S |

Order matters in two places. Step 0 comes first because the proxy already exposes the whole operator API and the unredacted upstream URL. Step 4 comes last because it is the only step where a mistake shows as lost or misordered output; by then every read and action already goes through the generated client, so the change is confined to how the page learns that something changed. During step 4 the dashboard can move before the conversation; a tab is briefly on two streams, which is acceptable for one release.

Step 1 can start immediately and in parallel with step 2.

## Cloud hosting and remote agents

The long-term plan is a hosted wi that dispatches agents to remote sandboxes or hosts. It does not change the recommendation; it is the strongest argument for it. It does change four details, already applied above, and it says which parts of this design are permanent and which are local conveniences.

### What it confirms

- **The page talks to the API and nothing else.** Hosted, the browser is the main client and the API server is reachable over HTTPS. The page's only assumption should be an origin that serves `wi.core.v1` over Connect. Every behavior left in `wi web` would have to be rebuilt in a cloud gateway.
- **`wi web` is the local stand-in for an edge.** Its three jobs (admission, static files, media) are what a load balancer, a CDN and a gateway do when hosted. Keeping it free of domain logic is what makes it replaceable. This also weakens the objection to alternative C: a hosted API server listens on the network and authenticates callers anyway.
- **No client-side state between the page and the daemon.** `listHub`, `contentFeeds` and the creation receipts are per-process memory. Behind more than one replica they would disagree; deleting them is required, not just tidy.
- **Jobs instead of open requests.** Provisioning a sandbox takes far longer than claiming a local worktree. A route that holds a request open until the agent is ready (today's `Outcome`) does not survive that; a call that returns a `Job` does.
- **The daemon observes; clients do not ask it to.** A client cannot cheaply trigger an inspection of a remote host. Remote hosts will push observations, and the change feed reports them (addition 5).
- **The change feed's contract already allows several instances.** A cursor from another instance answers `all` (`internal/coordinator/changes.go:100`), so the page's reconnect logic needs nothing new.
- **Rule 1 gets stronger.** More clients (a remote CLI, a phone) means more ways to disagree.

### What it changes

1. **Exposure is authorization.** Locally the browser is the operator and can already run code on the machine, so the `webAllowed` list only reduces surface. Hosted, code runs in sandboxes and several people may share an installation: who may call which method on which item is then the security boundary. Step 0 should build the table in that form (caller identity against procedure, default deny, in the daemon's interceptor), with today's three listener identities as its first rows. Remote agents calling back need the same table with item-scoped credentials, which `docs/design/api/README.md` already lists as future work.
2. **No bootstrap document.** An earlier draft had `wi web` serve the label rules, the user and the version to the page. Hosted, there is no "environment `wi web` was started in": these are the caller's identity and preferences. So the user comes from the API (`Caller` gains `string label = 3`, filled from what the trusted front asserts), and the label filter is a page preference.
3. **Media handlers are self-contained.** The two media routes take the generated client and produce HTTP, with no other dependency on `internal/web`, so the same handler can be mounted in a gateway or the API server, or replaced by signed URLs to object storage. The page builds media URLs in one helper.
4. **The daemon owns the live-content fan-out.** Locally, one driver subscription per watcher is harmless. Over a network link to a sandbox it is not, and clients on poor connections need the `after` cursor to resume. Moving `contentFeed` into the daemon moves from optional to planned, to be done with the first remote executor.

One justification weakens: `EventService.Watch` is forced locally by HTTP/1.1's six connections. Hosted, HTTPS gives HTTP/2 and the limit goes. The stream is still worth having (one resumable connection through proxies and on phones, heartbeats, jobs without a stream per action), but that argument is local.

### What the page should not come to depend on

The API has fields that describe one machine. They are fine to display; the page should not build behavior on them, because they will mean something else or nothing when the agent is elsewhere.

| Field | Local meaning | Page use today |
| --- | --- | --- |
| `Terminal.tmux_socket`, `session`, `window` | A tmux server on this machine | None; only `attached_clients`, for a warning |
| Control texts naming `wi switch --take` | A terminal on this machine | `web/src/lib/actions.ts:286` |
| `WorkspaceClaim.path` | A directory on this machine | Shown in the ticket sheet; `file-link.tsx` strips it from paths the agent prints, which stays valid if it is the path the agent sees |
| `GetInfoResponse.data_root` | The daemon's data directory | Shown in general settings |
| `AddRepositoryRequest.source` as a path | A directory on this machine | The add-repository form accepts it |
| `ListItemsRequest.inspect` | Re-observe local processes and worktrees now | Sent on a timer by the adapter; removed by addition 5 |

`Checkout.executor` ("where the checkout lives, such as local") is the existing hook for the rest.

### What it does not settle

Most of the work for a hosted wi is behind the API and outside this document: authentication and tenancy; the transport, which is Unix sockets with a peer UID check today; jobs that fail on a daemon restart instead of resuming; control leases held in memory; driver control and content sockets, which are local sockets; and conversations and descriptions, which are files on the daemon's disk. None of it changes where the web boundary sits. All of it is easier with one contract and no second place where behavior hides.

## Risks and open questions

- **The daemon API as a browser contract.** The three rules are the guard. A proposed daemon addition that only the page would use, and that could not cause two clients to disagree, belongs in the page.
- **Step 4's failure modes** are reconnect, ordering and slow readers. The fixture and an end-to-end test that kills and restarts the stream mid-turn should exist before the cutover.
- **Failed start as agent status** (item 6) touches the status decision and needs its own small design.
- **Whether `GetItemView` renews leases** needs checking before the page's renewal timer replaces it.
- **Label rules in the browser** become a page preference, so the config file's filter no longer applies there. If that default matters, the daemon should expose it as a typed field; a document served by `wi web` is the one answer the cloud plan rules out.
- **JSON or binary.** The page uses Connect JSON, which is readable in the browser's tools. Binary would shrink history pages; it is a one-line transport option and can wait for a measurement.
