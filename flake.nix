{
  description = "Wi short-lived development tasks";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      perSystem = system:
        let
          pkgs = import nixpkgs { inherit system; };
          # A dirty Git tree still identifies its base commit. A non-Git source
          # has unknown identity; never invent a revision or release label.
          revision = self.rev or (nixpkgs.lib.removeSuffix "-dirty" (self.dirtyRev or ""));
          modified = if self ? rev then "false" else "true";
          wi = pkgs.buildGo126Module {
            pname = "wi";
            version = "unstable-${if revision == "" then "unknown" else builtins.substring 0 12 revision}${if modified == "true" then "-dirty" else ""}";
            src = self;
            vendorHash = "sha256-IAkxa6kvGN0NbcwQrYLAVAylnt1v3cvXu6Y028+oZEA=";
            subPackages = [ "cmd/wi" ];
            ldflags = [
              "-s" "-w"
              "-X=github.com/regb/workitem/internal/version.sourceRevision=${revision}"
              "-X=github.com/regb/workitem/internal/version.sourceModified=${modified}"
            ];
            # The check below runs every package, not just cmd/wi.
            doCheck = false;
            meta = {
              description = "Short-lived development tasks built on Git, Pi, and tmux";
              homepage = "https://github.com/regb/workitem";
              license = pkgs.lib.licenses.mit;
              mainProgram = "wi";
              platforms = systems;
            };
          };
          testTools = with pkgs; [ git tmux fzf direnv nodejs ];
          tests = wi.overrideAttrs (old: {
            pname = "wi-tests";
            nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ testTools;
            doCheck = true;
            # Generated test executables need store-backed interpreters too.
            postPatch = ''
              while IFS= read -r -d $'\0' file; do
                substituteInPlace "$file" \
                  --replace-quiet '#!/bin/sh' '#!${pkgs.runtimeShell}' \
                  --replace-quiet '#!/bin/bash' '#!${pkgs.bash}/bin/bash' \
                  --replace-quiet '#!/usr/bin/env ' '#!${pkgs.coreutils}/bin/env '
              done < <(find . -name '*_test.go' -print0)
            '';
            checkPhase = ''
              runHook preCheck
              export HOME="$TMPDIR/home"
              export XDG_CONFIG_HOME="$TMPDIR/config"
              export XDG_DATA_HOME="$TMPDIR/data"
              export XDG_CACHE_HOME="$TMPDIR/cache"
              export XDG_STATE_HOME="$TMPDIR/state"
              export XDG_RUNTIME_DIR="$TMPDIR/run"
              mkdir -p "$HOME" "$XDG_RUNTIME_DIR"
              # runtime.Caller-based fixtures require untrimmed source paths.
              export GOFLAGS="-mod=vendor"
              export TMPDIR=/tmp
              # See the Nix guide for these sandbox-specific exclusions.
              go test ./... -skip '^(TestHeadlessRuntimeUsesNativeControlWithoutTmux|TestCoordinatorWaitAndQueueReturnsAtomicPostTransitionSnapshot|TestRepositoryRegistrationThroughOperatorEndpoint|TestTmuxInputBracketedPasteOwnershipAndSteerKeys)$'
              runHook postCheck
            '';
          });
        in { inherit pkgs wi tests testTools; };
    in {
      packages = forAllSystems (system: let p = perSystem system; in {
        default = p.wi;
        wi = p.wi;
      });
      apps = forAllSystems (system: let app = {
        type = "app";
        meta.description = "Wi CLI";
        program = "${self.packages.${system}.wi}/bin/wi";
      }; in { default = app; wi = app; });
      checks = forAllSystems (system: { go-tests = (perSystem system).tests; });
      devShells = forAllSystems (system: let p = perSystem system; in {
        default = p.pkgs.mkShell {
          packages = [ p.pkgs.go_1_26 ] ++ p.testTools;
        };
      });
    };
}
