# Handoff: remote execution boxes

## For the next agent

You are picking up a design that is settled in direction but not implemented. The branch still contains a working but superseded transitional implementation. Your job is to turn the branch into the box design, in the order in [Next steps](#next-steps).

Ground rules, in addition to `AGENTS.md`:

- **Commits.** Do not commit unless the user asks. The branch has one work-in-progress commit that the user amends. Leave work in the working tree for review.
- **Installing.** After code changes that affect the `wi` executable, run `scripts/install-local` and update the Agentbox sandbox copy as `AGENTS.md` describes. Documentation-only changes need neither.
- **Decided points.** Do not reopen the decisions below unless you find concrete evidence against them. If you do, raise it with the user instead of silently diverging.
- **Open points.** Items marked **[OP-n]** in the design are open. Before implementing anything that depends on one, check whether the user has decided it. If they have not, ask, or pick the documented lean and say so explicitly.
- **Scope.** Keep each cleanup step small, with `go test ./...` and `go vet ./...` green after each.

## Read first

1. This file: the current state, decisions with their rationale, rejected alternatives, and the cleanup plan.
2. [`docs/internal/remote-execution.md`](docs/internal/remote-execution.md): the agreed box design, operation set, Git handoff, and numbered open points (OP-1 to OP-20).
3. [`docs/internal/remote-execution-research.md`](docs/internal/remote-execution-research.md): a cited survey of comparable tools (E2B, Daytona, Vercel, Coder Agents, container-use, Codex exec-server, Claude Code, VS Code and Zed remote, shpool and zmx, Mutagen, Git security).
4. [`docs/internal/remote-agent-placement.md`](docs/internal/remote-agent-placement.md): the hosted-workstation direction, updated to point at boxes.

## Repository state

- Branch `remote-tool-routing`, one work-in-progress commit ("wip - remote exec") on top of `main` (`f2367f3`, "Start agent runtimes with clean environments"). The commit hash changes whenever the user amends it.
- The commit contains the transitional implementation described below and the box design documents. Do not merge it as is.
- The installed development binary was built from the transitional implementation. Documentation changes since then do not affect it.
- Last validation:
  - On the transitional code: `go test ./...`, `go vet ./...`, `git diff --check`.
  - After the documentation amendment: `cd docs && npm run check` (0 errors) and `npm run build`. `docs/node_modules` had to be installed with `npm ci` first.
- Raw research notes are on disk, untracked, under `research_notes/Remote agent execution boxes/`. Whether to keep, commit, or delete them is pending with the user. The synthesized report is committed as `docs/internal/remote-execution-research.md`.

**Historical commits have no refs.** The documents previously said these were preserved as branches, but no branch or tag points at them. They are reachable only until Git prunes unreferenced objects:

| Commit | Content | Intended ref |
|---|---|---|
| `28c1c73` | worker-hosted remote executor snapshot: Git transfer, slot claims, remote terminal persistence, copy mode, clipboard | `snapshot/worker-hosted-remote-executor` |
| `79d12f4` | remote executor and driver protocol work in progress | `snapshot/remote-executor-wip-79d12f42` |
| `e68972b` | remote tmux attach experiment | `remote-tmux-attach` |

Create those refs (for example with `git branch <name> <commit>`) if they should survive. That decision is the user's.

## What the transitional implementation does

The branch restored the worker-hosted implementation and changed runtime placement in place, so that Pi runs on the host:

```text
Wi host
  coordinator + Pi + conversation + host tmux
    -> embedded Agentbox-derived Pi extension (internal/agentdriver/pi/remoteexec)
       -> wi internal-workspace-runtime container exec   (OCI-shaped shim)
          -> per-runtime Unix socket (internal/workspacetool)
             -> executor client (internal/executor/client.go)
                -> multiplexed SSH -> wi internal-executor
                   -> Worker.Handle (internal/executor/worker.go)
                      -> claim check through the worker daemon's item replica
                      -> process in the remote managed slot
```

It works and was tested:

- `go test ./...` and `go vet ./...` pass.
- The docs check and build pass.
- Manual checks with a fake-SSH executor passed:
  - Pi loaded the remote extension.
  - A remote slot materialized from a Git bundle.
  - `workspace.exec` returned the correct exit code, stdout, and stderr.
  - A stale generation and a released claim were rejected.
  - A clean release returned committed Git state.

Structural problems found in review, which the box design removes:

1. **Every tool call needs the worker daemon.** `workspaceExec` (`internal/executor/worker.go`) validates the claim by asking the worker's own daemon for its replica manifest.
2. **Claim authority is inverted.** `ensureWorkspace` mints the claim with `AssignNewClaim` on the worker replica. The host only accepts it (`internal/app/executor.go`, `ensureRemoteWorkspace`). The host must mint claims.
3. **Code is unreferenced but still present.** The worker `runtimeEnsure`, `runtimeStatus`, `runtimeControl`, and `terminalCopyMode` handlers, all of `internal/executor/attach.go`, `authorityInfo` and the authority tunnel, `ConversationSnapshot`, and `ExecutorAgentConfig` and `ExecutorDriverConfig` have no callers outside their own definitions. `ReconcileItems` exists only to maintain the replica.
4. **Versions must match exactly.** The executor's Wi version must equal the host's (`internal/app/executor.go`). That is unworkable once the box is provisioned separately.
5. **The shell is local.** The default shell window is host-local, so the original usability problem (a nearby shell in the remote target) is unsolved.
6. **Slots are allocated by scanning manifests.** `remoteSlot` picks slots by scanning host manifests rather than under a claim minted in the daemon.

## How the design was reached

Each step below was agreed in discussion. The design document states the result. This section records why.

1. **The goal is "run an item elsewhere," uniformly.** It started with a local container. Remote VMs, remote Macs, and cloud sandboxes must use the same model. The common denominator is a restricted container. Whatever works for a remote Mac must also work in a limited container or sandbox.
2. **`wi merge` is important.** That ruled out a "bring your own target" first version, in which work never returns as Git objects.
3. **The box owns its own clone.** Bind-mounting a host worktree into a container needs the shared `.git` mounted writable. That lets the box plant hooks or `core.fsmonitor` that host Git later executes, and it does not extend to remote boxes. Bundles in and out work for every box kind.
4. **No 1:1 path mapping.** Users, home directories, and permissions differ across boxes. The driver picks the workdir, and the box path is canonical for the agent. Most of the need for mapping disappeared once editors run in the box.
5. **Editor and diff run in the box.** Running neovim or hunk on the host against box files would need sync or mounts. Project-aware language servers would execute box-authored code on the host. Hunk also stores review notes for the agent next to the diff. The decision: they are box dev tools, launched from the box shell. Wi only opens the terminal.
6. **Git `origin` is a real remote backed by a bundle file.** `origin` is only a naming convention. A dangling `refs/remotes/origin/*` without a configured remote is non-standard and misleading. A bundle-file URL makes `git fetch origin` real and `git push` impossible.
7. **Environment note.** Wi appends a short system-prompt section describing the box, its constraints, and the handback rules.
8. **Persistence across disconnects.** The agent pauses with the host, but commands it started in the box keep running. Terminals die with the SSH link unless something in the box holds them. Durable execution was therefore needed for remote boxes.
9. **Helper or tmux.** Several positions were considered in turn:
   - plain-shell jobs plus tmux for terminals
   - a custom Wi helper for both, uploaded per platform
   - tmux for both
   - a preinstalled helper

   Two of the user's constraints decided it:
   - Use only guaranteed requirements. Dev boxes may be required to have dev tools.
   - Do not embed or upload per-platform binaries. Shipping all platforms is impractical, and a compiler cannot be embedded.

   The research then showed that real products use an in-box holder with handles and box-side logs. tmux, already required, can be that holder for both terminals and jobs, with `sh` scripts driving it. Its pseudo-terminal does not touch job output, because job streams go to files.
10. **Define the protocol, defer the helper.** The user challenged that this is "just ssh plus bash or tmux." The transport is. The protocol is not: handles, offset reads, process-group kills, claim fencing, and refusal of stale state. Wi defines that operation set now and implements it with scripts. A resident helper (possibly `wi` in a box mode, preinstalled) becomes a requirement only if measured latency demands it.

## Rejected alternatives

| Alternative | Why rejected |
|---|---|
| Worker Wi daemon with item replica (current branch) | Puts a second state owner in the box, inverts claim authority, and requires an exact version match |
| Pi or the whole agent running in the box (old snapshot, Claude Desktop SSH, Codex `--remote`) | Moves conversation and credential authority off the Wi host |
| Bring-your-own target without Git handoff | Breaks `wi merge`, release checks, and worktree status |
| Bind-mounted host worktree and shared `.git` | Host code-execution risk through repository config; does not work for remote boxes |
| Identical host and box paths | Breaks with different users, home directories, and read-only roots |
| Host-side editor or diff against box files | Needs sync or mounts; language servers break isolation; review notes belong beside the box |
| Opportunistic use of whatever holder exists (tmux, dtach, shpool) | Violates "guaranteed requirements only" |
| Uploaded per-platform Wi helper | The release would carry every platform build; development builds need cross-compilation; macOS signing and quarantine checks |
| Custom PTY and job daemon now | Real work that tmux already does; the protocol can be fixed without it |
| Plain-shell jobs without tmux | `setsid` is not guaranteed on macOS, and `set -m` job control is unreliable in non-interactive shells |
| Mutagen, sshfs, or other working-tree sync | Agent and user write concurrently; `.git` must not sync; transports are limited to SSH and Docker |
| mosh or Eternal Terminal | Fix only the transport, need UDP or their own servers, and do not survive host restarts |
| iTerm2 `-CC` or WezTerm SSH domains | Take over the layout that host tmux owns |

## Cleanup plan for this branch

Do this before adding box features. Keep the test suite green at each step.

1. **Delete worker-hosted leftovers:**
   - in `internal/executor/worker.go`: the runtime, terminal, copy-mode, authority, items-reconcile, and item-delete handlers
   - `internal/executor/attach.go`
   - the authority-tunnel code in `internal/executor/client.go`
   - `ConversationSnapshot` and the runtime and terminal types in `executor/protocol`
   - `internal/agentdriver/pi/conversation_snapshot.go`, if nothing else uses it
   - `ExecutorAgentConfig` and `ExecutorDriverConfig` in `internal/config`
   - the related CLI wiring
2. **Delete the worker daemon and replica:**
   - `workerRoot`, `workerPaths`, `runWorkerWI`, and `saveWorkerManifest`
   - replica creation in `ensureWorkspace`
   - the coordinator-client dependency in the executor package
3. **Introduce the box operation interface** on the host side: Go types for the operation set, and an `sh` script library embedded in `wi` as text. Add a fake driver for tests.
4. **Move claim minting to the host.** Materialization mints the claim ID and generation in `wi.db` before creating the box. The box stores the marker.
5. **Rewire `internal/workspacetool` and the runtime shim** from `executorprotocol.ToolRequest` to box `exec.run` and jobs. Keep the per-runtime socket and host-safe working directory.
6. **Replace the `executors.*` config** with `boxes.*` profiles: the `docker` driver first, then `ssh` and `command`. Decide naming first (OP-1).
7. **Replace remote managed slots** with per-item boxes (OP-2), and drop `remoteSlot`.
8. **Replace `wi internal-executor` and `executor/protocol`** with the script-based operations. Remove exact version matching.
9. **Box terminals.** Implement `wi internal-box-attach`, open box shell windows for box items, and add the `tmux.conf` holder.
10. **Git.** Reuse the bundle helpers for seed, update, export into `refs/wi/box/<item>/<generation>/*`, and hardened host Git. Then teach `integration_merge` to read the quarantine ref.
11. **Documentation.** Rewrite `docs/src/content/docs/guides/remote-executors.md` and the related reference pages once the commands exist. Update the AGENTS.md glossary (`executor`, `managed slot`, and the remote constraints).

Tests listed in the old executor document still matter where they carry over:

- malformed-input limits
- stale-claim rejection
- working directories that escape the workspace
- bounds on output, input, environment, command, and timeout
- release waiting for active operations
- remote runtime records and conversations staying local
- no remote path used as `cmd.Dir`
- unsupported drivers failing closed
- TUI and headless composition both routing tools correctly

## Highest-priority open points

All open points, with options and current leanings, are in the design document. These block or shape the cleanup most:

1. **OP-1 and OP-2 naming and domain model.** "Box" versus "executor"; a checkout kind versus a placement field; per-item boxes losing warm build caches.
2. **OP-7 latency.** Measure `docker exec` and warm-ControlMaster SSH per call on a real remote Mac before committing to one exec per Pi file read.
3. **OP-6 Pi tool semantics.** Whether Bash output is combined or separate; how `edit` writes; abort signals.
4. **OP-5 stale jobs after a host restart.** Kill them or report them to the agent; how Pi resumes an interrupted tool call.
5. **OP-8 SSH argument quoting.** Login-shell parsing, including zsh and fish.
6. **OP-9 tmux holder UX.** Copy-mode entry with the prefix disabled, terminfo, mouse, clipboard.
7. **OP-11 Git details.** Large-repository seeding, how the host branch updates from quarantine, signing, export cadence.
8. **OP-13 Pi `!` commands.** These currently run on the host even for box items.

## Next steps

### 1. Confirm pending user decisions

Ask the user, or confirm they have already answered, before writing code:

1. **Snapshot refs.** Should `28c1c73`, `79d12f4`, and `e68972b` get branches so they survive garbage collection? Do not create them unasked.
2. **Research notes.** Keep `research_notes/` untracked, commit them next to the report, or delete them?
3. **Naming (OP-1).** "Box" or another term? This affects config keys, commands, packages, and glossary updates, so settle it before step 3 below.
4. **Domain model (OP-2).** A checkout kind `box`, or a placement field? This shapes `internal/model/manifest.go` changes.

### 2. Verify facts the design depends on

These are quick investigations. Record findings in the design document's open points:

- **OP-6.** Read `internal/agentdriver/pi/remoteexec/core.ts` and its `*-support.ts` files. Determine:
  - how `bash` returns stdout and stderr
  - how `edit` and `write` write files
  - how abort and timeout reach the helper

  The current shim interface is `wi internal-workspace-runtime container inspect|exec`, in `internal/workspacetool/runtime.go`.
- **OP-14 and OP-13.** Find the Pi extension API for appending to the system prompt and for intercepting user `!` commands. Check the installed Pi package's extension types (`@earendil-works/pi-coding-agent`).
- **OP-5.** Determine what Pi does on resume when the last tool call never received a result.
- **OP-7.** If the user can provide a remote Mac or a local Docker, measure per-call latency of `docker exec` and of SSH with a warm `ControlMaster` for a trivial command. A small shell loop timing about 100 calls is enough.

### 3. Run the cleanup plan

Follow [Cleanup plan for this branch](#cleanup-plan-for-this-branch) steps 1 and 2 first. They are pure deletions of unreferenced code and the worker daemon, and should not change behavior on the local-only path.

Useful checks while deleting:

- `grep -rlw <symbol> --include='*.go' .` confirms a symbol has no remaining callers.
- Package-boundary tests enforce allowed imports. Run the full suite, not just the edited packages.
- Remove CLI commands and help text together. Preserve stable JSON shapes and exit codes for commands that remain.

### 4. Build phase 2: local container boxes

Start with steps 3 to 5 of the cleanup plan: the operation interface, the `sh` script library, a fake driver, host-minted claims, and the rewired tool shim. Then add the `docker` driver, Git seed and export, box shell windows, and `wi merge` from quarantine. The fake driver should make every operation testable without Docker, SSH, or a live tmux. `AGENTS.md` requires that the suite runs without them.

### Pitfalls

- Do not run host Git inside any directory the box wrote. Only bundles cross back.
- Do not let SSH boxes inherit the user's agent forwarding. The `ssh` driver sets `ForwardAgent=no`.
- `flock` and possibly `setsid` are missing on macOS. Use `mkdir` locks and tmux process groups in scripts.
- Keep the tmux socket path short. Unix sockets are limited to about 104 bytes on macOS.
- Neither `docker exec` nor SSH without a PTY kills remote processes when the client dies. Always kill the job's process group explicitly.
- Headless runtimes must not depend on host tmux. Box-side tmux is part of the box contract, so it is allowed.
- Preserve partial outcomes. A minted claim whose box creation failed is reported and retained, never silently deleted.
