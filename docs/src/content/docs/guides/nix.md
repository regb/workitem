---
title: Nix installation
description: Build, run, and pin Wi with an upstream flake.
---

Wi's flake provides `packages.default`, `packages.wi`, `packages.web-client`, `apps.default`,
`apps.wi`, `devShells.default`, and `checks.go-tests` for `x86_64-linux` and
`aarch64-darwin`. Enable Nix's `nix-command` and `flakes` experimental features.
The locked nixpkgs input supplies Go 1.26, satisfying Wi's Go 1.24 minimum.

## Build and run

From a checkout:

```sh
nix build .#default
./result/bin/wi version --json
nix run . -- --help
nix develop
nix flake check -L
```

New files in a Git checkout must be tracked for Nix to include them. For a
remote invocation, use `nix run github:regb/workitem/main -- --help`.
That command resolves main when invoked. Use a locked consumer input for a
repeatable installation.

The package contains the Wi executable and its embedded resources, including
the `wi web` browser client. The flake builds the client from
`web/package-lock.json` with Node.js (`packages.web-client`) and embeds it;
running Wi needs no Node.js. A plain `go build` or `go install` embeds only a
placeholder page: build the client first with `scripts/build-web`, as
`scripts/install-local` does. The package does not wrap PATH or install an
agent. Supply Git on PATH for repository operations,
tmux for interactive TUI runtimes, and fzf 0.71 or newer for `wi switch` and
`@` file mentions in `wi web`.
Headless operation needs neither tmux nor fzf. Install your chosen agent
separately and select its executable through Wi configuration or PATH. Optional
direnv integration also requires direnv. Plugins and editors may need their own
tools. No credentials, Fleet configuration, or Wi runtime state are packaged.

The development shell supplies Go, Git, tmux, fzf, direnv, and Node.js.
Node.js exercises the Pi bridge tests and builds the web client; it does not
install Pi or credentials.

## Pin a consumer

A minimal consumer flake can expose the upstream package for its own system:

```nix
{
  inputs.wi.url = "github:regb/workitem/main";

  outputs = { self, wi }: {
    packages = builtins.mapAttrs (system: packages: {
      default = packages.wi;
    }) wi.packages;
  };
}
```

Run `nix flake lock` in the consumer and commit its `flake.lock`. The lock pins
Wi's revision and its nixpkgs dependency. In a NixOS or Home Manager module that
receives the `wi` input, select `wi.packages.${pkgs.system}.wi` in
`environment.systemPackages` or `home.packages`. Unsupported systems have no
package output.

To advance the consumer to the current upstream main explicitly:

```sh
nix flake update wi
nix build .#default
# Review and commit the consumer's flake.lock, then apply its configuration.
```

No build or activation script fetches an unpinned HEAD. Updating this repository's
own toolchain is a separate `nix flake update nixpkgs` operation. After Go module
changes, update `vendorHash` in `flake.nix` using the hash reported by a build
with `vendorHash = pkgs.lib.fakeHash;`, then rebuild and run the checks.

## Version identity

The flake embeds source identity through `internal/version.sourceRevision` and
`internal/version.sourceModified` linker variables. It does not require `.git`
in the build sandbox. A pinned Git source reports the full revision in
`wi version --json`, a `devel+<12-character revision>` version, and
`modified: false`. These are development builds even when main happens to point
to a release commit.

Dirty Git sources report their known base revision and `modified: true`.
Sources without Git identity, including `path:` flakes, report `devel`, omit
the revision, and conservatively report `modified: true`. No commit timestamp
is invented from the snapshot modification time. Existing Go VCS discovery and
release linker flags still work for `scripts/install-local` and
`scripts/release-local`.

## Checks and platform coverage

`nix flake check` runs `go test ./...` in a separate derivation, with isolated
HOME and XDG directories and the repository's existing process-test isolation.
It uses vendored, hashed dependencies without network access. Test-generated
script shebangs are patched to Nix store interpreters in the check source only.
Test builds disable path trimming because real-binary fixtures use
`runtime.Caller` to find the repository. No live agent or tmux server is needed.
The credential-dependent native Pi smoke test remains opt-in and is not enabled.

Thirteen tests are excluded from this sandbox check:

- `TestHeadlessRuntimeUsesNativeControlWithoutTmux` expects PATH to be unchanged,
  but the implementation adds the configured `/usr/local/bin/wi` directory.
  That directory is absent from the sandbox's initial PATH.
- `TestCoordinatorWaitAndQueueReturnsAtomicPostTransitionSnapshot` and
  `TestRepositoryRegistrationThroughOperatorEndpoint` create socket paths from
  long test names that exceed Unix socket path limits in the sandbox.
- `TestTmuxInputBracketedPasteOwnershipAndSteerKeys` uses process group 1 as an
  invalid owner, but the Linux Nix sandbox can itself use process group 1.
- `TestAgentProjectTrustRequiresVerifiedRegisteredManagedWorktree`,
  `TestSetAgentSettingsAppliesLiveWhereTheRuntimeCan`,
  `TestSetAgentSettingsWaitsForAStartingRuntime`,
  `TestStructuredControlRequiresCurrentControllerAndLeaseGeneration`,
  `TestAgentExecHoldsLockAndRunsPiWithExplicitRootConversationLocator`,
  `TestAgentExecTUIDriverUsesPublicControlAndEventProtocol`,
  `TestExternalObserverVerifiesRuntimeAndBuildsAgentProjection`,
  `TestSingleProcessAndSnapshotAgreeOnIdentity` and
  `TestDarwinIdentityIgnoresTheCallersLocale` verify process identity, which
  on macOS reads `/bin/ps`. The macOS sandbox does not
  allow running that setuid program. Linux reads `/proc` and runs them.

These exclusions do not affect ordinary `go test ./...` or the existing Go CI
job. The Nix CI workflow builds, verifies clean revision provenance, exercises
the development shell, and runs checks on Linux and Apple Silicon macOS. Local
Linux validation does not establish a successful Darwin build; that requires
the macOS CI job or a Darwin builder. `nix flake check --all-systems --no-build`
can evaluate both systems without a Darwin builder.

## Choose one executable

For a Nix-managed installation, remove any old local Wi executable from the
active PATH, or put the Nix profile first. For local development, keep
`scripts/install-local` and put its GOBIN or GOPATH/bin first instead. Check
`type -a wi` and `wi version --json` before using it. Do not install into a Nix
store path. Installing or updating a package does not restart existing Wi
sessions; manage those separately when intended.
