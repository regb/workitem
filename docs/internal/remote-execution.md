# Remote execution boxes

Status: agreed design direction, not implemented. It supersedes the worker-daemon executor that the `remote-tool-routing` branch currently implements. `HANDOFF.md` describes the transitional code and the cleanup plan. [Remote execution research](remote-execution-research.md) records the external evidence behind these decisions.

Open points are collected at the end and referenced inline as **[OP-n]**. Anything not marked as decided is a proposal.

## Goal

Run the expensive or dangerous part of a work item in an isolated **box**, while the Wi host keeps coordination.

Target box kinds, in delivery order:

1. a local container on the Wi host
2. a remote VM or Mac reached over SSH
3. a cloud sandbox reached through its CLI

All box kinds use one model. Whatever Wi does with a remote Mac must also work with a restricted local container or a cloud sandbox. Driver differences stop at provisioning and command transport.

Motivations:

- strong per-item isolation for agent commands
- more compute for expensive builds and tests
- a different operating system or architecture, for example iOS work from a Linux Wi host

Non-goals:

- running Pi, conversations, or runtime ownership in the box
- an editor, diff tool, or language server on the host operating on box files
- syncing working-tree files between host and box
- moving dirty working trees, checkpointing, or high availability
- Wi-provisioned images (the user or the sandbox provider owns images) **[OP-4]**

## Decisions

These were settled in design discussion. Rationale and rejected alternatives are summarized in `HANDOFF.md`.

1. **Control plane on the host.** The coordinator, `wi.db`, work items, Pi, native conversations, runtime ownership, and user-facing tmux stay on the Wi host. The box owns a replaceable Git checkout and the processes started in it.
2. **One box per item.** A box is the item's environment. Reusable slots shared between items are not part of the box model. Fencing reduces to "this box belongs to this item at this claim generation."
3. **The box owns its own repository.** The box gets a separate clone, not a bind-mounted host worktree or shared Git directory. Commits travel as Git bundles in both directions, so `wi merge` keeps working on the host.
4. **No path identity.** The driver chooses the box workspace path and Wi records it. That path is canonical for the agent. Wi does not try to reproduce host paths in the box.
5. **Editor, diff, and dev tools run in the box.** Neovim, hunk, compilers, and test runners are box image contents, like any build tool. Wi does not run them on the host against box files.
6. **Wi opens terminals in the box.** Wi's terminal feature is starting interactive commands in the box from host tmux panes. A shell is the default.
7. **Requirements are guaranteed, not detected.** A box profile declares a fixed set of required tools. Wi checks them once when the box is created and refuses a box that lacks them. Wi does not adapt its behavior to whatever happens to be installed.
8. **Dev boxes may be required to have dev tools.** Requiring tmux and Git is acceptable.
9. **No Wi binary is shipped into the box.** Embedding or uploading per-platform helper binaries is impractical. Wi drives the box with short POSIX `sh` scripts over the driver's exec transport. If a resident helper becomes necessary later, it is a preinstalled requirement (possibly `wi` in a box mode), not an upload. **[OP-7]**
10. **tmux in the box holds long-lived processes.** One private tmux server per box holds interactive terminals and durable agent jobs. Agent jobs never run inside user terminals.
11. **The executor operation set is defined now.** Callers depend on a fixed set of box operations. The first implementation is `sh` scripts. A later resident helper may replace the scripts without changing callers.
12. **An environment note is appended to the agent's system prompt.** It tells the agent where it runs and what it may use.

## Responsibilities

| Concern | Wi host | Box |
|---|---|---|
| Item identity, lifecycle, history | owns | none |
| Claim ID and generation | mints in `wi.db` | stores a marker; rejects mismatches |
| Pi process, conversation, credentials | owns | none |
| Pi tool calls | issues through box operations | executes |
| User-facing tmux layout and navigation | owns | none |
| Interactive terminal processes | attaches from host panes | holds them in a private tmux |
| Durable agent jobs | starts, follows, cancels | runs and logs them |
| Git history | canonical; quarantines returned objects | working clone for one item |
| Worktree status | projects an observation | answers `git status` |
| Images, toolchains, network policy | none | provided by the box profile's image or machine |

## Concepts

