`wi` is a CLI for durable AI-assisted development work items. It builds on `git`, `pi`, and `tmux`.

## Important concepts:

- A work item is the canonical durable aggregate: identity, description, lifecycle, labels, deep-work classification, checkout specification, conversation identity, and append-only history.
- Lifecycle states are `backlog`, `working`, `waiting`, and `archived`.
- A workspace is a Git checkout claim/materialization, not a tmux session or coding-agent process.
- Checkout kinds are reusable `managed-slot` worktrees, exclusive borrowed `repository-home` claims, and per-item Docker or SSH `executor` clones.
- Each item has at most one durable primary agent conversation, created when agent work starts, and at most one active runtime owner.
- Wi is local-first: one host owns a data root, daemon, native content, runtime processes, and user-facing tmux. That host may be a laptop or an always-on server reached through SSH.
- TUI runtimes use tmux on the Wi host; headless runtimes must not require host tmux. Executors require their own private tmux holder for jobs and shells.
- Agent status (`busy`, `idle`, `problem`) and worktree status (`absent`, `clean`, `changed`, `problem`) are separate observations.
- The daemon's private `wi.db` is canonical for domain state, revisions, compact events, and read projections. Native agent conversations and descriptions remain canonical filesystem content. Runtime and terminal metadata are rebuildable caches; diagnostic logs live under XDG state and sockets under the XDG runtime directory.

## Glossary

