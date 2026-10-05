# Remote agent placement alternatives

Status: architecture exploration. No placement change has been approved or implemented.

The current remote executor design places both the workspace and coding-agent process on the executor. An alternative is to keep Pi and its native conversation on the coordinator while routing Pi's filesystem and shell tools to the remote workspace.

This distinction matters:

```text
workspace placement != agent process placement
```

The existing model treats them as the same executor. They do not have to be.

## Historical findings

There is no evidence in the Workitem repository that coordinator-hosted Pi with remote tool routing was implemented, tested, or explicitly rejected.

The relevant history is:

- The unreachable WIP commit `79d12f42` and its untracked-file companion contain the first remote executor implementation. They launch Pi on the worker.
- Branch `remote-tmux-attach`, commit `e68972b`, also launches Pi on the worker. Its alternative was terminal attachment: the user's terminal detached from local tmux and attached directly to worker tmux, avoiding nested tmux.
- The current local-gateway and private-worker-tmux topology evolved from that worker-hosted Pi design.
- No Workitem ref contains the Agentbox `remote-exec` tool-routing implementation or a Wi-specific equivalent.

The Agentbox history shows that the underlying idea was available around the same time:

- `08dc2b1`, 2026-08-04: initial local Pi container tool-routing extension
- `f47c463`, 2026-09-04: local Pi routed into Scalium sandboxes
- `602a97d`, 2026-09-13: shared `remote-exec` core
- `e68972b`, 2026-09-14: Workitem worker-hosted remote executor commit

The close dates do not prove that the Wi design considered it. The code and history contain no recorded comparison or rejection.

## Current worker-hosted model

```text
coordinator
  canonical daemon
  local gateway tmux
    agent pane -> SSH -> worker tmux -> worker Pi
    shell pane -> SSH -> worker tmux -> worker shell

executor
  managed worktree
  worker daemon and item replica
  Pi driver and conversation
  agent tmux session
  shell tmux sessions
```

This model gives Pi a native remote working directory. Every Pi extension and process naturally runs on the executor. It also creates several synchronization problems:

- native conversations need export and import
- runtime events need synchronization
- Pi versions and extensions must match
- model credentials or provider configuration may need to exist remotely
- the agent TUI crosses SSH and nested tmux
- remote copy mode and clipboard need explicit bridging
- workspace movement must move both workspace and conversation ownership

## Coordinator-hosted Pi model

```text
coordinator
  canonical daemon
  local Pi process and native conversation
  local runtime bridge and semantic events
  local tmux agent TUI
      -> remote tool transport over SSH
           -> executor managed worktree
  local shell gateway
      -> SSH -> persistent worker shell tmux

executor
  managed worktree
  worker daemon and item replica
  fenced command and file operations
  persistent shell tmux sessions
  no Pi process or agent tmux session
```

Pi remains local. Its built-in tools operate on the remote worktree through an extension. User shell panes remain remote and persistent because they are independent workspace access resources.

## Existing Agentbox implementation

`~/vcs/opensource/agentbox/pi/extensions/remote-exec/` already implements most of the Pi-facing work.

It overrides:

- read
- write
- edit
- bash
- ls
- find
- grep
- interactive user bash commands
- path completion
- clipboard-image placement
- project context-file loading

It handles streaming shell output, cancellation, timeouts, process cleanup, path mapping, target validation, and one retry after target disappearance.

The shared core expects an OCI-shaped runtime command:

```text
RUNTIME container inspect TARGET
RUNTIME container inspect -f {{.State.Running}} TARGET
RUNTIME exec [-i] [-w CWD] [BACKEND_ARGS...] TARGET COMMAND...
```

Container, Agentbox, and Scalium wrappers implement that contract. Wi would need either:

- a Wi-specific wrapper around the shared core
- a local runtime adapter that maps the OCI-shaped calls to fenced executor SSH operations
- a dedicated SSH implementation using the same Pi tool-operation abstractions

