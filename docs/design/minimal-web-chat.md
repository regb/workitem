# Minimal web chat investigation

Status: initial investigation followed by a first runnable MVP. The proposal below records the intended design; [Local web chat](../src/content/docs/guides/web-chat.md) documents what the prototype actually implements.

Rebased onto main `d488742`, the MVP adds `wi web` using main's history paging, live snapshots, image-aware existing-owner controls, startup guards, and safe archive handling. New uses registered repositories and untitled creation, then edits only the display title and starts headless work in the background. Existing-chat navigation remains read-only. No core or driver extension remains. See [Main integration and naming decision](web-main-integration.md). The frontend uses NDJSON streaming. Authentication has since moved to the proxy in front of `wi web`; see the web chat guide. Lossless replay and a snapshot watermark remain future work.

## Recommendation

Add `wi web` as a loopback-only access server with embedded HTML, CSS, and JavaScript. Keep the existing daemon as the only structured-state writer. Use a small browser client, not a t3code fork. Establish driver content access and ownership behavior before building the chat.

Opening a chat is a read operation. Starting an agent is an explicit mutation. A browser tab never owns a conversation or creates another runtime identity.

The first interface has only:

- A work list with lifecycle, agent and worktree observations, and Archive on each row.
- A full-screen conversation with List, Next, Archive, owner/status text, history, and a composer.
- Inline refusal, warning, and partial-outcome messages.

No separate queue, project model, terminal emulator, settings page, or browser-side lifecycle policy.

## What already exists

| Need | Existing implementation | Consequence |
| --- | --- | --- |
| Durable state access | `internal/coordinator/client.go`, CLI `BindCoordinatorApp` and coordinator-backed store | Reuse daemon reads and commits; never open `wi.db` from the web server. |
| List observations | `internal/app/view/`, `internal/cli/coordinator_projection.go` | Reuse lifecycle and observations, including freshness and inspection warnings. |
| Next selection | `Client.ActionabilitySnapshot`, `coordinator.SelectActionability` | Send the browser's current item ID explicitly. Use daemon selection and configured priority. |
| Primary owner | `internal/app/core/primaryagent/` | Reuse verified process identity, claim generation, conversation locks, and capabilities. |
| Headless startup | `StartWorkItemInMode`, primary-agent runtime ensure, Pi RPC transport | A browser-accessed runtime can be headless without tmux. |
| Message control | `App.AgentControl`, public `ControlCommand` | Prompt, steer and follow-up already exist. No simulated keystrokes. |
| Safe archive | `internal/app/usecase_shelve_archive.go` | Invoke `Archive(..., false)`, never `item.state.set` to archived. |

Do not call `App.NextWorkItem` for web navigation. It is tmux porcelain and invokes `SwitchWorkItem`. `ActionabilitySnapshot` already separates selection from access, so no second ranking algorithm is needed.

The composition detail matters: today CLI use cases orchestrate adapters while `coordinatorStore` sends structured mutations to the daemon. The daemon protocol does not expose a complete archive or agent-control use case. The web server should reuse that composition through a shared host-side service, rather than assume such daemon methods already exist or copy CLI policy into HTTP handlers. Extract only the wiring needed by both entry points, respecting package-boundary tests.

## t3code assessment

Source reviewed: https://github.com/pingdotgg/t3code at commit `c85dc642681a944e644cbbbea7257d274822c21a`, fetched during this investigation. This was a source review, not an interactive usability test.

The root license is MIT, copyright 2026 T3 Tools Inc. Copying substantial code requires retaining its copyright and permission notice. A reused component would also need a dependency and third-party-license review. No t3code code or assets were copied here.

Its chat layout is a useful reference: dedicated conversation space, a persistent composer, and an activity timeline. Keep those ideas, but leave out its project/thread management, provider setup, previews, diffs, terminals, remote environments, and desktop integration.

Direct reuse is expensive at the reviewed revision:

