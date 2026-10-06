# Remote execution research

Status: research snapshot from 2026-10-08 that informed [Remote execution boxes](remote-execution.md). Sources are cited inline. Statements marked **Inference** are reasoning, not sourced facts. Product details change quickly; re-check before relying on a specific API.

## Summary: keep tmux, define the protocol, defer the helper

The products closest to `wi`'s shape keep the agent loop and conversation on the host and send only tool execution to a box. Coder Agents does this, and so do container-use, Codex exec-server and the Pi SSH extensions. Every one of them that supports durable commands or reattaching to terminals runs **a process inside the box that outlives the transport**. That process hands out a handle (a PID or command ID) and keeps output in a buffer or log on the box side. E2B, Vercel, Daytona, Codex exec-server and Claude Desktop all work this way. Plain `ssh`/`docker exec` exec with no in-box holder gives you neither durability nor reliable cleanup. A no-PTY ssh command and a `docker exec` command both keep running when the client dies. So the candidate's v1 rule that agent commands die on disconnect is not free: wi has to kill them explicitly. The user's worry about reinventing things is half right. Wi should not write its own PTY holder or job daemon now, because a **tmux server already required in the box can be the transport-outliving holder for both shells and jobs**, driven by small `sh` scripts that wi sends over the exec prefix. Wi should, however, fix the shape of an executor protocol now: start, read-from-offset, write, signal, and a claim fence on every call. That way a later `wi` box-agent mode can replace the scripts without changing callers. On git, the candidate's bundle handoff matches the safest precedents. It is missing a quarantine ref namespace, hardened host-side git, a defined update path from host to box, and claim fencing on returned refs.

Sourced findings below carry inline citations. Statements labelled **Inference** are this report's reasoning, not something a source says.

## Tool execution: the mainstream builds a daemon with handles and log buffers