- `box`: one item's isolated execution environment. It contains the item's working clone, a Wi state directory, a private tmux server, and processes started by the agent or user. It is materialization, never canonical state.
- `box profile`: named configuration that describes how to create, reach, and destroy boxes of one kind. Items select a profile.
- `box driver`: the transport and lifecycle adapter that a profile configures. Drivers provide exactly two transport primitives (non-interactive exec and interactive exec) plus optional lifecycle commands.
- `box claim`: the durable host record that an item's box is materialized. It records the profile, box identity, workspace path, state directory, claim ID, and generation.
- `box operation`: one call in Wi's executor operation set, implemented as an `sh` script sent through the driver.
- `job`: one durable agent command running in the box under the private tmux server, with logs and an exit record in the state directory.
- `terminal`: an interactive session in the box's private tmux, attached from a host tmux pane.

How these map onto the existing glossary (`executor`, `managed slot`, `workspace claim`, checkout kind) is open. **[OP-1]** **[OP-2]**

## Box profiles

Profiles live in the Wi config file beside agent and driver definitions. Each command is an argv array. Wi substitutes template variables element by element and never passes host-side strings through a host shell.

```toml
[boxes.local]
driver      = "docker"
image       = "ghcr.io/me/dev:latest"
workdir     = "/workspace"
state_dir   = "/var/lib/wi-box"
isolation   = "container"
network     = "none"

[boxes.mac]
driver      = "ssh"
destination = "mac-mini"
workdir     = "~/wi/boxes/{repo}/{item}/work"
state_dir   = "~/wi/boxes/{repo}/{item}/state"
isolation   = "none"
network     = "full"

[boxes.sandbox]
driver      = "command"
create      = ["sbx", "create", "--name", "wi-{box}", "--image", "dev"]
start       = ["sbx", "start", "wi-{box}"]
stop        = ["sbx", "stop", "wi-{box}"]
destroy     = ["sbx", "rm", "-f", "wi-{box}"]
exec        = ["sbx", "exec", "wi-{box}", "--"]
interactive = ["sbx", "exec", "-t", "wi-{box}", "--"]
argv        = "exec"
workdir     = "/home/dev/work"
state_dir   = "/home/dev/.wi-box"
isolation   = "vm"
network     = "egress-restricted"
```

`sbx` stands for any sandbox CLI with exec and terminal support.

Template variables:

| Variable | Meaning |
|---|---|
| `{item}` | short item ID |
| `{box}` | stable box name derived from host key and item ID |
| `{repo}` | repository key |
| `{claim}` | claim ID |
| `{generation}` | claim generation |

### Driver kinds

The proposal is typed drivers for common cases plus a raw `command` driver. **[OP-3]**

- `docker`: generates `docker run -d --name … sleep infinity`, `docker exec -i`, `docker exec -it`, `docker stop`, and `docker rm -f`. It can also target Podman or Apple `container` through a `runtime` field. Wi controls the flags, so it can refuse privileged containers and host network by default.
- `ssh`: generates `ssh` commands with Wi-controlled options:
  - `BatchMode=yes`
  - `ForwardAgent=no` and `ForwardX11=no`
  - `ControlMaster=auto` with a short `ControlPath` and `ControlPersist=60`
  - `ServerAliveInterval=15` and `ServerAliveCountMax=3`

  There is no create command. The box is the profile's directories on that machine, and destroy removes them after checking the ownership marker.
- `command`: raw argv prefixes for anything else. The user is responsible for safe transport options.

### Argument passing

SSH joins its remote arguments into one string and the remote login shell parses it. `docker exec` and most sandbox CLIs pass argv unchanged. A profile therefore declares `argv = "exec"` or `argv = "shell"`.

In shell mode, Wi sends exactly one remote argument: a POSIX single-quoted `exec sh -c '<script>' wi-op <args…>`. The remote login shell may be zsh or fish. Single-quote quoting is portable across the common shells, but fish behavior needs a test. **[OP-8]**

### Isolation and network labels

`isolation` and `network` are declared by the profile, not detected. Wi shows them in `wi show`, includes them in the agent's environment note, and may refuse risky combinations by policy, such as `isolation = "none"` for items marked untrusted. **[OP-12]**

## Box requirements

Every box must provide:

