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

Local-first deployment does not require every workspace to live on the Wi host. The selected scaling direction keeps Pi and its conversation on the Wi host while routing workspace-scoped tools to a claimed worker checkout:

```text
Wi host
  coordinator, Pi, conversation, tmux
      -> remote workspace adapter
         -> worker managed workspace

worker
  Git objects and managed slots
  file and process operations
  optional persistent user shells
```

This is useful for:

- large build machines
- cloud development sandboxes
- architecture-specific testing
- several workers behind one workstation
- local Wi installations that need occasional cloud compute

The agent process stays on the Wi host. Laptop sleep is safe when that host is an always-on server. A laptop-hosted Wi installation remains laptop-bound even if its tools run elsewhere.

## Tool execution semantics

Pi Bash calls are independent. Pi starts each call with a complete command and explicit working directory. Shell state such as `cd`, exports, aliases, and functions does not persist to the next call.

Remote workspace tools preserve that behavior:

- keep a persistent or multiplexed SSH transport to reduce connection setup
- start one addressed process for each Bash call
- run file operations as independent addressed requests
- keep tmux for persistent user shell terminals, not model tool calls

Injecting tool commands into a persistent tmux shell would create hidden mutable state, serialize calls, and complicate cancellation and output framing.

## Claim fencing

Every remote workspace operation must carry:

- work-item ID
- executor identity
- workspace claim ID
- claim generation
- repository and slot identity

The worker validates the claim before execution and holds a shared claim lease for the operation's lifetime. Release writes a revocation marker, rejects new operations, waits for active leases through an exclusive lock, and only then permits slot reuse.

A remote path supplied by the host is not proof of ownership. The worker resolves the path from its claim record.

Claim fencing prevents stale Wi runtimes from accidentally operating in reassigned slots. It is not a security sandbox. Bash may still reach other worker resources unless the executor also provides operating-system or container isolation.

## Pi configuration on remote workspaces

Pi configuration belongs to the runtime host.

For host Pi with a remote workspace:

- load the Wi host user's Pi settings, packages, extensions, skills, prompts, model credentials, and MCP configuration
- ignore the worker user's `~/.pi`
- do not require Pi on the worker
- load repository instructions such as `AGENTS.md` from the claimed workspace
- initially ignore repository-local Pi settings, executable extensions, packages, skills, prompts, and MCP configuration

The Pi process needs a real host CWD that does not accidentally discover configuration from a stale checkout. The remote adapter supplies the logical workspace CWD and remote instructions.

Settings that name a local shell cannot be forwarded blindly. The worker adapter owns shell selection, environment construction, and direnv activation.

Workspace-scoped tools must fail closed when their remote target is unavailable. They must never fall back to the Wi host filesystem.

## Persistent remote shells

A user shell differs from a Pi Bash tool call. Users expect shell state and scrollback to survive a network interruption. A worker may use a private tmux session for that shell.

The Wi host still owns the user-facing work-item layout:

```text
host tmux shell window
  -> SSH
     -> worker private shell tmux
```

Release must refuse or close persistent shells before reusing the slot.

## Source transfer

A hosted workstation works best when repositories live on the host or are available through normal Git remotes.

Creating work from another machine requires explicit transfer. Options include:

- commit and push, then fetch on the host
- upload a Git bundle
- register the other machine as an executor
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
- workers that reject commands from superseded coordinator epochs
- backups and tested restoration

Do not place the current SQLite database on a shared network filesystem or run several daemons against it. Tmux sessions and Unix processes are not replicated state. After failover, a new runtime host may resume a conversation, but it cannot migrate a live Unix process.

Remote workers reduce the amount of compute tied to the workstation and make future failover easier. They do not by themselves make the coordinator highly available.

## Historical work

The prior worker-hosted executor experiments remain available as Git refs:

- `snapshot/worker-hosted-remote-executor` at `28c1c73`
- `snapshot/remote-executor-wip-79d12f42` at `79d12f4`
- `remote-tmux-attach` at `e68972b`

Those versions ran Pi on the worker and synchronized runtime and conversation state back to the coordinator. The design remains useful evidence, especially for claims, slots, Git transfer, and persistent worker shells. It is no longer the default placement direction.

Agentbox's `remote-exec` implementation remains the main reference for host Pi with remote filesystem and shell tools. Pi's bundled SSH extension example provides a smaller reference implementation.

## Delivery order

A sensible sequence is:

1. Document and validate Wi on an always-on Linux host through ordinary SSH and host tmux.
2. Identify concrete gaps in reconnect, multi-client control, backups, source onboarding, and server installation.
3. Add claim-bound remote process and file operations.
4. Route Pi's workspace-scoped tools through that adapter.
5. Add remote managed-slot materialization and persistent worker shells.
6. Add a restricted read-oriented web API and event stream.
7. Add authenticated mutations and optional browser terminal access.
8. Design coordinator failover only after storage, fencing, and recovery requirements are explicit.