- The web package uses React 19, TanStack Router, Effect atoms, Zustand, Tiptap, Tailwind, and multiple workspace contracts/provider/client-runtime packages.
- `MessagesTimeline.tsx` imports application state, thread workflows, provider contracts, file/diff previews, and work-log presentation. It is not a drop-in message list.
- `ChatView.tsx`, `ChatMarkdown.tsx`, `MessagesTimeline.tsx`, and `ChatComposer.tsx` total about 29,000 lines. This is evidence of coupling and scope, not a bundle-size measurement.
- The server has its own orchestration and providers, plus dependencies such as Node PTY and native keyring support. Reusing it would bring in runtime/state machinery Wi must not adopt.
- The root development environment requires Node 24 and pnpm 11. A tiny embedded client avoids a new production Node server and frontend build toolchain for the prototype.

Recommendation: independently implement the small layout. Reassess isolated rendering code only if plain text and code-block presentation prove inadequate. Do not start from a fork.

## Ownership and interaction model

| Current condition | Opening chat | Explicit agent action |
| --- | --- | --- |
| Live headless owner | Read history and subscribe to that owner | Submit to the same runtime through existing control. |
| Live TUI owner | Same behavior; show that the conversation is also open in the TUI | Submit only supported controls to the same owner. Do not restart it in headless mode. |
| No live owner | Read history, or show no conversation yet | Offer Start for backlog, Resume for waiting, or Start headless agent for working. Reuse existing lifecycle/capacity/workspace/trust checks. |
| Unverified/problem owner | Show history and a diagnostic | Refuse startup/control until existing ownership recovery resolves it. |
| Archived item | Read-only history | No implicit restoration or agent start. |
| Driver lacks content/control capability | Explain which operation is unavailable | Disable that operation; do not replace it with direct native-file access. |

Closing a tab or the access server leaves the owner running. Multiple tabs observe the same conversation. Navigating Next neither starts work nor modifies lifecycle. An empty attention queue is a normal result; wrapping and current-not-in-queue behavior follow daemon selection.

The initial composer offers Send while idle and Queue follow-up while busy. Do not label busy-time `prompt` as a follow-up: both Pi modes currently translate it into steering. Recheck status on submission, and make the selected delivery mode explicit. An idle-to-busy race may change prompt delivery to steering under the existing driver contract; document this or add a driver-side idle-only precondition if that behavior is unacceptable. Do not abort a turn to send a message.

A control acknowledgement is submission/transport acceptance, not proof of a completed answer. Keep the draft on rejection. On a lost response, show delivery unknown and reconcile with native history; do not retry automatically. Existing `RequestID` is correlation, not an end-to-end exactly-once guarantee, and `AgentControl` allocates a new command ID per invocation.

## Missing driver APIs

### Readable history

The public `conversation.observe` and `conversation.decode` operations only return compact facts. `ConversationFact` has no message text. The daemon conversation index therefore cannot produce a readable transcript.

Add a capability-negotiated, read-only `conversation.history` operation to `agentdriver/protocol/`. The host resolves the item's authoritative driver, conversation and native locator. The browser supplies only item ID and an opaque paging cursor, never a filesystem path.

Return bounded pages of normalized entries with stable native entry IDs, roles, text/code blocks, tool summaries, timestamps, a current branch/leaf identity, and the next cursor. Pi's driver must select the active native branch, not flatten every JSONL record into chat. Unsupported images, thinking blocks, and other content should have explicit placeholders. Keep content out of daemon compact events and projections.

Use the driver's supported conversation facilities where available; any Pi-specific reading or parsing belongs inside the driver, not generic app code or the frontend. Define behavior for concurrent append, incomplete records, compaction, branch changes, malformed content, and history reset. Reading must not acquire write ownership or start another agent session.

### Live content

Public `driverprotocol.RuntimeEvent` deliberately contains no text. Pi's private `agent.RuntimeEvent` has text and backend payloads, but the live public relay strips those fields. `Client.RuntimeEvents` exposes retained semantic facts, not a transcript stream. Polling that API cannot display token output.

