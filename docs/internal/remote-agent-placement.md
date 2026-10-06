# Deployment and remote execution direction

Status: selected architecture direction. Hosted deployment and remote execution beyond ordinary SSH attachment are not yet implemented as product features.

## Decision

Wi is local-first, where "local" means one Wi host rather than one laptop.

One host owns:

- the canonical data root and coordinator daemon
- descriptions and native conversations
- coding-agent runtime processes
- user-facing tmux
- local repositories and workspaces
- Pi installation, configuration, extensions, and provider credentials

The host may be a developer laptop or an always-on Linux server. Wi uses the same local filesystem, Unix-socket, process, and tmux model in both deployments.

Remote access does not change domain identity. SSH, a browser terminal, and a future web API are ways to access the host. They are not work items, workspaces, conversations, runtimes, or terminal identities.

## Hosted workstation

The preferred always-on deployment is a Linux workstation reached through SSH:

```text
terminal
  -> SSH
     -> host tmux
        -> work-item session
           -> Pi TUI

Wi host
  -> coordinator daemon
  -> wi.db
  -> descriptions and conversations
  -> repositories and managed worktrees
  -> agent runtimes
```

The server owns user-facing tmux. A laptop does not run a second Wi client or mirror the server's layout. It can run SSH inside a personal tmux, but that outer tmux is outside Wi's model.

This has several useful properties:

- laptop sleep only disconnects the terminal
- Pi and tmux continue on the host
- another laptop can attach to the same account
- Unix sockets remain private to one machine
- native conversation files do not need routine synchronization
- Pi versions, extensions, settings, and credentials have one owner
- local commands and code paths remain useful on laptops and servers

The attachment path is ordinary terminal forwarding:

```text
Pi process <-> host PTY <-> host tmux <-> SSH <-> terminal
```

Pi has no native TUI attachment protocol. Tmux owns the persistent PTY. Starting a second Pi TUI against the same conversation is not attachment and must remain forbidden.

## Why not a distributed local tmux client

An earlier cloud sketch kept user-facing tmux on every laptop and gave each local pane an SSH gateway to cloud terminals. That would require Wi to coordinate two UI layers, reconnect gateway panes, route copy mode, map local panes to remote terminals, and keep per-client layout state.

The hosted-workstation model avoids that distributed UI. Tmux already supports disconnect and reattach. Wi should use it before inventing another terminal controller.

## Multi-client access

Several SSH clients can attach to the host. They share canonical work-item and runtime state.

Tmux can attach several clients to one session, but simultaneous control of one Pi TUI causes input and resize conflicts. The initial operational rule should be one controlling client per interactive runtime. Explicit observer and takeover semantics may come later.

Wi navigation should switch the invoking tmux client between work-item sessions. It must not treat one client's selected session as global user intent.

## Web access

Web access has two separate forms.

### Structured application

A web application should query work-item projections and submit typed commands through a restricted network service. Likely resources include:

- work-item list and detail
- descriptions and append-only history
- lifecycle and labels
- attention ranking
- workspace and agent observations
- decoded conversation views
- prompt, steer, abort, and shutdown controls
- bounded event streams with durable cursors

The daemon's operator Unix socket remains local and must not be exposed over TCP. A network gateway needs authentication, authorization, command idempotency, rate and size limits, and audit records.

Native descriptions and conversations can remain filesystem content. The gateway may read and decode them on demand without copying unrestricted content into `wi.db`.

### Browser terminal

A browser terminal can connect `xterm.js` through an authenticated WebSocket gateway to a constrained PTY that attaches to host tmux. It should not reconstruct a terminal by scraping `capture-pane`.

The structured application should not depend on browser-terminal availability. Work items remain useful without a live terminal.

## Optional remote workspace execution

Local-first deployment does not require every workspace to live on the Wi host. The selected direction keeps Pi and its conversation on the Wi host and runs an item's workspace-scoped work in a per-item **box**: a local container, a remote VM or Mac reached over SSH, or a cloud sandbox. See [Remote execution boxes](remote-execution.md) for the full design and its open points.

```text
Wi host
  coordinator, wi.db, Pi, conversation, host tmux
      -> box driver (docker exec | ssh | sandbox CLI)
         -> Wi sh scripts for box operations

box (one per item)
  own Git clone of the item branch
  private tmux server holding terminals and agent jobs
  image-provided toolchain, editor, and diff tools
```

This is useful for:

- per-item isolation of agent commands
- large build machines
- architecture- or OS-specific work
- cloud sandboxes
- local Wi installations that need occasional extra compute

The agent process stays on the Wi host. Laptop sleep is safe when that host is an always-on server. With a laptop-hosted Wi installation, the agent pauses with the laptop, while box jobs keep running and are followed again after wake.

## Tool execution semantics

Pi Bash calls are independent. Pi starts each call with a complete command and explicit working directory. Shell state such as `cd`, exports, aliases, and functions does not persist to the next call.

Box tools preserve that behavior:

