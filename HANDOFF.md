# Handoff: remote execution direction

## Current repository state

- Branch: `remote-tool-routing`
- HEAD: `f2367f3` (`Start agent runtimes with clean environments`)
- The working tree has a large uncommitted change set. Do not commit it without review.
- The installed development binary was rebuilt from this dirty tree with `scripts/install-local`.
- The Agentbox sandbox copy was also updated.

Historical implementations remain recoverable:

- `snapshot/worker-hosted-remote-executor` at `28c1c73`
- `snapshot/remote-executor-wip-79d12f42` at `79d12f4`
- The earlier remote tmux experiment exists as commit `e68972b`, although there is currently no branch pointing to it.

The worker-hosted snapshot matters because it contains tested Git transfer, slot claims, remote terminal persistence, copy mode, and clipboard work. It should remain history, not the default architecture.

## Product direction that is still agreed

Wi is local-first by host, not necessarily by laptop. One Wi host owns:

- the coordinator daemon and `wi.db`
- work items and lifecycle
- native Pi conversations
- Pi and driver processes
- runtime ownership
- user-facing tmux sessions

The host may be a laptop or an always-on Linux machine reached through normal SSH. Remote access to an always-on Wi host should be ordinary SSH attachment to that host's tmux server. It does not require a remote executor.

Remote execution has a narrower purpose. It lets a Wi host use another operating system, architecture, sandbox, or build machine for workspace operations. Examples include a Linux Wi host using macOS for iOS work, or a laptop using a larger build server.

Pi should stay on the Wi host. A worker should not own the primary conversation or run a second Pi process against it.

## What prompted the current discussion

Agentbox already provides most of the Pi-side remote execution behavior:

- replacement `bash`, `read`, `write`, `edit`, `grep`, `find`, and `ls` tools
- local-to-remote path mapping
- context-file discovery
- completion
- timeout and cancellation wrappers
- independent process calls rather than commands injected into a persistent shell

In the existing Agentbox workflow, Pi can already work in a remote target. The missing user experience is mostly that the nearby shell has to be connected manually. A Wi shell pane could enter the same target over SSH and optionally use a private remote tmux session for persistence.

This raises the central question:

> Why are we building a remote Wi daemon and a worker-side replica when the desired workflow is mostly Pi remote tools plus a shell attached to the same target?

The current conclusion is that a remote Wi daemon is probably the wrong design for this scope.

## What the current working tree implements

The branch was built by restoring the tested worker-hosted implementation and changing runtime placement in place.

The active runtime path now does the following:

```text
Wi host
  coordinator + Pi + conversation + host tmux
    -> embedded Agentbox-derived Pi extension
       -> wi internal-workspace-runtime
          -> per-runtime Unix socket
             -> executor client
                -> multiplexed SSH
                   -> wi internal-executor
                      -> remote managed workspace
```

Implemented behavior includes:

- `workspace.exec` in the executor protocol
- item, executor, claim ID, claim generation, repository key, slot, CWD, argv, stdin, environment, and timeout fields
- worker-side claim validation
- CWD restriction to the claimed workspace
- input and output limits
- process-group cancellation
- OpenSSH connection multiplexing
- shared operation locks and exclusive release locks
- a revocation marker that rejects new operations while release waits
- release receipts so a failed host-side Git import can retry the same worker release
- a private host-side Unix socket between Pi's helper command and the Wi application
- an OCI-shaped helper command used by the Agentbox remote-exec core
- embedded Pi extension materialization under the Wi cache
- host-owned conversations and runtime records for remote workspaces
- host-safe process CWDs instead of passing remote absolute paths to local `cmd.Dir`
- Git bundle transfer for materialization and release
- local TUI and headless runtime control
- remote workspace status combined with local agent status

The worker daemon still exists. It runs under an isolated worker XDG root and stores a local item replica. Workspace ensure, status, release, and tool execution use that daemon to reuse Wi's local workspace and claim code.

The branch also still contains substantial code inherited from the worker-hosted design:

- worker runtime and conversation handlers
- private worker tmux support
- terminal attachment helpers
- restricted authority endpoint and tunnel support
- executor-specific agent and driver configuration structures
- conversation snapshot code
- worker event synchronization code

Some dispatch and application paths for these features have been disabled, but the code remains. This is the main source of the current complexity.

The default shell window is host-local in the current host-owned runtime path. The earlier nested remote tmux behavior is not currently connected to that path.

## Why some Wi-specific code is still needed

The Agentbox extension does not manage Wi's workspace lifecycle. If Wi owns reusable remote worktrees, it still needs code for:

- creating or selecting a remote worktree
- transferring committed Git objects
- recording which item owns a slot
- rejecting operations from an old claim after slot reuse
- checking worktree status
- returning committed work to the Wi host
- opening a user shell in the same workspace

None of these requirements imply a remote daemon or worker-side work-item replica.

## Simpler architecture under discussion

The preferred direction is a stateless SSH worker helper:

