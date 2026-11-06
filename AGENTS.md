`wi` is a CLI for short-lived development tasks, built on Git, Pi, and tmux.

## Task scope

Each work item should have one bounded objective and a clear completion condition. Review, integrate where appropriate, and close it when done. File unrelated work as a new item rather than turning an item or its conversation into a permanent project workspace.

Short-lived work has a durable record: restarting an agent, releasing a worktree, or closing the item does not erase its description, conversation, or history.

## Core model

- A work item owns a stable ID, title, description, lifecycle, labels, deep-work classification, repository reference, checkout specification, primary conversation identity, and optional agent settings that override its profile's driver configuration (model, reasoning) for every runtime of its conversation.
- Repositories are explicitly registered independent bare Git stores. Items retain the repository ID and immutable created-from commit. External onboarding checkouts are not operational dependencies.
- IDs and branch specifications are immutable. Active slugs stay fixed until close clears them; reopening allocates a new slug without renaming the branch. Titles and descriptions remain editable without implicit prompts.
- Drivers validate merged profile config through stateless `config.validate` before new runtime startup. Plain backlog capture is driver-independent; existing owners are not reconfigured.
- Lifecycle is `backlog|active|closed` and describes resources only; closing records a resolution (`completed|abandoned|superseded`). Typed blockers (`time|item|external`) are durable intent on backlog or active items; the coordinator clears time and item blockers, external ones are cleared explicitly. Deep work is a capacity-controlled classification, not a label.
- A workspace is a generation-fenced checkout claim. Managed slots are reusable wi-owned worktrees; release preserves the branch and warm slot. A workspace is not an agent process or terminal.
- Each item has at most one durable primary conversation and one active runtime owner. Successive runtimes may resume the conversation; drivers own its native format and content.
- TUI runtimes use tmux. Headless runtimes must remain tmux-independent. Terminal metadata is rebuildable access information, not item or conversation identity.
- Runtimes expose channels: observe (always), control (`structured`, or `emulated` keystrokes), and terminal (a native TUI in tmux). Drivers report them per runtime instance. One controller lease per item, kept in coordinator memory, decides which client may send input; any number of clients observe. Taking control may abort a turn and replace the runtime with one that has the channel the new controller needs.
- Agent status `busy|idle|problem` and worktree status `absent|clean|changed|problem` are separate observations. Attention state is core: activity facts, unread, needs-attention, defer, and ranking knobs (priority, pinned, snoozed-until). The built-in ranking is `ListItems` in attention order: waiting items (with an open blocker) stay out of the queue unless they have an unread response. Clients may rank differently from the same state. Attention never chooses an access mechanism.
- One daemon per data root runs every operation and owns all adapters (Git, tmux, processes, direnv, plugins, drivers). It serves the public API `wi.core.v1` (protobuf in `proto/`, served with Connect: Connect JSON, gRPC and gRPC-Web on its unix sockets). Its coordinator owns structured writes, revisions, compact append-only events, and projections in the private bbolt `wi.db`. Descriptions and native conversations remain canonical filesystem content outside that database.
- Clients (the CLI, the `wi web` browser adapter, other tools) use only the API through generated clients. They never run use cases or adapters, nor read wi's storage. Client-side only: argument parsing and selector resolution (with `ResolveItem`), default labels and label filters from config and environment (sent explicitly), contextual Git bases resolved read-only in the caller's own checkout, reading files and stdin (sent as content), the editor, fzf selection, attaching to tmux with the terminal target the daemon reports, rendering and exit codes, daemon start/stop, and plugin commands.
- The API has two levels, like Git. Plumbing is the domain services' small operations (item state, claim, commit and release of checkouts, start and stop of agents); porcelain is `LifecycleService` (create and start, start, shelve, close, reopen), each documented as the plumbing it performs. Plumbing enforces lifecycle invariants atomically, so no order of calls leaves an unsafe state. Fencing tokens (`expected_revision`, `expected_generation`, claim generations, plan tokens) fail stale requests with `ABORTED`; `FAILED_PRECONDITION` means the state does not allow the operation. Creation and messages take idempotency keys.
- Operations that can outlive a request run as daemon-owned jobs, with progress, approval pauses and cancellation. Job summaries persist; a job unfinished when the daemon stops ends as failed (interrupted) and stays queryable.
- The API speaks of agents: a durable conversation that is running or not, started, stopped and restarted, with a generation that changes on each start. Runtimes are the daemon's internal implementation of a running agent.
- Observations can become stale and must not replace durable intent. Runtime and terminal caches are rebuildable. Logs live under XDG state; sockets under XDG runtime.
- The operator endpoint serves the full API. The restricted agent endpoint allows safe backlog creation but rejects lifecycle, workspace, agent control, terminal, delete, and shutdown operations. The caller's access comes from the endpoint, never from request metadata; `GetInfo` reports it.
- Driver-owned runtime control sockets accept prompt, steer, follow-up, abort, and shutdown commands. Host-owned event sockets validate owner identity and relay compact semantic facts, never unrestricted native content.
- A partial outcome retains a successful durable step when a later resource operation fails. Report it rather than silently rolling it back.

## Code map