- POSIX `sh` and the POSIX utilities used by Wi scripts: `mkdir`, `rm`, `mv`, `cat`, `tail`, `kill`, `test`, `printf`, `date`, `uname`, `id`
- Git, at a minimum version to be fixed **[OP-11]**
- tmux, at least 3.3 for `allow-passthrough` and current `extended-keys` behavior **[OP-9]**
- a writable state directory and workdir, on a filesystem that allows execution
- for SSH Linux boxes, user processes that survive logout: `loginctl enable-linger`, or no systemd-logind `KillUserProcesses` **[OP-17]**

A box does not need:

- Wi or Pi
- sshd (container and sandbox drivers)
- outbound network
- forge or model credentials

### Probe

When a box is created, Wi runs one probe operation. It records OS, architecture, user, home, and the versions of `sh`, Git, and tmux, and verifies that both directories are writable. A failed requirement refuses the box with an actionable message. The probe result is stored as an observation. It is never used to switch implementations.

## Box layout

```text
<workdir>/                     the item's working clone (own .git)
<state_dir>/
  owner                        Wi host key + item ID; checked before destroy
  claim                        "<claim-id> <generation>" or "revoked <generation>"
  lock/                        mkdir-based mutual exclusion for claim changes
  tmux.sock                    private tmux server socket
  tmux.conf                    Wi-written holder configuration
  git/origin.bundle            current host export; the box's origin URL
  git/incoming/                bundles being received
  jobs/<job-id>/
    cmd                        argv record for diagnostics
    pane_pid                   process-group leader recorded at start
    out                        stdout log (append-only)
    err                        stderr log (append-only), unless combined [OP-6]
    exit                       exit code, written when the job ends
    started, ended             timestamps
```

`flock` is not available on stock macOS, and `setsid` is not confirmed there. Scripts use `mkdir` as the atomic lock primitive and tmux to create process groups.

Unix socket paths are limited to roughly 104 bytes on macOS and 108 on Linux. The tmux socket must stay short, so the state directory default needs care on deep home paths. **[OP-17]**

## Executor operation set

Every request carries item ID, claim ID, and generation. Every script first compares them with `<state_dir>/claim` and fails with a fencing error on mismatch or revocation.

Fencing and protocol errors are reported on stderr as a single framed line, `wi-box: <code>: <message>`, together with a reserved exit status. The host tells a protocol failure from a command's own exit status by the framed line, not by the number alone. **[OP-8]**

| Operation | Purpose | Notes |
|---|---|---|
| `probe` | requirements and facts | runs before a claim exists |
| `claim.write` | write owner and claim markers | first write after create |
| `claim.revoke` | mark revoked, kill jobs, close terminals | first step of release |
| `git.seed` | receive the seed bundle and create the working clone | bundle arrives on stdin |
| `git.update` | receive an incremental bundle as the new `origin` | the agent or Wi then runs `git fetch origin` |
| `git.export` | stream a bundle of the item branch to stdout | host quarantines it |
| `git.status` | porcelain v2 status plus branch and head | feeds worktree status |
| `exec.run` | short attached command with stdin and stdout/stderr | read, write, edit, grep, find, ls |
| `job.start` | start a durable command; return job ID | bash tool |
| `job.follow` | stream logs from offsets until exit, then exit with the job's status | reconnects reuse it |
| `job.status` | running or exited, exit code, log sizes | |
| `job.signal` | signal the job's process group | cancel and timeout |
| `job.list` | jobs for this claim | after host restart |
| `job.prune` | delete finished job directories | retention policy **[OP-5]** |
| `term.attach` | interactive: create or attach a named terminal | uses the interactive transport |
| `term.list` | terminal sessions in the private tmux | rebuild host panes |
| `term.kill` | end one terminal | |
| `box.destroy-check` | verify owner marker and Git cleanliness | before driver destroy |

Scripts are passed as `sh -c` arguments, not stored in the box. That keeps stdin free for command input and removes any box-side script version to drift. The size of a script plus its arguments must stay well below `ARG_MAX`.

The operation set is the stable interface. A resident helper, if one is ever needed, implements the same operations. The operation names and the Codex exec-server shape (process start, read with cursor, write, terminate) are deliberately similar.

## Agent tool execution

Pi keeps the Agentbox-derived remote-exec extension. Its OCI-shaped helper (`wi internal-workspace-runtime container exec`) becomes a client of box operations instead of the worker executor.

