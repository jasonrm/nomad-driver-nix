job "nix-example-slow-build" {
  datacenters = ["dc1"]
  type        = "batch"

  group "example" {
    # Forces a nix build that takes time, useful for testing
    # progress events in the Nomad UI.
    task "slow-build" {
      driver = "nix"

      config {
        packages = [
          "path:.#slow-hello",
          "nixpkgs#bash",
        ]
        command = "slow-hello"
      }

      template {
        data = <<-EOF
          {
            description = "A flake that forces a slow build for testing progress events";
            inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
            outputs = { self, nixpkgs }:
            let
              forAllSystems = nixpkgs.lib.genAttrs [
                "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin"
              ];
            in {
              packages = forAllSystems (system:
                let
                  pkgs = import nixpkgs { inherit system; };
                  slow-hello = pkgs.runCommand "slow-hello" {} ''
                    echo "Starting slow build..."
                    echo "Phase 1: simulating work..."
                    sleep 5
                    echo "Phase 2: more work..."
                    sleep 5
                    echo "Phase 3: finishing up..."
                    sleep 5
                    mkdir -p $out/bin
                    cat > $out/bin/slow-hello << 'SCRIPT'
                    #!/usr/bin/env bash
                    echo "Hello from the slow build!"
                    SCRIPT
                    chmod +x $out/bin/slow-hello
                  '';
                in {
                  inherit slow-hello;
                  default = slow-hello;
                }
              );
            };
          }
        EOF
        destination = "flake.nix"
      }
    }
  }
}
