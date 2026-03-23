job "nix-example-git-repo" {
  datacenters = ["dc1"]
  type        = "batch"

  group "example" {
    # Demonstrate using a git repository as a flake source via artifact stanza.
    # Nomad fetches the repo, then the nix driver builds the flake output.
    task "from-repo" {
      driver = "nix"

      artifact {
        source      = "git::https://github.com/user/my-flake"
        destination = "local/repo"
      }

      config {
        packages = ["local/repo#default"]
        command  = "my-app"
      }
    }
  }
}