- `bash` uses jobs:
  1. `job.start` launches `tmux -S <sock> new-session -d -s job-<id> -c <cwd> 'sh -c "<cmd>" <in >out 2>err; echo $? >exit'` and records `pane_pid`.
  2. `job.follow` streams `out` and `err` from the last offsets and returns when `exit` exists.
  3. On cancel or timeout, `job.signal` sends TERM, then KILL after a grace period, to the process group `-<pane_pid>`.

  The start and the first follow may run as one exec round trip so a call costs one transport hop. **[OP-7]**
- `read`, `write`, `edit`, `grep`, `find`, and `ls` use `exec.run`. They are short, and their whole-file writes are safe to retry. This must be verified against the extension's `edit` implementation. **[OP-6]**
- Command stdout and stderr go to files, not to a terminal. Programs therefore see no TTY on those streams: no colors, pagers, or merged output. A program that opens `/dev/tty` directly may still block until timeout. The environment note forbids interactive prompts.
- Commands that daemonize themselves leave the job's process group. They are cleaned up only by box stop or destroy.

### Disconnects and restarts

| Event | Behavior |
|---|---|
| Transport drops while Pi is alive (sleep, network) | The host-side runtime process retries `job.follow` from its last offsets with backoff until Pi's own timeout or cancel. The agent sees one uninterrupted call. |
| Pi or the Wi host restarts | The in-flight tool call is gone from Pi's perspective. The job keeps running. Policy for such jobs is open: kill on runtime start, or report them to the agent. **[OP-5]** |
| Box stops | Every process ends. Logs on disk remain. |
| Box destroyed | Everything is removed after Git export. |

This is the handle-plus-log pattern of E2B, Vercel, Daytona, and Codex exec-server, built from `sh` and tmux. Plain `ssh` without a PTY and `docker exec` both leave orphaned processes when the client disappears. Explicit process-group kills are therefore required even if durability were not wanted.

### Environment note

Wi appends a short section to the agent's system prompt for box items. It is built from facts Wi already has:

- the box workspace path, which is the working directory
- the profile name, isolation label, network label, OS, and architecture from the probe
- the item branch, and `origin/<base>` as the host-provided base
- how to get newer base commits: `git fetch origin` once Wi has delivered an update
- how to hand back work: commit on the item branch; the host integrates; do not push
- no credentials and no interactive prompts; long commands are fine
- `wi` is not available in the box; item operations happen on the host

The Pi hook for appending this is unverified. **[OP-14]**

### Paths

- Pi runs on the host with a host-safe item directory as its process working directory. This is already implemented.
- The extension maps that directory to the box workdir. The box path is what the agent sees and uses.
- Context files such as `AGENTS.md` are discovered in the box through the redirected tools.
- Pi user `!` commands are not redirected by the extension and would run on the host. **[OP-13]**

## Terminals

Each box has one private tmux server on `<state_dir>/tmux.sock`, started with the Wi-written `tmux.conf`. Host tmux owns the layout. The box tmux only keeps processes alive.

Attach path:

```text
host tmux pane
  -> wi internal-box-attach <item> <name>
     -> driver interactive exec
        -> tmux -S <sock> -f <conf> new-session -A -s term-<name> -c <workdir>
```

- The host attach command reconnects after transport failure (SSH exit status 255 or a failed connection) with a visible notice and backoff. It exits when the remote session itself ends.
- For box items, Wi opens a box shell window by default where it currently opens a host shell. The Pi window stays a host-local Pi TUI.
- Additional named windows (for example `editor = ["nvim"]` or `diff = ["hunk"]`) may be configured per profile. **[OP-10]**
- After a host tmux restart, Wi calls `term.list` and recreates host windows for existing terminals. Window names and order are recorded in rebuildable terminal metadata.

### Holder configuration

`tmux.conf` contains at least:

```tmux
set -g prefix None
unbind C-b
set -g status off
set -g escape-time 0
set -g set-clipboard on
set -g allow-passthrough on
set -g extended-keys on
set -as terminal-features ',*:RGB'
set -g history-limit 50000
set -g remain-on-exit off
```

Known costs and open questions **[OP-9]**:

- Scrollback lives in the inner tmux. Host tmux scrollback fills with redraw output. Entering inner copy mode needs a binding, because the inner prefix is disabled. Options: a root-table key in the inner config, or a host binding that runs `tmux -S <sock> copy-mode` through exec, as the old snapshot did.
- `default-terminal` should be `tmux-256color`, but box images may lack that terminfo entry. A fallback is needed.
- Mouse handling between the two tmux layers is undecided.
- shpool or zmx would keep host scrollback native. They stay possible later, but only as declared requirements behind `term.attach`.