Add a separate, driver-owned, user-only content subscription socket with a negotiated capability and versioned protocol. Both Pi modes already receive message updates; publish normalized message start/delta/completion and tool summaries there. Do not widen the retained runtime-event protocol to store prompts or deltas in `wi.db`, and do not tail private diagnostic logs as the browser API.

The host validates the current owner and stream identity before exposing content. Use bounded per-subscriber buffers and sequence numbers. A slow or disconnected browser must not block an agent turn. Overflow or owner replacement produces a reset/gap signal, then reload history. Late events from a superseded runtime must be discarded.

Subscribe before taking the history snapshot, buffer events during that read, and merge using stable message IDs plus a defined snapshot watermark. This needs an actual protocol guarantee; timestamps alone cannot prevent duplicate or missing messages. Final native history remains authoritative after reconnect. Old running drivers can return content unavailable until explicitly restarted after a turn, not silently rebound.

## Small backend shape

Use a host-side service with narrow list, next, history, subscribe, control, headless-start and archive methods. Inject it into `internal/web/`; HTTP code must not own domain policy. Reuse a coordinator-backed App, never a bare filesystem Store for mutations.

Proposed routes, not existing endpoints:

| Route | Behavior |
| --- | --- |
| `GET /api/items` | Existing work-list projection and observation warnings. |
| `GET /api/next?current=<id>` | Daemon actionability snapshot and selected ID. |
| `GET /api/items/<id>/history?cursor=...` | Driver-normalized native history. |
| `GET /api/items/<id>/stream` | Authenticated SSE bridge to driver content subscription. |
| `POST /api/items/<id>/agent/start` | Explicit lifecycle-aware headless startup through existing use cases. |
| `POST /api/items/<id>/messages` | Capability-checked public driver control with delivery mode. |
| `POST /api/items/<id>/archive` | Existing safe archive workflow, force always false initially. |

Item mutation/start/archive operations need shared item locking, not just an HTTP-process mutex. Recheck revision, owner and workspace claim under that coordination. Keep HTTP requests out of inherited tmux access context; browser archive is not "close the current pane" navigation. Cleanup of a pre-existing TUI owner's resources may still involve existing terminal adapters. A headless-only item must neither create nor inspect tmux resources.

### Archive reporting gap

Existing archive checks worktree safety before runtime shutdown and refuses a busy agent without force. That behavior must remain.

However, failures after shutdown or checkout release can return an empty `StateTransitionResult`. For example, `prepareArchive` drops shutdown warnings on error, and `finishArchive` returns an empty result if workspace closure or manifest saving fails. HTTP must not report these as "nothing changed".

Add typed archive progress/partial outcomes at the use-case layer, preserving CLI compatibility. Report shutdown requested/confirmed, terminal cleanup, claim release, lifecycle committed, warnings, and the failed step. Re-read durable state after an ambiguous failure, but do not treat a later snapshot alone as proof of which external steps succeeded. Leave retained successes in place. An archived item with cleanup warnings is distinct from an item whose agent stopped but lifecycle remains working.

Do not offer Force in the first prototype. If added later, it requires separate explicit authorization describing the busy-turn interruption and any retained resources.

## Browser security boundary

A loopback page can control a local coding agent with the user's authority. Local-only binding does not stop hostile websites from making requests or DNS-rebinding attacks.

