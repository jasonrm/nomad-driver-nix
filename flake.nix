{
  description = "Nix task driver for HashiCorp Nomad";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    {
      overlays.default = final: prev: {
        nomad-driver-nix = final.callPackage ./package.nix {
          rev = self.shortRev or self.dirtyShortRev or "unknown";
        };
      };
    }
    // flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };
      in
      let
        nomad = pkgs.nomad_1_11;
        cgoEnabled = if pkgs.stdenv.isDarwin then "0" else "1";

        nomadDevConfig = pkgs.writeText "nomad-dev.hcl" ''
          addresses {
            http = "127.0.0.1"
            rpc  = "127.0.0.1"
            serf = "127.0.0.1"
          }

          ports {
            http = 14646
            rpc  = 14647
            serf = 14648
          }

          client {
          }

          plugin "nix-driver" {
            config {
              default_nixpkgs = "github:nixos/nixpkgs/nixos-25.11"
            }
          }
        '';

        nomad-dev-build = pkgs.writeShellScriptBin "nomad-dev-build" ''
          set -euo pipefail
          VERSION="''${VERSION:-$(${pkgs.git}/bin/git describe --tags --always --dirty 2>/dev/null || echo "dev")}"
          echo "Building nix-driver (version: $VERSION, CGO_ENABLED=${cgoEnabled})..."
          CGO_ENABLED=${cgoEnabled} ${pkgs.go}/bin/go build \
            -ldflags "-X main.version=$VERSION" \
            -o nix-driver .
          echo "Build complete: ./nix-driver"
        '';

        nomad-dev-agent = pkgs.writeShellScriptBin "nomad-dev-agent" ''
          set -euo pipefail
          ${nomad-dev-build}/bin/nomad-dev-build
          ${pkgs.lib.optionalString pkgs.stdenv.isLinux ''
            if [ "$(id -u)" -ne 0 ]; then
              echo "Root required on Linux for nix driver isolation, re-executing with sudo..."
              exec sudo --preserve-env=PATH,NOMAD_ADDR "$(${pkgs.coreutils}/bin/realpath "$0")" "$@"
            fi
          ''}
          mkdir -p .nomad-dev-data
          echo "Starting Nomad dev agent on http://127.0.0.1:14646 ..."
          REALDIR="$(${pkgs.coreutils}/bin/realpath .)"
          exec ${nomad}/bin/nomad agent -dev \
            -config=${nomadDevConfig} \
            -plugin-dir="$REALDIR" \
            -data-dir="$REALDIR/.nomad-dev-data"
        '';

        nomad-dev = pkgs.writeShellScriptBin "nomad-dev" ''
          export NOMAD_ADDR="http://127.0.0.1:14646"
          exec ${nomad}/bin/nomad "$@"
        '';
      in
      {
        packages = {
          nomad-driver-nix = pkgs.callPackage ./package.nix {
            rev = self.shortRev or self.dirtyShortRev or "unknown";
          };
          default = self.packages.${system}.nomad-driver-nix;
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            nomad
            nomad-dev-build
            nomad-dev-agent
            nomad-dev
          ];

          env = {
            NOMAD_ADDR = "http://127.0.0.1:14646";
            CGO_ENABLED = cgoEnabled;
          };

          shellHook = ''
            echo ""
            echo "nomad-driver-nix development shell"
            echo "==================================="
            echo ""
            echo "Available commands:"
            echo "  nomad-dev-build  - Build the nix-driver plugin"
            echo "  nomad-dev-agent  - Build plugin and start a local Nomad dev agent"
            echo "  nomad-dev        - Run nomad commands against the local dev agent"
            echo ""
            echo "Quick start:"
            echo "  1. nomad-dev-agent       # in one terminal"
            echo "  2. nomad-dev run ./example/example-batch.hcl"
            echo ""
            echo "NOMAD_ADDR is set to $NOMAD_ADDR"
            echo ""
          '';
        };
      }
    );
}