For local containers, a plain `docker exec -it` shell would suffice, because no network link can drop. Using the same holder everywhere keeps one code path. **[OP-10]**

## Git handoff

### Seed

1. The host creates a bundle of the base ref at `created_from_commit` and the item branch if it exists.
2. The bundle streams through exec stdin into `git/incoming/`, then moves to `git/origin.bundle`.
3. The box initializes `<workdir>`, sets `remote.origin.url` to the bundle path and `remote.origin.fetch` to the usual `+refs/heads/*:refs/remotes/origin/*`, runs `git fetch origin`, and checks out the item branch tracking `origin/<base>`.
4. The box Git identity (`user.name`, `user.email`) is copied from the host's effective config for the repository.

`origin` is a real remote backed by a bundle file:

- `git fetch origin` works and returns what the host last exported.
- `git push` fails, because a bundle cannot be pushed into.
- The usual `origin/main` workflows work for both people and agents.

### Updates from the host

Wi writes an incremental bundle (`<old>..<new>`) of the base branch into `git/origin.bundle`. The box already has the prerequisite commits. The agent or Wi then runs `git fetch origin`, and the box rebases or merges at its own pace. Updates are triggered explicitly ("refresh base") at first. **[OP-11]**

### Returning work

1. `git.export` runs `git bundle create - <item-branch> --not <last-known-base>` in the box and streams it to the host.
2. The host verifies the bundle and fetches it into a quarantine namespace: `refs/wi/box/<item>/<generation>/heads/<branch>`.
3. Integration (`wi merge`) and worktree status read from that quarantine ref. How the host's own item branch ref is updated from it is open. **[OP-11]**

Export runs before merge, before release or destroy, and on explicit status refresh. Whether to also export periodically is open. **[OP-20]**

### Host-side hardening

- Host Git never runs with its working directory inside a box-written directory. Only packed objects return.
- Fetches from box bundles use `transfer.fsckObjects=true`.
- Wi's own Git invocations on box-derived refs use `-c core.hooksPath=/dev/null -c core.fsmonitor=false`. Review diffs use `--no-ext-diff --no-textconv`.
- Repository-local `core.fsmonitor`, `core.hooksPath`, and filter drivers are honored by Git from repository config. A shared writable `.git` would give the box code execution on the host. That is why the box has its own clone.
- After `wi merge`, box-authored files are in the host checkout. Host tools that execute repository content, such as direnv `.envrc`, editor workspace settings, and Git hooks committed in-tree, then see untrusted content. Review at merge is the trust gate. **[OP-12]**

### Cleanliness and destroy

Release and destroy refuse when the box has uncommitted changes or unexported commits, unless forced. A forced destroy reports what was lost. Whether Wi offers an automatic WIP commit before destroy is open. **[OP-11]**

### Unsupported in v1

Submodules, Git LFS, reftable repositories, and bundles beyond a size limit are refused with a clear message. Claude Code's bundle seeding refuses similar cases. **[OP-11]**

## Claims and fencing

1. Materialization starts by minting a new claim ID and generation in `wi.db`, together with the selected profile and box name. This corrects the current branch, where the worker mints the claim.
2. Wi creates and starts the box, runs the probe, and writes `owner` and `claim`.
3. Every box operation checks `claim` first.
4. Release:
   1. Write `revoked <generation>` under the `mkdir` lock.
   2. Kill all jobs and terminals.
   3. Run `git.export` and quarantine the result.
   4. Verify cleanliness.
   5. Clear the claim in `wi.db`.
   6. Destroy or stop the box according to lifecycle policy. **[OP-16]**
5. Later materialization advances the generation. Delayed operations from an old runtime fail at step 3.

Partial outcomes follow existing rules. A minted claim whose box creation or seed failed is retained and reported. It is never silently deleted. Retrying materialization reuses or replaces the box by name.

## Item lifecycle mapping