- reuse a multiplexed transport (SSH ControlMaster) where the driver supports it
- run each Bash call as a separate durable job: its own tmux session in the box's private server, logs in files, an exit record, and process-group cancellation
- run short file operations as independent attached commands
- reconnect to a running job from recorded log offsets after a transport drop

Injecting tool commands into a user's persistent shell would create hidden mutable state, serialize calls, and complicate cancellation and output framing. Jobs and user terminals share the box tmux server but never a session.

## Claim fencing

The Wi host mints each box claim ID and generation in `wi.db`. Every box operation carries the item ID, claim ID, and generation. The box state directory holds a claim marker. Every Wi script checks it before acting and fails closed on mismatch or revocation.

Release writes a revocation marker, kills jobs and terminals, exports Git, verifies cleanliness, and only then clears the canonical claim. A later materialization advances the generation, so delayed operations from an old runtime fail.

A path supplied by the host is not proof of ownership. The box resolves its workdir from its own markers.

Claim fencing prevents stale Wi runtimes from operating on a replaced box. It is not a security sandbox. Isolation comes from the box driver and image, and the profile declares it.

## Pi configuration on remote workspaces

Pi configuration belongs to the runtime host.

For host Pi with a remote workspace:

- load the Wi host user's Pi settings, packages, extensions, skills, prompts, model credentials, and MCP configuration
- ignore any `~/.pi` inside the box
- do not require Pi in the box
- load repository instructions such as `AGENTS.md` from the claimed workspace
- initially ignore repository-local Pi settings, executable extensions, packages, skills, prompts, and MCP configuration

The Pi process needs a real host CWD that does not accidentally discover configuration from a stale checkout. The remote adapter supplies the logical workspace CWD and remote instructions.

Settings that name a local shell cannot be forwarded blindly. The box profile and image own shell selection, environment construction, and direnv activation inside the box.

Workspace-scoped tools must fail closed when their remote target is unavailable. They must never fall back to the Wi host filesystem.

## Persistent remote shells

A user shell differs from a Pi Bash tool call. Users expect shell state and scrollback to survive a network interruption. Each box runs one private tmux server, with a Wi-written configuration that disables its prefix and status line so it acts only as a process holder.

The Wi host still owns the user-facing work-item layout:

```text
host tmux shell window
  -> wi box attach (reconnects on transport loss)
     -> driver interactive exec
        -> box private tmux session
```

Editors and diff tools run inside these box terminals. Release closes them before destroying the box.

## Source transfer

A hosted workstation works best when repositories live on the host or are available through normal Git remotes.

Creating work from another machine requires explicit transfer. Options include:

- commit and push, then fetch on the host
- upload a Git bundle
- run the work in a box on that machine
- add a future client command that sends committed source state

Staged, unstaged, and untracked files must not be implied to transfer. Dirty workspace portability remains a separate artifact problem.

## High availability

A hosted workstation is one failure domain. Replication is a later design, not a property of SSH access.

A small highly available Wi service would need:

- one fenced active coordinator writer for each data root
- replicated database and native content with explicit consistency rules
- durable event cursors
- runtime ownership leases
- a stable network endpoint
- replaceable runtime hosts
- boxes that reject operations from superseded coordinator epochs
- backups and tested restoration

Do not place the current SQLite database on a shared network filesystem or run several daemons against it. Tmux sessions and Unix processes are not replicated state. After failover, a new runtime host may resume a conversation, but it cannot migrate a live Unix process.

Boxes reduce the amount of compute tied to the workstation and make future failover easier. They do not by themselves make the coordinator highly available.

## Historical work

The prior executor experiments remain as commits. No branch or tag currently points at them, so they are reachable only until Git garbage collection prunes unreferenced objects. Create refs if they must be kept:

- `28c1c73` worker-hosted remote executor snapshot (intended name `snapshot/worker-hosted-remote-executor`)
- `79d12f4` remote executor and driver protocol work in progress (intended name `snapshot/remote-executor-wip-79d12f42`)
- `e68972b` remote tmux attach experiment

Those versions ran Pi on the worker and synchronized runtime and conversation state back to the coordinator. The `remote-tool-routing` branch then kept Pi on the host but retained a worker daemon and item replica. Both are superseded by the box design. They remain useful evidence for Git bundle transfer, claim fencing, persistent worker shells, copy mode, and clipboard handling.

Agentbox's `remote-exec` implementation remains the main reference for host Pi with remote filesystem and shell tools. Pi's bundled SSH extension example provides a smaller reference implementation.

## Delivery order

A sensible sequence is:

1. Document and validate Wi on an always-on Linux host through ordinary SSH and host tmux.
2. Identify concrete gaps in reconnect, multi-client control, backups, source onboarding, and server installation.
3. Reduce the `remote-tool-routing` branch to the parts the box design keeps.
4. Ship local container boxes: Git seed and export, Pi tools through box operations, and box shell windows.
5. Ship SSH boxes for remote VMs and Macs, then a generic command driver for cloud sandboxes.
6. Add a restricted read-oriented web API and event stream.
7. Add authenticated mutations and optional browser terminal access.
8. Design coordinator failover only after storage, fencing, and recovery requirements are explicit.