- Bind explicitly to `127.0.0.1` on an ephemeral port. No wildcard bind, remote serving, or CORS support.
- Require the exact expected Host including port, and reject cross-origin mutations and cross-site requests. Validate Origin where present and use Fetch Metadata as an additional check.
- Generate a per-server random access secret. A launch URL may carry it in the fragment, never query parameters or logs. Exchange it for a short-lived HttpOnly, SameSite=Strict session cookie; clear the fragment immediately. Require authentication for history, streams and mutations, not just writes.
- Require a session-bound CSRF token and JSON content type for mutations. No state changes on GET. Bound request size, message size, pages, stream buffers and timeouts.
- Serve only embedded static assets and selected driver content. No arbitrary path/file API. Render untrusted text with `textContent`; if Markdown comes later, sanitize it and disable raw HTML.
- Set CSP, no-store for content, no-referrer, nosniff, and frame-ancestors none. Do not load scripts, fonts or message assets from third-party hosts.
- The server is trusted local operator software, not a restricted agent endpoint. Never expose the operator Unix socket or daemon protocol directly to the browser. Do not let an agent endpoint be promoted to web operator access.

No defense against a compromised browser profile or another process already acting as the same Unix user is claimed. Public hosting, remote tools, active-active runtime owners and live runtime migration remain out of scope.

## Prototype sequence and tests

1. Add fake-backed host service and a small embedded list/chat interface. Exercise navigation and outcome presentation without native processes. A fixture-only demo is useful but does not satisfy the final runnable native-chat outcome.
2. Add history and content-subscription driver contracts with Pi implementations and conformance tests. Prove snapshot/stream reconciliation before wiring real chat.
3. Share daemon-backed host composition, add `wi web`, explicit headless startup and safe archive reporting. Avoid unrelated CLI refactoring.
4. Run the prototype against an isolated data root and test it in a browser. Never use this investigation's live primary conversation as a destructive archive test.

Required checks:

- List to chat, browser back, refresh/deep link, empty history, scrolling, and Next with empty/current-missing/wrapped queues. Confirm list and next match Wi policy.
- Streaming before/during/after history reads, Unicode and multiline deltas, completion replacement, tool output, slow consumers, disconnect/reconnect, branch reset and owner replacement.
- Idle send, busy follow-up, unsupported control, rejection, lost acknowledgement, concurrent tabs and no automatic duplicate retry.
- Archive dirty-worktree refusal before shutdown, busy refusal without abort, clean success, shutdown timeout, terminal cleanup failure, released claim followed by lifecycle-save failure, and clearly displayed retained outcomes.
- Existing idle/busy TUI and headless owners, unverified identity, stale workspace claim, ownerless startup races and archived read-only history. Assert no new owner on open/navigation and no tmux calls for headless-only work.
- Host/Origin/CSRF/auth rejection, unauthenticated SSE/history, malicious message HTML, path injection, oversized requests and bounded stream buffering.
- Existing package-boundary, public driver, CLI JSON/exit-code and TUI/headless tests remain green.

## Validation completed in the initial investigation

Source inspection and targeted baseline tests only. The following passed:

```sh
go test ./agentdriver/protocol ./internal/agentdriver/pi ./internal/coordinator ./internal/cli ./internal/app/...
```

At the initial investigation stage, no executable code had changed and no browser tests had run.

## MVP validation

The first implementation passes `go test -short ./...`, race tests across `internal/...` and `agentdriver/...`, and `go vet ./...`. New tests cover driver history branch selection and bounds, snapshot reconnects and owner checks, real embedded TUI bridge behavior with a fake extension context, idle-only control refusal, archive timeout/partial cleanup, headless environment isolation and archive without tmux, and HTTP auth/origin/body validation.

An isolated data-root browser smoke test used the bundled Pi driver with a fake native RPC executable, not a paid model or an existing user conversation. It exercised list to chat, live previews, message delivery, final native history, Next, browser back/forward, busy and dirty archive refusal, clean archive and warnings, and malicious HTML rendered as text. No browser errors were reported. Test runtimes, the access server and the isolated daemon were stopped afterward.

The full `go test ./...` run is blocked by the existing `cmd/wi` real-binary version assertion. Binaries built in this managed checkout omit Git revision metadata, including with `-buildvcs=true`. All other packages passed. Paging, lossless stream/history reconciliation and broader content rendering remain unresolved.
