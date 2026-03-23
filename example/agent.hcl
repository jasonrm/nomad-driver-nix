#log_level = "TRACE"

client {
}

plugin "nix-driver" {
  config {
    default_nixpkgs = "github:nixos/nixpkgs/nixos-25.11"
    allow_privileged = true

    # Remote builders (each entry is a Nix builder specification)
    # builders = ["ssh://builder@linux-box x86_64-linux - 4 1 big-parallel"]

    # Additional binary caches
    # extra_substituters = ["https://cache.example.com"]

    # Public keys for verifying the additional binary caches
    # extra_trusted_public_keys = ["cache.example.com-1:xyzabc123="]

    # Script to run after each successful build (e.g. sign and upload to cache)
    # post_build_hook = "/etc/nix/post-build-hook.sh"

    # Netrc file for HTTP authentication to private binary caches
    # netrc_file = "/etc/nix/netrc"

    # Per-namespace overrides (merged with globals above)
    # namespace "production" {
    #   builders = ["ssh://builder@prod-box x86_64-linux - 8 1 big-parallel"]
    #   extra_substituters = ["https://prod-cache.example.com"]
    #   extra_trusted_public_keys = ["prod-cache-1:abc="]
    #   post_build_hook = "/etc/nix/prod-post-build-hook.sh"
    #   netrc_file = "/etc/nix/prod-netrc"
    # }
  }
}