Pi also ships a simpler `examples/extensions/ssh.ts`, but it covers fewer tools and lacks much of the Agentbox implementation's recovery, context, completion, and cleanup behavior.

## Main benefits

### Conversations stay local

Pi writes directly to the coordinator's canonical native conversation file. There is no conversation export, full-file checkpoint, format negotiation between hosts, or executor-side conversation replica.

The existing local conversation importer and attention machinery continue to work without SSH.

### Runtime control and events stay local

The Wi Pi bridge connects to local control and event sockets. Prompt, steer, follow-up, abort, shutdown, readiness, and settled events use the existing local path.

Durable event cursors between worker and coordinator are no longer needed for Pi runtime events. Worktree observations still need remote synchronization.

### Pi configuration is singular

The coordinator owns:

- Pi version
- model provider credentials
- Pi extensions
- driver bridge version
- user-level Pi configuration
- conversation storage

Workers no longer need Pi or driver installation. `executor doctor` can focus on Git, the remote shell, workspace tools, and the executor protocol.

### The agent TUI becomes ordinary local tmux

The Pi TUI runs in the local work-item agent window. Agent scrollback, copy mode, clipboard, resize behavior, and rendering use one tmux server.

Private worker tmux remains only for persistent user shell panes. The nested agent tmux path disappears.

### Workspace moves become simpler for Pi

The conversation does not move. A move quiesces local Pi tool execution, transfers the workspace, advances the claim, changes the extension target, and resumes the same local conversation.

Dirty workspace portability remains necessary, but conversation portability does not.

### Network loss has a smaller effect

Pi remains alive and keeps its TUI and conversation. An in-flight remote tool call fails or retries. Reconnecting the workspace transport does not require reattaching the agent process.

## Problems this does not solve

### Dirty workspace durability

The only live copy of uncommitted files still lives on the executor. Permanent worker loss can still lose them. This architecture removes conversation checkpointing from that problem, but it does not answer whether dirty workspace recovery is required.

### Remote project processes

Builds, tests, servers, and tool subprocesses run remotely. Long-lived processes need clear cancellation and ownership rules. A command that daemonizes can outlive the SSH tool call just as it can outlive a worker-hosted Pi tool call.

### Persistent user shells

User shell panes still need worker tmux because they are interactive contexts that must survive SSH loss. This is acceptable and much smaller than using worker tmux for the Pi TUI.

## Design problems to solve

### Placement must split into two fields

The current runtime model uses `Executor` as if the workspace and agent process share a host. Coordinator-hosted Pi needs distinct concepts:

```text
workspace executor: netfabric-macbook
runtime host: coordinator
```

The runtime must still bind to the remote workspace claim ID and generation. A local process must lose tool authority as soon as that claim is released or superseded.

### Every remote tool call needs claim fencing

A raw command such as `ssh host 'cd /slot && ...'` is unsafe. A delayed local Pi process could continue writing after the slot was released and assigned to another item.

Each operation should carry:

- item ID
- executor name
- workspace claim ID
- claim generation
- expected workspace path or slot identity

The worker must validate that identity immediately before executing the file or shell operation.

The Agentbox core's opaque target identifier can represent a fenced Wi target, but its runtime adapter must enforce the claim on every `exec` call.

### Local and logical working directories differ

Pi requires a real local process working directory for session grouping and resource discovery. The remote worktree path may not exist on the coordinator and may use another operating system's path syntax.

The driver launch contract needs to distinguish:

- host process CWD
- logical workspace CWD shown to the model
- remote tool target CWD

Using the remote path as `cmd.Dir` will fail. Creating symlinks to pretend the remote tree is local is not acceptable.

### Project resource discovery changes

The Agentbox core fetches remote `AGENTS.md` and `CLAUDE.md` files and rewrites the system prompt. It does not automatically load every remote project resource into local Pi.