- `work item`: The stable durable aggregate. It owns identity, description, lifecycle, labels, deep-work classification, source context, checkout specification and claim state, primary conversation identity, and append-only history. It may exist without a materialized workspace, live runtime, or terminal.
- `source context`: The repository identity and created-from commit recorded when the item is created. In the current schema this includes the repository root at creation, Git common directory, sanitized origin URL, and `created_from_commit`. It remains useful provenance after refs and the implementation branch advance.
- `checkout specification`: Durable instructions for materializing work: checkout kind, optional executor profile, and implementation branch. The immutable created-from commit belongs to source context, not checkout state. The specification may exist without a workspace claim.
- `checkout kind`: The workspace policy. `managed-slot` uses a reusable wi-owned worktree slot. `repository-home` borrows the repository's primary checkout through an exclusive logical claim and never makes that external checkout wi-owned. `executor` owns an independent clone in a per-item Docker container or dedicated directory on an existing SSH host.
- `workspace claim`: The durable identity of one materialized checkout assignment: claim ID, monotonic generation, repository key, path, and either a local slot or an executor identity. An executor claim is reserved before provisioning and records readiness and release progress. It is absent before materialization and cleared on release, while the latest generation remains durable.
- `materialization`: Creation or attachment of external resources described by durable state, such as assigning a managed slot, checking out the implementation branch, or recognizing a claimed repository home.
- `worktree`: A concrete Git working tree observed by wi. Managed worktrees may remain as warm reusable slot directories after an item releases its workspace claim.
- `managed slot`: A wi-owned reusable local worktree location. Slots are claimed by items, reset and validated before reuse, and retained to preserve build caches.
- `executor`: Configured execution placement using Docker or SSH. Each item gets an independent workspace and private tmux holder there. Docker provisions an owned container; SSH allocates only a claim directory on an existing host. Pi, conversations, runtime ownership, and user-facing tmux remain on the Wi host. No worker daemon or uploaded Wi binary is required.
- `executor profile`: Named Docker image/network or SSH host/root settings under `[executors.<name>]`. Claims pin these settings at first provisioning.
- `repository-home claim`: An exclusive logical claim on the repository's primary checkout. Releasing or deleting the item relinquishes the claim but does not modify or delete that checkout.
- `lifecycle`: The durable work-item state: `backlog`, `working`, `waiting`, or `archived`. Lifecycle does not by itself describe whether a workspace, runtime, or terminal currently exists.
- `deep work`: A durable item classification subject to configured active-capacity policy. It is not a label.
- `primary conversation`: The item's durable driver-owned conversation identity and native history. It is created when agent work first starts and survives runtime restarts. An item has at most one primary conversation.
- `runtime`: One live or recorded coding-agent process instance that owns the primary conversation for a period. Successive runtimes may resume the same conversation; a runtime is not the conversation itself.
- `runtime owner`: The sole runtime currently authorized to write the primary conversation and affect runtime-activity projections. Ownership includes runtime ID, workspace claim ID and generation, and verified process identity. Events from superseded runtime IDs are rejected.
- `runtime mode`: `tui` runs a driver's interactive interface and is composed with tmux. `headless` runs headlessly and must not depend on tmux.
- `runtime activity`: Compact semantic facts reported by the current runtime, such as ready, turn started, tool started, and settled. Activity drives busy/idle observation but does not contain unrestricted prompts, responses, deltas, tool arguments, paths, or errors.
- `agent status`: The derived `busy|idle|problem` observation for the primary agent. It is separate from lifecycle and worktree status.
- `worktree status`: The derived `absent|clean|changed|problem` observation for the checkout. It is separate from agent status.
- `terminal`: Optional access metadata for an interactive runtime, currently a wi-owned tmux session and panes. Terminal identity is rebuildable and is not durable conversation or runtime identity.
- `access mechanism`: How a user enters or observes work, currently local tmux attachment or switching. Access code may enter an already selected resource but must not become work-item, conversation, or runtime identity.
- `attention`: Policy that selects and ranks actionable working items from lifecycle, activity, defer facts, agent status, and worktree status. Attention does not choose or invoke an access mechanism.
- `NEEDS ATTENTION`: A view bucket for working items whose primary agent is idle and whose agent/worktree observations do not report a problem. It is derived, not a lifecycle state.
- `projection`: A daemon-maintained read model derived from canonical commands, events, native content, or external observations. Projections live in `wi.db`; some external-resource projections are rebuildable.
- `observation`: A timestamped statement about an external resource such as a process, agent conversation, worktree, or tmux session. Observations can become stale and must not silently replace durable intent.
- `event`: A compact append-only domain or runtime fact with stable identity and ordering. Events in `wi.db` exclude unrestricted native content.
- `coordinator daemon`: The sole structured-state writer and projection owner for one data root. It validates commands, commits events and projections, coordinates native writes, and ingests compact runtime events.
- `data root`: The canonical per-installation durable root, normally `$XDG_DATA_HOME/wi`. Each distinct absolute data root has its own `wi.db`, daemon, and deterministic runtime socket namespace.
- `operator endpoint`: The user-only daemon Unix socket with the full local API. Normal CLI commands and host-side driver bridges use it.
- `agent endpoint`: The separately mountable restricted daemon Unix socket for sandboxed tools. It permits health/minimal creation reads and safe absent managed-slot backlog creation, but rejects lifecycle, workspace, runtime, terminal, delete, and shutdown control.
- `runtime control socket`: A driver-owned per-runtime Unix socket used for direct prompt, steer, follow-up, abort, and shutdown commands. It speaks the public driver protocol and is not canonical state.
- `runtime event socket`: A host-owned per-runtime Unix socket that accepts compact public driver events. The runtime host validates identity and relays accepted events through the private daemon API.
- `native content`: Canonical filesystem content intentionally kept out of `wi.db`, currently descriptions and driver-owned native conversations.
- `adapter`: Code that talks to an external system such as Git, coding-agent drivers, tmux, direnv, Unix processes, or the filesystem. Adapters implement narrow contracts and do not own domain policy.
- `use case`: Cross-cutting application orchestration such as start, resume, switch, shelve, archive, delete, merge, or shutdown. Use cases compose core policy and adapters; lower layers do not invoke them.
- `partial outcome`: A deliberately retained successful durable step followed by a later failure, for example an item created before workspace startup fails. wi reports the retained result instead of rolling it back or deleting it implicitly.

## Code map