```text
Wi host
  coordinator + Pi + conversation + tmux
    ├── Pi tools -> Agentbox remote-exec core
    │                -> Wi SSH adapter
    │                   -> stateless worker helper
    └── shell pane -> SSH
                       -> optional private remote tmux
                          -> same workspace

worker
  bare repositories
  managed worktree slots
  claim files
  lock files
  project tools
```

The worker would not run a Wi daemon. It would not have item replicas, conversations, runtime ownership, an authority tunnel, or agent configuration.

A worker helper may still be useful because raw shell composition is weak at:

- argument and output framing
- cancellation
- bounded transfers
- claim validation
- cross-process release locking
- safe path resolution

The helper can remain a short-lived `wi internal-executor` process started by SSH. It reads claim files and performs one typed operation. It does not become a daemon.

The minimum operation set would be:

- `doctor`
- `workspace.ensure`
- `workspace.status`
- `workspace.exec`
- `workspace.release`
- `item.delete` or equivalent cleanup
- `shutdown` only if persistent shell resources exist

Remote user shells are separate from model tool calls. A shell may use private remote tmux because users expect state and scrollback to survive reconnects. Pi tool calls must stay independent and use multiplexed SSH transport.

## An even smaller first version

There is an unresolved question about whether Wi should manage remote worktrees at all in the first version.

A bring-your-own-target version could record only:

- SSH destination
- remote workspace path or opaque Agentbox target
- remote shell command
- optional remote tmux identity

Wi would configure Pi's remote extension and open the shell pane in the same target. The user or Agentbox would own target creation and cleanup.

This version would solve the immediate usability problem with much less code. Wi-managed slots, Git bundles, and claim fencing could follow only if users need Wi to allocate and reuse remote workspaces.

## Decisions still open

### Who creates the remote workspace?

Options:

1. The user or Agentbox provides an existing target. Wi only connects Pi and the shell to it.
2. Wi creates a dedicated worktree at a configured remote root.
3. Wi supports both through separate executor adapters.

The first option is the smallest and matches the working Agentbox setup. The second preserves Wi's managed-workspace semantics.

### What is an executor in the domain model?

It may be either:

- a workspace placement selected by Wi, with a durable claim and generation, or
- a configured external target provider whose own identity and lifecycle remain opaque to Wi

Trying to force both into one worker-replica model caused much of the present complexity.

### Is claim fencing needed for existing targets?

Claim fencing is necessary when Wi reuses slots among items. It may not be necessary when a user provides a dedicated path or sandbox that another system owns.

The implementation should not impose reusable-slot machinery on every remote target.

### How should remote shells work?

The previous implementation proved that a private one-pane worker tmux can retain shell state and scrollback. The host tmux should still own navigation and layout.

A likely shell path is:

```text
host tmux shell pane
  -> SSH
     -> private remote tmux shell
        -> claimed workspace
```

This shell must not become Pi's tool transport.

### How should the Agentbox code be shared?

The branch currently copies the Agentbox TypeScript modules into `internal/agentdriver/pi/remoteexec`. The same author owns both repositories and both use the MIT license, but source duplication is still awkward.

Possible choices:

- keep the embedded copy and update it deliberately
- publish the shared core as a package
- move the generic core to Pi or another shared repository
- make Wi depend on a user-installed Agentbox extension and provide only target configuration

### How much remote Git transfer belongs in Wi?

The current Git bundle path transfers committed state and works without repository credentials on the worker. It does not transfer staged, unstaged, untracked, or ignored files.

For a first version, committed-only transfer is enough. Dirty workspace migration, cross-instance migration, checkpoint storage, and high availability are out of scope.

## Recommended next action

Do not add more behavior to the current worker-daemon implementation before resolving the product boundary.

The likely cleanup plan is:

1. Keep the Agentbox-derived remote tool integration and its Pi tests.
2. Keep the host-side target configuration and multiplexed SSH client.
3. Decide whether version one consumes existing targets or manages remote worktrees.
4. Replace worker daemon state with claim files and lock files if managed slots remain.
5. Remove worker item replicas, authority tunnels, worker runtime APIs, conversation synchronization, event synchronization, and worker agent configuration.
6. Reconnect only the remote shell path, with optional private worker tmux.
7. Rewrite the remote executor docs after the implementation boundary is settled.

Because the old implementation is preserved in Git, deleting unused worker-hosted code from this branch does not lose the design history.

## Validation completed on the current branch

The current implementation passed:

- `go test ./...`
- `go vet ./...`
- `git diff --check`
- `cd docs && npm run check`
- `cd docs && npm run build`

Additional manual checks passed:

- Pi loaded the remote extension from an unrelated temporary cache directory.
- A fake-SSH executor materialized a remote managed slot from a Git bundle.
- `workspace.exec` returned the expected exit code, stdout, and stderr.
- A stale claim generation was rejected.
- An operation using a released claim was rejected.
- Clean release returned committed Git state.

These checks show that the current implementation works. They do not answer whether its architecture is appropriate. That is the open discussion captured by this document.