Remote agent systems use one of three architectures. In the first, the loop runs outside the box and only execution happens inside: E2B, Modal, Daytona, Vercel, OpenHands V0, Coder Agents, container-use. In the second, the whole agent runs inside the box: Claude Code cloud and self-hosted runners, Docker Sandboxes, Codex cloud, Cursor. The third is a local OS sandbox. **Coder Agents chose the first architecture on purpose.** "The agent loop runs in the control plane" and the workspace "has no knowledge of AI … no LLM API keys, no agent harnesses, and no special software installed". Tools such as `execute`, `process_list`, `process_signal` and `process_output` run over the existing workspace connection ([Coder Agents](https://coder.com/docs/ai-coder/agents)). This is `wi`'s model. Running the whole agent remotely, as Claude Desktop SSH and Codex `--remote` do, makes disconnects easy to survive. The cost is moving model credentials and conversation authority onto the box ([Claude Desktop SSH](https://claude.com/docs/third-party/claude-desktop/ssh-remote-sessions)), which `wi`'s constraints rule out.

Durable execution looks the same in every product that has it:

| System | Handle | Output retention | Behaviour on disconnect |
|---|---|---|---|
| E2B (envd daemon) | PID; `commands.connect(pid)`, `list()` | Streamed output goes **only to the originating client**; the docs say to redirect to files | Keeps running until its timeout; default timeout is 60 s ([E2B](https://docs.e2b.dev/commands/background)) |
| Vercel Sandbox | `cmdId`; `getCommand(cmdId)` | `logs()` iterator | Detached commands survive ([Vercel](https://vercel.com/docs/sandbox/sdk-reference)) |
| Daytona | session and command IDs | `getSessionCommandLogs`, also over WebSocket | Async session commands ([Daytona](https://www.daytona.io/docs/en/process-code-execution/)) |
| Codex exec-server | process ID | `process/read` with a **sequence cursor** and long-poll | **Kills processes when the WebSocket closes** unless a relay keeps the session ([codex exec-server](https://github.com/openai/codex/tree/main/codex-rs/exec-server)) |
| Claude Desktop SSH | engine per session | **16 MiB** rolling buffer, then the on-host transcript | Engine keeps running and the client catches up ([Claude Desktop SSH](https://claude.com/docs/third-party/claude-desktop/ssh-remote-sessions)) |
| pi-ssh | remote shell PID | none | `kill -- -pid` on abort; tied to the channel ([pi-ssh](https://github.com/hakergeniusz/pi-ssh)) |

E2B's caveat makes the key point: **reattaching without a server-side log does not recover output**. The minimal durable shape is a handle, an append-only log on the box, an exit-status record, and reading by offset. Two raw-transport facts change what "die on disconnect" means. A no-PTY `ssh host cmd` is orphaned rather than killed, and dies only if it later writes to the closed pipe (OpenSSH bug 396, [narkive mirror](https://openssh-bugs.mindrot.narkive.com/o3c8RzNh/bug-396-sshd-orphans-processes-when-no-pty-allocated); [theia-hq #71](https://github.com/theia-hq/services/pull/71)). **Killing the `docker exec` client does not signal the process in the container** ([moby #36029](https://github.com/moby/moby/issues/36029); [hermes-agent #140](https://github.com/Froraut/hermes-agent/issues/140)). Things that do survive are processes started with `setsid` and output redirected, `systemd-run --user` with linger, queue daemons like pueue, and session holders like tmux ([Margrop](https://blog.margrop.net/en/post/setsid-daemon-process-survival/); [ArchWiki systemd/User](https://wiki.archlinux.org/title/Systemd/User); [pueue](https://github.com/Nukesor/pueue)). systemd-logind `KillUserProcesses` defeats all of them unless the user is excluded or has linger enabled ([Claude Desktop SSH](https://claude.com/docs/third-party/claude-desktop/ssh-remote-sessions)).

Latency is the other reason products ship a helper. Emacs TRAMP, which has no server, costs **about 50–100 ms per operation**, and one Magit refresh triggers about 30 shell commands ([Core Dumped](https://coredumped.dev/2025/06/18/making-tramp-go-brrrr./)). pi-ssh reduces this with ControlMaster, so "every later call [is] a nearly free slave" ([pi-ssh](https://github.com/hakergeniusz/pi-ssh)).

**Inference.** For the candidate design, "commands die on disconnect" is acceptable for v1. Codex exec-server and pi-ssh have the same default. But wi must make it true with an explicit kill by recorded process group, because neither transport does it reliably. Durable jobs are not needed on day one for a local container, since there is no network to drop. They become necessary for remote SSH boxes, where laptop sleep outlasts ServerAlive timers ([shelbi #1080](https://github.com/jlong/shelbi/issues/1080)).

## Terminals: tmux in the box is the dependable holder, at a scrollback cost

Remote session holders fall into three tiers:

1. **Raw PTY holders.** dtach and abduco keep the shell alive but **lose output produced while detached**, and both are effectively unmaintained ([dtach](https://github.com/crigler/dtach); [abduco](https://github.com/martanne/abduco)).
2. **Pass-through holders with a shadow terminal model.** shpool and zmx send bytes straight to the outer terminal, so **local tmux scrollback and copy-mode keep working**. They repaint a snapshot only on reattach ([shpool](https://github.com/shell-pool/shpool); [zmx](https://github.com/neurosnap/zmx)). shpool allows a single client. zmx allows multiple clients but needs Zig 0.16 to build and has a known cursor issue with nested SSH ([bower.sh](https://bower.sh/you-might-not-need-tmux)).
3. **Full multiplexers.** tmux, screen and zellij own scrollback, which causes the double-prefix, double copy-mode and capability-stripping problems. Each of those needs configuration: `set-clipboard`, `allow-passthrough`, `terminal-features RGB`, `extended-keys` ([tmuxai](https://tmuxai.dev/tmux-allow-passthrough/); [tmux-sensible-revamped](https://github.com/tmux-revamped/tmux-sensible-revamped)).

The standard workaround for nested tmux is an outer toggle that sets `prefix None` ([samoshkin gist](https://gist.github.com/samoshkin/05e65f7f1c9b55d3fc7690b59d678734)). iTerm2 `-CC` and WezTerm SSH domains replace local tmux as the layout owner, so they conflict with `wi`'s design ([tmux Control Mode](https://github.com/tmux/tmux/wiki/Control-Mode); [WezTerm](https://wezterm.org/multiplexing.html)). mosh and Eternal Terminal fix the transport, not the holder. mosh has had no release since 2022 ([mosh 1.4.0](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.4.0)).

**Inference.** On technical fit, shpool or zmx is the best holder inside a local tmux pane. Neither meets the "guaranteed requirement" bar the way tmux does, because tmux is packaged everywhere and is already on the requirement list. Recommendation: run box-side tmux as a **dumb holder**. Use one tmux server on a wi-specific socket (`-L wi-<claim>`), with `prefix None`, `status off`, passthrough and clipboard on, and a box-side config shipped by wi. That removes the double-prefix problem. It leaves box-side scrollback owned by the inner tmux, which is the accepted cost. Keep the holder behind a named "attach command" in the box contract, so a later version can offer shpool or zmx as an option without redesigning. For a local container, the holder is optional in v1. A local tmux pane running `docker exec -it … sh` loses nothing that matters, because the container outlives the pane.

## Bootstrap: the industry ships helpers, but only behind version locks

Remote components get onto the box in one of three ways ([VS Code FAQ](https://code.visualstudio.com/docs/remote/faq); [Zed](https://zed.dev/docs/remote-development); [Mitogen](https://mitogen.networkgenomics.com/howitworks.html)):

1. **Downloaded or uploaded on demand and locked to the exact client version.** VS Code, Zed, JetBrains and Claude Desktop do this. Claude Desktop keeps three versions, about 1 GB, and defers upgrades for up to 7 days while sessions are active.
2. **Code shipped over the wire to a preinstalled interpreter.** Ansible and Mitogen need Python on the box. Ansible `raw` needs only a shell.
3. **A required preinstalled binary.** Codex remote must be on the login PATH ([Vaughan, secondary](https://codex.danielvaughan.com/2026/04/17/codex-remote-ssh-app-server-architecture/)).

VS Code's glibc ≥ 2.28 floor shows the cost of the first approach. Agent exec planes all have an in-box daemon: E2B envd, the Coder agent, DevPod's injected agent, and OpenHands' injected action server ([envd](https://github.com/e2b-dev/infra/tree/main/packages/envd); [Coder](https://coder.com/docs/admin/templates/troubleshooting); [DevPod](https://devpod.sh/docs/how-it-works/overview); [OpenHands](https://docs.openhands.dev/usage/architecture/runtime)). The Codex exec-server API is a usable reference for what such a daemon exposes: `process/start|read|write|terminate` with seq-numbered output and `fs/*` calls ([codex exec-server](https://github.com/openai/codex/tree/main/codex-rs/exec-server)).

**Inference.** `wi` can use the second approach with `sh` as the interpreter. Each executor operation is a short POSIX script that wi pipes through the exec prefix. Wi requires nothing beyond the declared `sh`, `git` and `tmux`, so there is no box-side version to drift. A "durable job" is then:

```
tmux -L wi-<claim> new-session -d -s job-<id> \
  "sh -c '<cmd>' >log/<id> 2>&1; echo \$? >log/<id>.exit"
```

followed by `tail -c +<offset> log/<id>` to read, and a process-group kill via a recorded PID to cancel. This is the E2B/Vercel handle-plus-log pattern built from tools the box already has. It also avoids needing `setsid`, which this research did not confirm is present on stock macOS. When latency or features call for it (file watches, chatty fs tools, PTY multiplexing over one stream), ship `wi` itself in a box-agent mode as the preinstalled binary, using the third approach. It must speak the same operation set, plus a version handshake that fails closed.

## Git handoff: bundles in, objects out, nothing executed on the host

Hosted agents use the forge as the handoff point. Local-first tools do not. **container-use** keeps a host-side bare repo as a `container-use` remote, seeds each environment by push, stores state in git notes, and runs all host git with `-c core.hooksPath=/dev/null` so it "must never trigger the user['s hooks]" ([repository.go](https://github.com/dagger/container-use/blob/main/repository/repository.go)). **Claude Code** seeds VMs with a git bundle when it can't clone. Its rules: under 100 MB, falling back to the current branch and then a squashed snapshot; credential-like files excluded; git ≥ 2.31 required; submodules, reftable and in-tree `include.path` refused. It also computes diffs from raw blobs so repo `textconv` drivers never run ([Claude Code cloud](https://code.claude.com/docs/en/claude-code-on-the-web)). Bundles can be cloned and fetched but **cannot be pushed into**. Incremental `old..new` bundles need `old` to already exist on the receiving side ([git-bundle](https://git-scm.com/docs/git-bundle)). Mutagen explicitly advises against syncing `.git` ([Mutagen VCS](https://mutagen.io/documentation/synchronization/version-control-systems)). Bind-mounted worktrees break in containers because of absolute `gitdir` paths ([devcontainers/cli #796](https://github.com/devcontainers/cli/issues/796)).

The security picture backs the candidate's choice to give the box its own clone. Repo-local `core.fsmonitor`, `core.hooksPath`, filter drivers and similar settings are **not** protected-config-only. Any host git command run inside a repo the box has written can therefore execute code the box planted. Only a short list of keys, such as `safe.directory` and `uploadpack.packObjectsHook`, is protected (local `git-config` man page, git 2.50.1). Clone-time CVEs keep appearing: CVE-2024-32002, CVE-2024-32465, and CVE-2025-48384, fixed in 2.50.1 and its backports ([NVD](https://nvd.nist.gov/vuln/detail/CVE-2024-32465); [Datadog](https://securitylabs.datadoghq.com/articles/git-arbitrary-file-write/)). The safest credential precedent puts **no forge credentials in the box**. container-use's box never pushes. Claude Code swaps a scoped credential for the real token in a proxy outside the VM. Dev Containers' default of forwarding the SSH agent is flagged as unsafe for hostile agents ([Anthropic sandboxing](https://www.anthropic.com/engineering/claude-code-sandboxing); [inspect_ai #575](https://github.com/meridianlabs-ai/inspect_ai/issues/575)). Claude Desktop forces `ForwardAgent` off on its session connection ([Claude Desktop SSH](https://claude.com/docs/third-party/claude-desktop/ssh-remote-sessions)).

**Inference.** The flow:

1. **Seed.** Seed with a host-made bundle and set the box's `origin` to the bundle path.
2. **Update host to box.** Send incremental bundles fetched into `refs/remotes/host/*`. The box rebases at its own pace, which avoids `updateInstead`'s clean-tree requirement.
3. **Return box to host.** Either the box writes an `out.bundle` that the host fetches into a quarantine namespace such as `refs/wi/box/<item>/<gen>/*`, or the host runs `git fetch` over a transport wrapper (`GIT_SSH`/`ext::` around the exec prefix) into that same namespace. Either way the host receives only a pack and never reads the box's config.

`wi merge` then rebases or fast-forwards from the quarantine ref under hardened options: `transfer.fsckObjects`, `core.hooksPath=/dev/null`, `core.fsmonitor=false`, and raw-blob diffs.

## What the candidate design is missing

The candidate is sound: config command prefixes, `sh` + `git` + `tmux`, Pi remote-exec, remote tmux, bundles, and die-on-disconnect. It matches the evidence. The gaps, all **Inference** drawn from the findings above:

| Gap | Why it matters | Minimal fix |
|---|---|---|
| Explicit kill on disconnect or cancel | `docker exec` and no-PTY ssh orphan children | Record the PGID per command; cancel with `kill -- -PGID` through the exec prefix; sweep on claim release |
| Claim fence inside the box | Remote ops must fail closed on stale claims | Box-side marker file `claim-id:gen`; every script checks it before acting; returned refs are namespaced by generation |
| Output to box-side files | Reattach without a log loses output (E2B) | All agent commands write to `log/<id>` even in v1; this enables later durability with no protocol change |
| Executor operation contract | Lets a `wi` box-agent replace the scripts later | Freeze the operations: start, read(offset), write, signal, status, plus fs ops. Model them on Codex exec-server |
| Requirements probe and version check | "Guaranteed requirements" still need verifying once | One round-trip probe at box create: versions of `sh`, `git` and `tmux`, cwd, writable non-`noexec` home; refuse if missing. Do not adapt behaviour to what is found |
| Transport liveness after sleep | ControlMaster silently dies across sleep | `ServerAliveInterval` 15–30 s, `ServerAliveCountMax` 3; on wake run `ssh -O check`/`-O exit`, then reattach the holder |
| logind and linger on Linux boxes | `KillUserProcesses` kills the tmux holder | Add linger, or exclusion from `KillUserProcesses`, to the documented requirements |
| Quarantine refs and hardened host git | Box-written config or hooks give host code execution | Only `refs/wi/box/*`; never run host git inside a box-written directory; `fsckObjects` on |
| Host-to-box update path and release policy | Bundles are read-only; uncommitted box work is lost on destroy | Incremental bundles; at release, refuse or auto-snapshot (a WIP commit into quarantine) before running destroy |
| Bundle edge cases | Submodules, LFS, reftable, size | Copy Claude Code's refusals and size fallback explicitly |
| Credentials and egress | Agent-forwarded keys give the box push authority | Forwarding off on wi's connections; no forge credentials in the box; network egress is a box-config concern, documented |
| Pi user `!` commands and path mapping | Only LLM tool calls are redirected ([pi-mono #528](https://github.com/badlogic/pi-mono/issues/528)); host and box paths differ | Decide how user bash commands route; define the cwd/path mapping per claim |
| Port forwarding | Dev servers inside the box | An optional `forward` prefix (`ssh -L`, `docker -p`), as Zed does ([Zed](https://zed.dev/docs/remote-development)) |
| Nested-tmux config | Prefix, clipboard and colour breakage | A wi-shipped box tmux config: `prefix None`, `status off`, passthrough, clipboard, RGB |

## Conclusion

The question "isn't this just ssh + tmux?" has a split answer. For **transport and the terminal holder**, yes. Every durable system needs something in the box that outlives the connection. tmux, which is already required, can be that holder for both the user shell and durable jobs, so wi needs no new box-side dependency. For the **protocol**, no. The pieces the candidate leaves implicit are where real products put their effort and where wi's claim model applies: command handles, offset-addressed logs, explicit group kill, claim fencing, and version handshakes. The ssh + tmux view hides these. The decision that holds up best is to write that protocol as wi's executor interface now, implement it with `sh` scripts over the configured prefix, and treat a `wi` box-agent binary as an optimisation for later, adopted when tool-call latency or file watching justifies it.

Several things remain unverified. Nothing in the notes measures per-call latency of `docker exec` or ssh against a resident helper. Whether `setsid` is present on stock macOS boxes was not checked. How shpool and zmx interact with local tmux's extended-keys was not confirmed. Daytona's repo status is also unconfirmed. The first two affect when the box-agent binary becomes worth building, so measure them on a real remote Mac before committing to it.
