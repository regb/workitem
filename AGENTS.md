`wi` is a CLI for durable AI-assisted development work items. It builds on `git`, `pi`, and `tmux`.

## Important concepts:

- A work item is the canonical durable aggregate: identity, description, lifecycle, labels, deep-work classification, checkout specification, conversation identity, and append-only history.
- Lifecycle states are `backlog`, `working`, `waiting`, and `archived`.
- A workspace is a Git checkout claim/materialization, not a tmux session or Pi process.
- Checkout kinds are reusable `managed-slot` worktrees and exclusive borrowed `repository-home` claims.
- Each item has at most one durable primary Pi conversation, created when agent work starts, and at most one active runtime owner.
- TUI runtimes use tmux; RPC runtimes are headless and must remain tmux-independent.
- Agent status (`busy`, `idle`, `problem`) and worktree status (`absent`, `clean`, `changed`, `problem`) are separate observations.
- The daemon's private `wi.db` is canonical for domain state, revisions, compact events, and read projections. Native Pi sessions and descriptions remain canonical filesystem content. Runtime and terminal metadata are rebuildable caches; diagnostic logs live under XDG state and sockets under the XDG runtime directory.

## Glossary

- `work item`: The stable durable aggregate. It owns identity, description, lifecycle, labels, deep-work classification, source context, checkout specification and claim state, primary conversation identity, and append-only history. It may exist without a materialized workspace, live runtime, or terminal.
- `source context`: The repository identity and created-from commit recorded when the item is created. In the current schema this includes the repository root at creation, Git common directory, sanitized origin URL, and `created_from_commit`. It remains useful provenance after refs and the implementation branch advance.
- `checkout specification`: The durable instructions and current claim for materializing work: checkout kind, implementation branch, and assigned path when present. The immutable created-from commit belongs to source context, not checkout state.
- `checkout kind`: The workspace policy. `managed-slot` uses a reusable wi-owned worktree slot. `repository-home` borrows the repository's primary checkout through an exclusive logical claim and never makes that external checkout wi-owned.
- `workspace`: The logical Git checkout claim and its materialization for one item. A workspace is not a directory alone, a tmux session, or a Pi process.
- `materialization`: Creation or attachment of external resources described by durable state, such as assigning a managed slot, checking out the implementation branch, or recognizing a claimed repository home.
- `worktree`: A concrete Git working tree observed by wi. Managed worktrees may remain as warm reusable slot directories after an item releases its workspace claim.
- `managed slot`: A wi-owned reusable worktree location. Slots are claimed by items, reset and validated before reuse, and retained to preserve build caches.
- `repository-home claim`: An exclusive logical claim on the repository's primary checkout. Releasing or deleting the item relinquishes the claim but does not modify or delete that checkout.
- `lifecycle`: The durable work-item state: `backlog`, `working`, `waiting`, or `archived`. Lifecycle does not by itself describe whether a workspace, runtime, or terminal currently exists.
- `deep work`: A durable item classification subject to configured active-capacity policy. It is not a label.
- `primary conversation`: The item's durable Pi conversation identity and native JSONL history. It is created when agent work first starts and survives runtime restarts. An item has at most one primary conversation.
- `runtime`: One live or recorded Pi process instance that owns the primary conversation for a period. Successive runtimes may resume the same conversation; a runtime is not the conversation itself.
- `runtime owner`: The sole runtime currently authorized to write the primary conversation and affect runtime-activity projections. Ownership includes runtime ID and verified process identity; events from superseded runtime IDs are rejected.
- `runtime mode`: `tui` runs the interactive Pi interface and is composed with tmux. `rpc` runs headlessly and must not depend on tmux.
- `runtime activity`: Compact semantic facts reported by the current runtime, such as ready, turn started, tool started, and settled. Activity drives busy/idle observation but does not contain unrestricted prompts, responses, deltas, tool arguments, paths, or errors.
- `agent status`: The derived `busy|idle|problem` observation for the primary agent. It is separate from lifecycle and worktree status.
- `worktree status`: The derived `absent|clean|changed|problem` observation for the checkout. It is separate from agent status.
- `terminal`: Optional access metadata for an interactive runtime, currently a wi-owned tmux session and panes. Terminal identity is rebuildable and is not durable conversation or runtime identity.
- `access mechanism`: How a user enters or observes work, currently local tmux attachment or switching. Access code may enter an already selected resource but must not become work-item, conversation, or runtime identity.
- `attention`: Policy that selects and ranks actionable working items from lifecycle, activity, defer facts, agent status, and worktree status. Attention does not choose or invoke an access mechanism.
- `NEEDS ATTENTION`: A view bucket for working items whose primary agent is idle and whose agent/worktree observations do not report a problem. It is derived, not a lifecycle state.
- `projection`: A daemon-maintained read model derived from canonical commands, events, native content, or external observations. Projections live in `wi.db`; some external-resource projections are rebuildable.
- `observation`: A timestamped statement about an external resource such as a process, Pi session, worktree, or tmux session. Observations can become stale and must not silently replace durable intent.
- `event`: A compact append-only domain or runtime fact with stable identity and ordering. Events in `wi.db` exclude unrestricted native content.
- `coordinator daemon`: The sole structured-state writer and projection owner for one data root. It validates commands, commits events and projections, coordinates native writes, and ingests compact runtime events.
- `data root`: The canonical per-installation durable root, normally `$XDG_DATA_HOME/wi`. Each distinct absolute data root has its own `wi.db`, daemon, and deterministic runtime socket namespace.
- `operator endpoint`: The user-only daemon Unix socket with the full local API. Normal CLI commands and the host-side Pi bridge use it.
- `agent endpoint`: The separately mountable restricted daemon Unix socket for sandboxed tools. It permits health/minimal creation reads and safe absent managed-slot backlog creation, but rejects lifecycle, workspace, runtime, terminal, delete, and shutdown control.
- `runtime control socket`: A per-runtime Unix socket used for direct prompt, steer, abort, and shutdown commands. It is distinct from both daemon endpoints and is not canonical state.
- `native content`: Canonical filesystem content intentionally kept out of `wi.db`, currently descriptions and Pi JSONL conversations.
- `adapter`: Code that talks to an external system such as Git, Pi, tmux, direnv, Unix processes, or the filesystem. Adapters implement narrow contracts and do not own domain policy.
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
- `internal/store/`: native description and Pi-session filesystem access used by application services and tests.
- `internal/agent/`: driver-neutral runtime protocol.
- `internal/pi/`, `internal/git/`, `internal/tmux/`, `internal/direnv/`, `internal/process/`: external-system adapters.

## Architectural constraints

- Keep durable-item primitives independent of workspace, agent, attention, and tmux operations.
- Workspace primitives must not create terminal sessions or agent conversations.
- Primary-agent core must not conceptually depend on tmux; only TUI composition may require it.
- Attention may rank observations but must not choose or enter an access mechanism.
- Low-level terminal operations must not select work or create an agent.
- Cross-cutting use cases may compose core areas and adapters. Lower layers must not invoke use cases.
- Preserve deliberate partial outcomes: for example, if creation or a lifecycle transition succeeds but later materialization fails, retain and report the durable result.
- Route structured mutations through the daemon and item locks. Keep compact event history append-only.
- Never edit or move Pi JSONL sessions manually.
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

- The full suite should run without requiring a live tmux server or Pi process.
- CLI integration tests should isolate HOME and XDG roots.
- Test both TUI and RPC composition when changing runtime startup or lifecycle behavior.
- Preserve stable JSON shapes and documented exit-code distinctions for CLI changes.
- For persistence changes, test malformed input, interrupted/rebuildable state, locking, and path-escape safety where relevant.