- `cmd/wi/main.go`: minimal executable entrypoint.
- `internal/cli/`: command parsing, help, text/JSON output, and exit-code mapping.
- `internal/app/app.go`: application service composition.
- `internal/app/core/item/`: durable item lifecycle and metadata.
- `internal/app/core/workspace/`: checkout slots, repository-home claims, ensure/release policy.
- `internal/app/core/primaryagent/`: conversation, runtime ownership, observation, and control.
- `internal/app/core/attention/`: activity facts and attention policy.
- `internal/app/view/`: list/read projections.
- `internal/app/adapter/tmux/`: low-level terminal access and interactive navigation porcelain.
- `internal/app/usecase_*.go`: cross-cutting start/resume/shelve/archive/delete/shutdown orchestration.
- `internal/app/integration_merge.go`: transactional rebase/fast-forward integration and rollback.
- `internal/model/`: schema and durable/rebuildable records.
- `internal/dataroot/`, `internal/runtimepath/`: stable per-root keys and bounded operational paths.
- `internal/store/`: native description and conversation filesystem access used by application services and tests.
- `agentdriver/protocol/`: public process protocol for coding-agent drivers.
- `internal/agent/`: driver-neutral runtime protocol.
- `internal/agentdriver/`: driver process host and clients; bundled driver implementations live in child packages such as `internal/agentdriver/pi/`.
- `internal/git/`, `internal/tmux/`, `internal/direnv/`, `internal/process/`: external-system adapters.
- `docs/internal/remote-agent-placement.md`: local-first deployment, hosted-workstation access, remote workspace tools, and future availability boundaries.
- `internal/executor/`: shared Docker/SSH environment interface, transports, embedded operation scripts, jobs, and Git bundle helpers.
- `internal/workspacetool/`: per-runtime streaming bridge for executor tools.
- `internal/app/executor*.go`, `internal/app/integration_executor.go`: executor orchestration and integration.
- `docs/internal/remote-execution.md`: current Docker/SSH executor design and limits.

## Architectural constraints

- Keep durable-item primitives independent of workspace, agent, attention, and tmux operations.
- Workspace primitives must not create terminal sessions or agent conversations.
- Primary-agent core must not conceptually depend on tmux; only TUI composition may require it.
- The Wi host remains canonical for remotely placed workspaces. Executors own replaceable Git materializations and claim-fenced tool processes, not work-item, conversation, or runtime authority.
- Remote workspace operations must carry and validate the current claim identity and fail closed without local filesystem fallback.
- Treat SSH and future web clients as access methods. They must not become work-item, conversation, workspace, or runtime identity.
- Attention may rank observations but must not choose or enter an access mechanism.
- Low-level terminal operations must not select work or create an agent.
- Cross-cutting use cases may compose core areas and adapters. Lower layers must not invoke use cases.
- Preserve deliberate partial outcomes: for example, if creation or a lifecycle transition succeeds but later materialization fails, retain and report the durable result.
- Route structured mutations through the daemon and item locks. Keep compact event history append-only.
- Never edit or move driver-owned native conversations manually.
- Treat absence of optional resources as normal state where appropriate. Do not emit repetitive warnings for an absent tmux server/session; reserve warnings for actionable inspection failures or inconsistent state.

Package-boundary tests enforce allowed imports. Child packages must not import the parent `internal/app` package.

## Change workflow

Do not create Git commits unless the user explicitly asks for a commit. Leave completed changes in the working tree for review.

After a successful change that affects the `wi` executable, install the development build unless the user says not to:

   ```bash
   scripts/install-local
   ```

A dirty-build warning is expected before commit. The script installs through `go install -buildvcs=true` and prints the installed path and version. Use `scripts/install-local --clean` only when an exact clean-commit build is required.

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
- CLI integration tests should isolate HOME and XDG roots.
- Test both TUI and headless composition when changing runtime startup or lifecycle behavior.
- Preserve stable JSON shapes and documented exit-code distinctions for CLI changes.
- For persistence changes, test malformed input, interrupted/rebuildable state, locking, and path-escape safety where relevant.