| Wi action | Box effect |
|---|---|
| `wi start` on a box item | mint claim, create and start box, probe, write markers, seed Git, write `tmux.conf`, start host Pi runtime, open box shell window |
| host restart or `wi resume` | start the box if stopped, re-probe, reattach terminals, handle stale jobs **[OP-5]** |
| shelve | stop the runtime; export Git; keep, stop, or destroy the box **[OP-16]** |
| `wi merge` | export Git, then integrate from the quarantine ref |
| archive or delete | release as above, then destroy the box after the owner check |

Headless runtimes use the same box operations. They need no host tmux; box-side tmux is part of the box contract, not the host runtime mode.

## Shared remote machines

A remote VM may run many unrelated things and several Wi boxes.

- Each box is a directory pair namespaced by Wi host key, repository, and item.
- `owner` markers prevent one Wi host or item from destroying another's directories.
- Tools, caches, and ports are shared with everything else on the machine. The profile's `isolation = "none"` makes that visible.
- The user's credentials and SSH agent on that machine may be reachable by agent commands. Wi's no-push policy is advisory there.
- Running a container driver over SSH on the VM restores isolation. It is a later combined driver. **[OP-15]**

## Code from the current branch

Keep or adapt:

- the embedded Pi remote-exec extension, its tests, and the `internal-workspace-runtime` shim
- host-safe process working directories for remote claims
- runtime ownership fenced by claim ID and generation
- the SSH multiplexing and keepalive client options
- Git bundle creation and import helpers
- config parsing and validation patterns for named profiles
- process-group cancellation semantics

Remove:

- the worker daemon, worker XDG root, and item replica
- `items.reconcile`, `authority.info`, the authority tunnel, and the restricted reverse socket
- worker runtime, terminal, copy-mode, and conversation-snapshot operations and `internal/executor/attach.go`
- `ExecutorAgentConfig` and `ExecutorDriverConfig`
- `wi internal-executor` and the JSON-over-stdin executor protocol in `executor/protocol`
- exact Wi version matching between host and box
- remote reusable managed slots

## Delivery phases

1. **Cleanup.** Reduce the branch to the kept pieces, with tests still passing.
2. **Local container box.**
   - `docker` driver
   - probe, claims, and markers
   - Git seed, export, status, and quarantine
   - `exec.run` and jobs for Pi tools
   - box shell window and environment note
   - `wi merge` from quarantine
   - tests with a fake driver that runs scripts locally
3. **SSH box.**
   - `ssh` driver and argument quoting
   - reconnecting attach and follow
   - keepalive handling after sleep
   - linger requirement
   - owner markers on shared machines
   - tested against a real remote Mac
4. **Command driver** for cloud sandboxes.
5. **Optional, evidence-driven additions:**
   - a resident helper if per-call latency demands one **[OP-7]**
   - shpool or zmx holders
   - port forwarding
   - host-side Wi tools exposed to the agent

## Testing

- Box scripts run against a fake driver whose exec is `sh -c` in a temporary directory, so the suite needs no container, SSH, or live tmux beyond what tests explicitly provide.
- Fencing: wrong claim, old generation, revoked marker, owner mismatch on destroy.
- Jobs:
  - start, follow, and resume from offsets
  - cancel kills the whole process group
  - exit status propagates
  - log reads past EOF
- Quoting: arguments containing quotes, spaces, newlines, and shell metacharacters through `argv = "shell"`.
- Git:
  - seed with and without an existing item branch
  - incremental update
  - export into quarantine
  - refusal of dirty or unexported state
  - hostile repository config in the box having no host effect
- Terminals: attach command construction and reconnect decisions. Live tmux tests stay optional.
- Both TUI and headless runtime composition for box items.

## Open points

### OP-1 Naming

"Box", "executor", "environment", or "sandbox". The existing glossary defines `executor` as a machine that materializes managed slots. The box model replaces that meaning. Lean: `box` for the per-item environment, `box profile` for configuration. Retire `executor` from user-facing commands, and update the AGENTS.md glossary.

### OP-2 Domain model

How a box appears in the checkout specification. Options:

- a new checkout kind `box` beside `managed-slot` and `repository-home`
- an orthogonal placement field on the specification

Related questions:

