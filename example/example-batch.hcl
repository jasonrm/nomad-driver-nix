job "nix-example-batch" {
  datacenters = ["dc1"]
  type        = "batch"

  group "example" {
    # Simple example: run a binary from a Nixpkgs package.
    # By default, uses nixpkgs from the agent config (default_nixpkgs).
    # Override per-task with nixpkgs = "another flake" in config {}.
    task "nix-hello" {
      driver = "nix"

      config {
        # Entries starting with # are relative to the configured nixpkgs.
        # e.g. "#hello" becomes "github:nixos/nixpkgs/nixos-25.11#hello"
        packages = [
          "#hello"
        ]
        command = "hello"
      }
    }

    # Demonstrate curl with SSL/CA certificates. The driver includes cacert
    # and sets common trust-store environment variables by default.
    task "nix-curl-ssl" {
      driver = "nix"

      config {
        packages = [
          "#curl"
        ]
        command = "curl"
        args = [
          "https://nixos.org"
        ]
      }
    }

    # Use a flake defined from a template file.
    task "nix-hello-flake" {
      driver = "nix"

      config {
        packages = [
          ".#hello"
        ]
        command = "hello"
      }

      template {
        data = file("flake.nix")
        destination = "flake.nix"
      }
    }
  }
}
