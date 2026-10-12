# Executor handoff

## Scope

Replace the WIP SSH worker executor with a small shared Docker/SSH executor implementation. There are no users or migration requirements. The user subsequently requested SSH on the same interface and lifecycle as Docker. Keep `research_notes/` untouched and untracked. The user authorized implementation, local Docker validation, and amending the WIP commit once implementation and validation are complete.

## Current implementation

Checkout kind `executor`, `[executors.<name>]`, and `--executor` select an independent clone per item, either in one Docker container or in a dedicated directory on an existing SSH host. Both backends share client operations, lifecycle orchestration, tool routing, job scripts, and Git exchange. Pi, conversations, database, coordinator, runtime ownership, and user-facing tmux remain on the host. Private executor tmux holds jobs and the interactive shell. No worker daemon, uploaded Wi binary, executor protocol, or conversation replicas remain.

See `docs/internal/remote-execution.md` for implementation paths and explicit limitations, and `docs/src/content/docs/guides/executors.md` for user instructions.

Claims are reserved before provisioning and retained on failure. Missing ready containers are not silently replaced. Shelve exports commits and retains dirty files. Release exports before destruction and retains destruction intent for retries. Git imports use quarantine refs; merge rebases in the executor and advances the host target without relocating the checkout.

The bundled Pi extension overrides seven coding tools and user Bash, routes them through a streaming runtime socket, stages writes with checksum verification, checks edit hashes, and injects executor project instructions.

## Validation

Docker runs through Colima on this development machine. Image `wi-executor-test` is built from `internal/executor/testdata/Dockerfile`. SSH acceptance tests use a real OpenSSH server in `wi-executor-ssh-test`, built from `internal/executor/testdata/Dockerfile.ssh`; test host/client keys are generated per server and never disable host-key checking.

```bash
TMPDIR=/tmp go test ./...
go vet ./...
WI_TEST_DOCKER=1 WI_TEST_SSH=1 TMPDIR=/tmp go test ./internal/app ./internal/executor -run 'Test(Docker|SSH)' -count=1
TMPDIR=/tmp go test -race ./internal/workspacetool ./internal/executor
cd docs && npm run check && npm run build && npm run test:site
```

The macOS default temporary directory can exceed Unix socket path limits, so use `TMPDIR=/tmp` for the full suite.

Shared Docker/SSH tests cover independent clone seed, in-executor commit/rebase, host merge, dirty release refusal, shelving, exported rematerialization, settled export, superseded ownership, destruction retry, streaming, cancellation, descendant termination, timeout, and real installed Pi tool APIs. Pi tests discover the installed package from `pi` on PATH. SSH tests also cover argument quoting, strict connection options, claim-scoped deletion, missing-workspace refusal, and actual interactive tmux attachment.

An isolated real CLI smoke test passed both headless and TUI startup, mode handoff, host Pi window, executor shell at `/workspace`, and executor copy mode. It used fresh HOME/XDG roots and a separate host tmux socket. No user panes or sessions were modified. Logs: `/tmp/wi-executor-cli-smoke.log`, `/tmp/wi-final-tests.log`, `/tmp/wi-docs-build.log`.

## Known limits

- Docker or existing SSH accounts only. No shared caches, forwarding, resident helper, or generic sandbox driver. SSH is not a sandbox; remote tools have the account's existing permissions. SSH root paths must be canonical and short enough for tmux sockets.
- Transport disconnect retains jobs but does not automatically reconnect the follower. A new runtime cancels unfinished old jobs.
- A killed short operation may leave a stale mkdir lock. Inspect and remove `/wi/lock` only after confirming no operation remains.
- Shell writers are not fenced against atomic edit/merge checks. Avoid concurrent edits during integration.
- Abort kills the job process group. Daemonized descendants may require destroying the container.
- Output limits terminate writers rather than providing a truncating ring buffer.
- No submodules, LFS pointers, reftable, or bundles larger than 256 MiB.
- Host Pi extensions remain trusted host code. An executor does not sandbox Pi itself.

## Build and review

The development binary has been installed with `scripts/install-local`, and the Agentbox sandbox copy updated when its tools directory is present. Final Go, vet, Docker, race, documentation, and whitespace checks pass. Interrupted-input tests verify that checksum failure leaves the original file intact. The user requested a new WIP checkpoint commit and push on `remote-tool-routing`, rather than amending the existing WIP commit. Keep `research_notes/` untracked.

Rerun checks and reinstall after any further executable edits. Review the diff before subsequent commits.