- whether a box item can change profile before first materialization (today's executor selection allows that)
- whether `repository-home` can ever be boxed (lean: no)

Per-item boxes lose warm build caches that slots preserved. Profile-level cache volumes or directories shared per repository could restore them, at some isolation cost.

### OP-3 Typed drivers versus raw prefixes

Lean: `docker` and `ssh` typed drivers that enforce safe options, plus a raw `command` driver. Open: whether typed drivers are worth the code in phase 2, and how much of Docker's flag surface to expose.

### OP-4 Image provisioning

Who builds images and documents requirements:

- user-maintained Dockerfiles
- reuse of `devcontainer.json`
- Agentbox images
- sandbox provider templates

Lean: document requirements and a reference Dockerfile; no Wi image builder.

### OP-5 Job retention and stale jobs

Two questions:

- What happens to jobs whose Pi call disappeared after a host restart. Options: kill them on runtime start (safe default), or list them in the environment note with a way to read their logs.
- How long finished job logs stay, and whether there is a size cap.

Pi's behavior with an interrupted tool call on resume is unverified.

### OP-6 Pi tool semantics

Verify against the embedded extension and Pi:

- whether `bash` returns stdout and stderr separately or combined; this decides one or two job logs
- how `edit` writes; whether it is a whole-file write that is safe to retry
- what timeout and abort signals look like

### OP-7 Latency and the resident helper

Nothing measured yet. Measure per-call latency of `docker exec` and SSH with a warm ControlMaster on a real remote Mac, for both `exec.run` and a combined `job.start` plus follow. Agents issue many small reads, and Emacs TRAMP's 50–100 ms per operation is the cautionary reference. Define the threshold at which a preinstalled resident helper (possibly `wi box serve`) becomes worth requiring.

### OP-8 Script transport details

Decide and test:

- `sh -c` argument delivery and size limits
- POSIX single-quote quoting for SSH, including zsh and fish login shells
- the framed `wi-box:` error line and the reserved exit statuses
- whether shell mode should wrap everything in `exec sh -c` to neutralize the login shell

### OP-9 tmux holder UX

Decide:

- the minimum tmux version
- terminfo fallback
- the copy-mode entry key
- mouse behavior
- clipboard passthrough tested with host tmux and common terminals
- `extended-keys` through both layers
- whether to offer shpool or zmx later as alternative requirements

### OP-10 Terminal defaults

Decide:

- whether local containers use the tmux holder or plain `docker exec -it`
- which windows open by default for box items
- whether editor and diff windows are configurable per profile or left to the user's shell

### OP-11 Git details

Decide:

- minimum Git version
- history depth and size limits for seed bundles in large repositories, with fallbacks
- submodules, LFS, and reftable policy
- commit signing in the box, since host keys are absent
- export triggers
- quarantine ref naming
- how and when the host's item branch ref is updated from the quarantine ref, including divergence when both sides changed
- whether Wi auto-delivers base updates or only on request
- WIP commit before forced destroy

### OP-12 Security policy

Decide:

- network egress control and who enforces it (driver flags versus image)
- the credential policy text
- the minimum isolation required for untrusted items
- warnings about untrusted content landing in the host checkout after merge, including direnv

### OP-13 Pi user `!` commands

They currently run on the host. Options:

- route them through `exec.run`
- block them for box items
- document the behavior

Lean: route them through the box once the extension exposes a hook.

### OP-14 Environment-note mechanism

Confirm how a Pi extension appends to the system prompt, and whether the note can refresh after base updates.

### OP-15 Shared machines and nesting

Decide:

- namespacing by Wi host key
- discovering and cleaning orphaned box directories from crashed hosts
- whether to add an `ssh+docker` driver for isolated boxes on shared VMs

### OP-16 Lifecycle policy

Decide what shelve and `waiting` do to a box: keep it running, stop it, or destroy it after export. Also decide whether the deep-work capacity policy should count running boxes.

### OP-17 Platform specifics

macOS boxes:

- socket path length
- no `flock` and possibly no `setsid`
- whether processes survive SSH logout without extra setup

Linux boxes:

- logind linger
- cgroup limits in containers
- Apple `container` behavior as a Docker-compatible runtime

### OP-18 Port forwarding

Dev servers in the box need host access sometimes. A profile-level `forward` operation (`ssh -L`, published container ports) is deferred. Decide whether it is needed before SSH boxes ship.

### OP-19 Status cost

`wi list` cannot run `git status` in every box on every refresh. Worktree status for boxes needs an observation cache with a time-to-live, refreshed on demand and after jobs finish.

### OP-20 Export cadence

Decide whether commits are exported only on demand, after each settled agent turn, or periodically. More frequent export narrows what a lost box takes with it.