- `proto/wi/core/v1/`, `gen/wi/core/v1/`: the API contract and its generated Go messages and Connect clients/handlers. Regenerate with `scripts/generate-api` (see `docs/api-generation.md`); never edit `gen/` by hand.
- `internal/apitransport/`, `internal/socketpeer/`: the Connect transport over unix sockets and socket peer checks. `internal/daemonapi/`: the API handlers. `internal/daemonapp/`: daemon-side composition of application policy, drivers, plugins and adapters.
- `cmd/wi/`, `internal/cli/`: entrypoint, command parsing, client-side input, rendering, and generated-client calls. `internal/apiview/`: conversions from API messages to presentation types.
- `internal/web/`, `web/`: the `wi web` browser adapter over the API (HTTP API and event stream for the page) and its React client (Vite, embedded from `internal/web/dist/app`).
- `internal/app/`: use cases hosted by the daemon: service composition, cross-cutting `usecase_*.go`, merge integration, shared `contract/`, and read composition in `view/`.
- `internal/app/core/`: separate `item/`, `workspace/`, `primaryagent/`, and `attention/` policy. Preserve these dependency boundaries.
- `internal/coordinator/`: daemon, sole structured-state writer, bbolt events/projections, repository registry, and native-content indexing.
- `internal/model/`, `internal/store/`: record types and filesystem access. Live structured state belongs to the coordinator; drivers own native conversations.
- `agentdriver/`, `internal/agent/`, `internal/agentdriver/`, `skills/`: public driver contracts, runtime support, process host/clients, bundled Pi implementation, and embedded skills.
- `internal/git/`, `internal/tmux/`, `internal/direnv/`, `internal/process/`: system adapters. `internal/app/adapter/tmux/` separates terminal operations from interactive navigation.
- Support and tooling: configuration/path/locking/ranking/version/test helpers under `internal/`; offline conversion in `internal/migraterepositories/`; install/release and popup helpers in `scripts/`.

## Architectural constraints

- Keep durable-item primitives independent of workspace, agent, attention, and tmux operations.
- Workspace primitives must not create terminal sessions or agent conversations.
- Primary-agent core must not conceptually depend on tmux; only TUI composition may require it.
- Attention may rank observations but must not choose or enter an access mechanism.
- Low-level terminal operations must not select work or create an agent.
- Cross-cutting use cases may compose core areas and adapters. Lower layers must not invoke use cases.
- Preserve deliberate partial outcomes: for example, if creation or a lifecycle transition succeeds but later materialization fails, retain and report the durable result.
- Route structured mutations through the daemon and item locks. Keep compact event history append-only.
- Clients reach wi only through the generated API clients. Do not import use-case, adapter or storage packages from client code, except shared presentation and validation helpers.
- API contract changes go through `proto/`, regeneration and `buf lint`. Within `v1`, change additively: new fields and methods, never renumbered or retyped fields. Handlers stay thin translations to use cases.
- Never edit or move driver-owned native conversations manually.
- Treat absence of optional resources as normal state where appropriate. Do not emit repetitive warnings for an absent tmux server/session; reserve warnings for actionable inspection failures or inconsistent state.

Package-boundary tests enforce allowed imports. Child packages must not import the parent `internal/app` package.

## Protected documentation and agent instructions

- Do not edit `README.md`, `AGENTS.md`, or skill instruction files such as `skills/wi/SKILL.md` unless the user explicitly asks to change those files. This includes files named `SKILL.md` or `SKILLS.md`.
- A general request to update documentation does not authorize changes to these files. Make ordinary documentation updates under `docs/` instead.
- If a code change makes a protected file inaccurate, report the mismatch and ask before editing it. Do not rewrite, reorganize, or expand these files as incidental cleanup.

## Change workflow

Do not create Git commits unless the user explicitly asks for a commit. Leave completed changes in the working tree for review.

After a successful change that affects the `wi` executable, install the development build unless the user says not to:

   ```bash
   scripts/install-local
   ```

A dirty-build warning is expected before commit. The script builds the web client with `scripts/build-web` (Node.js and npm required), installs through `go install -buildvcs=true`, and prints the installed path and version. Use `scripts/install-local --clean` only when an exact clean-commit build is required.

If `~/.agentbox/tools/bin` exists, also update its sandbox copy after installing:

```bash
gobin=$(go env GOBIN)
if [ -z "$gobin" ]; then
    gopath=$(go env GOPATH)
    gobin=${gopath%%:*}/bin
fi
agentbox_tmp="$HOME/.agentbox/tools/bin/.wi.$$"
trap 'rm -f "$agentbox_tmp"' EXIT HUP INT TERM
cp -p "$gobin/wi" "$agentbox_tmp"
mv -f "$agentbox_tmp" "$HOME/.agentbox/tools/bin/wi"
trap - EXIT HUP INT TERM
```

Documentation-only and test-only changes do not require reinstalling the binary.

## Testing notes

- The full suite should run without requiring a live tmux server or coding-agent process.
- The web client has its own tests: `scripts/build-web --test` (or `npm test` in `web/`). Go tests must pass with or without a built client.
- Process-launching test packages use `testutil.RunIsolatedTests` in `TestMain` to isolate HOME, XDG roots, Pi configuration, Git overrides, and Wi/tmux/direnv context. Preserve this guard when adding tests. Go compiler/module caches may be shared; binary installation destinations must remain private.
- CLI subprocess tests must also use explicit isolated environments. Manual development-binary/browser smoke tests need their own HOME and XDG roots; do not run older binaries against the operator's live daemon.
- Test both TUI and headless composition when changing runtime startup or lifecycle behavior.
- CLI `--json` output is the API's JSON (protojson with proto field names): keep it consistent with the contract and preserve documented exit-code distinctions.
- `cmd/wi/e2e_test.go` drives the real binary end to end in an isolated environment with a fake driver; extend it when changing client or daemon behavior.
- A build with a changed daemon protocol cannot stop an older daemon: stop the running daemon with its own binary before installing, then start the new one.
- For persistence changes, test malformed input, interrupted/rebuildable state, locking, and path-escape safety where relevant.