We must define behavior for:

- project `.pi` settings
- project Pi extensions
- project skills and prompt templates
- MCP server configuration
- files referenced by local global skills

One reasonable policy is to keep trusted Pi configuration local while loading remote repository context and executing coding tools remotely. That policy must be explicit because it differs from launching Pi inside the repository on the worker.

### Tool coverage must be complete and fail closed

The Agentbox core routes Pi's built-in coding tools. Other extensions may register tools that access the local filesystem or spawn local commands.

Wi needs a declared remote-workspace capability. Options include:

- allow only audited remote-safe extensions
- let extensions declare whether they operate locally or through the workspace adapter
- disable unknown filesystem or process tools for remote items
- keep worker-hosted runtimes for drivers that cannot route tools

A tool must never silently fall back to the coordinator filesystem because the remote target is unavailable.

### Remote environment and direnv

The local Pi process should keep model credentials and local Pi settings. Remote commands need the worker workspace environment, including approved direnv state and project tool paths.

The worker command adapter should construct this environment. Forwarding the coordinator process environment wholesale would leak host-specific paths and secrets.

### Transport latency and reuse

Pi tools can make many small filesystem calls. Starting a new SSH connection for each one would be slow. The runtime adapter should use a coordinator-owned SSH control connection or a persistent typed channel.

Cancellation must terminate the remote process group, not only the local SSH client.

### Driver abstraction

This approach is easy for Pi because Pi supports tool overrides. It is not automatically available to every coding agent.

The driver protocol should advertise a capability such as coordinator-hosted runtime with remote workspace operations. Drivers without that capability could remain worker-hosted or be unavailable for remote workspaces.

Do not put Pi-specific tool names into the generic workspace or executor model.

## Security comparison

Worker-hosted Pi keeps model-controlled tools and agent extensions on the executor. Coordinator-hosted Pi runs the trusted Pi process and extensions on the coordinator, while model-callable workspace tools cross a fenced adapter.

Coordinator hosting can keep provider credentials off the worker. It also means a badly behaved local extension has coordinator permissions. Extensions are already trusted code, but the remote-workspace policy must prevent model-callable tools from accidentally reaching coordinator files or commands.

The remote adapter should fail closed if target validation, claim validation, or environment setup fails.

## Suggested spike

Do not replace the current executor implementation before validating the tool-routing path.

Build a narrow Pi-only spike on the existing `netfabric-macbook` item or a disposable test item:

1. Keep the managed slot and worker shell sessions unchanged.
2. Launch Pi locally with the Wi bridge and a Wi-specific remote-exec extension.
3. Route read, write, edit, bash, ls, find, grep, user bash, context files, path completion, and clipboard images to the worker.
4. Run both TUI and headless modes.
5. Exercise prompt, steer, follow-up, abort, and shutdown through local runtime sockets.
6. Test direnv and project tool discovery remotely.
7. Test a configured local Pi extension and a project-local Pi extension.
8. Interrupt SSH during read, write, edit, and a long shell command.
9. Release and reuse the slot while the old Pi process remains alive. Require every stale tool call to fail.
10. Measure latency with and without SSH connection reuse.
11. Confirm the Pi session and attention projections remain entirely local.
12. Confirm remote shell panes still persist and scroll independently.

The spike should not introduce compatibility aliases or make raw remote paths valid local directories.

## Decision criteria

Prefer coordinator-hosted Pi if the spike proves:

- complete and fail-closed tool routing
- acceptable latency
- correct remote context and environment
- claim fencing on every operation
- compatibility with the required Pi extensions and workflows
- simpler terminal and conversation behavior in practice

Keep worker-hosted Pi where a driver cannot route its workspace operations or where execution requires the entire agent process and extension ecosystem to live beside the workspace.

A hybrid architecture may be the right result. Workspace placement and runtime placement should remain separate even if Pi defaults to coordinator-hosted execution.
