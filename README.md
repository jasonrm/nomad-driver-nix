# nomad-driver-nix

A [Nomad](https://www.nomadproject.io/) task driver that runs Nix-built packages with filesystem isolation.

On Linux, tasks run inside libcontainer-based isolation (same mechanism as Nomad's `exec` driver) with only the Nix closure bind-mounted into the container. On macOS, tasks are optionally sandboxed via `sandbox-exec` with a generated Seatbelt profile restricting file access to the Nix closure and task directory.

## Requirements

- [Go](https://golang.org/doc/install) 1.22+ (to build)
- [Nomad](https://www.nomadproject.io/downloads.html) 1.11+ (to run)
- [Nix](https://nixos.org/download.html) 2.11+ with flakes enabled

### Platform notes

- **Linux**: Requires root. Uses libcontainer for PID/IPC/filesystem isolation with cgroups resource limits.
- **macOS**: Runs without root. Optionally uses `sandbox-exec` for file access restriction. PID/IPC isolation options are ignored.

## Installation

### Using the flake overlay (NixOS / nix-darwin)

Add the flake as an input and apply the overlay:

```nix
{
  inputs.nomad-driver-nix.url = "github:jasonrm/nomad-driver-nix";

  outputs = { self, nixpkgs, nomad-driver-nix, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        {
          nixpkgs.overlays = [ nomad-driver-nix.overlays.default ];
          # Then use pkgs.nomad-driver-nix wherever you need the plugin
        }
      ];
    };
  };
}
```

### Direct package reference

Reference the package directly from the flake without an overlay:

```nix
nomad-driver-nix.packages.${system}.nomad-driver-nix
```

### Building from source

```sh
make build
```

This produces a `nix-driver` binary. Version is injected from git tags:

```sh
VERSION=0.2.0 make build
```

## Agent configuration

The plugin is configured in the Nomad agent config under `plugin "nix-driver"`. See [`example/agent.hcl`](example/agent.hcl) for a full example.

```hcl
plugin "nix-driver" {
  config {
    # Nixpkgs flake ref used when packages start with "#"
    default_nixpkgs = "github:nixos/nixpkgs/nixos-25.11"

    # Allow tasks to bind-mount host directories (default: true)
    allow_bind = true

    # Allow privileged containers (default: false)
    allow_privileged = false

    # Remote builders — each entry is a Nix builder specification
    # builders = ["ssh://builder@linux-box x86_64-linux - 4 1 big-parallel"]
    builders = []

    # Additional binary caches
    # extra_substituters = ["https://cache.example.com"]
    extra_substituters = []

    # Public keys for verifying the additional binary caches
    # extra_trusted_public_keys = ["cache.example.com-1:xyzabc123="]
    extra_trusted_public_keys = []

    # Script to run after each successful build (e.g. sign and upload to cache)
    # post_build_hook = "/etc/nix/post-build-hook.sh"

    # Netrc file for HTTP authentication to private binary caches
    # netrc_file = "/etc/nix/netrc"

    # Linux-only: isolation defaults
    default_pid_mode = "private"  # or "host"
    default_ipc_mode = "private"  # or "host"
    no_pivot_root    = false

    # Linux-only: allowed Linux capabilities
    allow_caps = ["audit_write", "chown", "dac_override", ...]

    # Per-namespace overrides — these are merged with the global settings above.
    # Supports: builders, extra_substituters, extra_trusted_public_keys,
    #           post_build_hook, netrc_file, allow_privileged
    # namespace "production" {
    #   builders                  = ["ssh://builder@prod-box x86_64-linux - 8 1 big-parallel"]
    #   extra_substituters        = ["https://prod-cache.example.com"]
    #   extra_trusted_public_keys = ["prod-cache-1:abc="]
    #   post_build_hook           = "/etc/nix/prod-post-build-hook.sh"
    #   netrc_file                = "/etc/nix/prod-netrc"
    #   allow_privileged          = false
    # }
  }
}
```

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `default_nixpkgs` | `string` | `"github:nixos/nixpkgs/nixos-25.11"` | Flake ref prepended to `#`-prefixed package entries |
| `allow_bind` | `bool` | `true` | Allow tasks to bind-mount host directories |
| `allow_privileged` | `bool` | `false` | Allow privileged containers |
| `builders` | `list(string)` | `[]` | Remote Nix builder specifications |
| `extra_substituters` | `list(string)` | `[]` | Additional binary caches |
| `extra_trusted_public_keys` | `list(string)` | `[]` | Public keys for the additional binary caches |
| `post_build_hook` | `string` | `""` | Script to run after each successful build |
| `netrc_file` | `string` | `""` | Netrc file for HTTP authentication to private caches |
| `default_pid_mode` | `string` | `"private"` | Linux-only: default PID namespace mode (`"private"` or `"host"`) |
| `default_ipc_mode` | `string` | `"private"` | Linux-only: default IPC namespace mode (`"private"` or `"host"`) |
| `no_pivot_root` | `bool` | `false` | Linux-only: disable pivot_root in the container |
| `allow_caps` | `list(string)` | *(default capability set)* | Linux-only: allowed Linux capabilities |
| `namespace "<name>" { }` | block | — | Per-namespace overrides for `builders`, `extra_substituters`, `extra_trusted_public_keys`, `post_build_hook`, `netrc_file`, `allow_privileged` |

## Task configuration

```hcl
task "example" {
  driver = "nix"

  config {
    # Nix packages to install. Entries starting with "#" are expanded using
    # the configured nixpkgs (e.g. "#hello" -> "github:nixos/nixpkgs/nixos-25.11#hello").
    # Other entries are full flake references.
    packages = ["#hello", "#curl"]

    # Command to run (must be on PATH from installed packages)
    command = "hello"
    args    = ["-g", "Hi from Nix!"]

    # Override nixpkgs for this task
    nixpkgs = "github:nixos/nixpkgs/nixos-25.11"

    # macOS: enable sandbox-exec file access restriction (default: true)
    sandbox = true

    # Host path -> container path bind mounts
    bind           = [{ "/data" = "/mnt/data" }]
    bind_read_only = [{ "/etc/hosts" = "/etc/hosts" }]

    # Linux-only (ignored on macOS with a warning)
    pid_mode = "private"
    ipc_mode = "private"
    cap_add  = ["net_bind_service"]
    cap_drop = []
  }
}
```

| Option | Type | Required | Default | Description |
|--------|------|----------|---------|-------------|
| `command` | `string` | yes | — | Command to run (must be on PATH from installed packages) |
| `args` | `list(string)` | no | `[]` | Arguments passed to the command |
| `packages` | `list(string)` | no | `[]` | Nix packages to install; `#`-prefixed entries are expanded with the configured nixpkgs |
| `nixpkgs` | `string` | no | *(agent default)* | Override the nixpkgs flake ref for this task |
| `sandbox` | `bool` | no | `true` | macOS: enable `sandbox-exec` file access restriction |
| `bind` | `list(map(string))` | no | `[]` | Read-write host bind mounts (`{ "/host/path" = "/container/path" }`) |
| `bind_read_only` | `list(map(string))` | no | `[]` | Read-only host bind mounts |
| `pid_mode` | `string` | no | *(agent default)* | Linux-only: PID namespace mode (`"private"` or `"host"`) |
| `ipc_mode` | `string` | no | *(agent default)* | Linux-only: IPC namespace mode (`"private"` or `"host"`) |
| `cap_add` | `list(string)` | no | `[]` | Linux-only: additional Linux capabilities |
| `cap_drop` | `list(string)` | no | `[]` | Linux-only: capabilities to drop |

## Examples

### Batch job

```hcl
job "nix-example-batch" {
  type = "batch"

  group "example" {
    task "hello" {
      driver = "nix"

      config {
        packages = ["#hello"]
        command  = "hello"
      }
    }
  }
}
```

### SSL/CA certificates

Include `#cacert` and set the `SSL_CERT_FILE` environment variable:

```hcl
task "curl-ssl" {
  driver = "nix"

  config {
    packages = ["#curl", "#cacert"]
    command  = "curl"
    args     = ["https://nixos.org"]
  }

  env = {
    SSL_CERT_FILE = "/etc/ssl/certs/ca-bundle.crt"
  }
}
```

### Service job

```hcl
job "nix-example-service" {
  type = "service"

  group "example" {
    task "go-httpbin" {
      driver = "nix"

      config {
        packages = ["#go-httpbin"]
        command  = "go-httpbin"
        args     = ["-port", "8080"]
      }
    }
  }
}
```

### Git repository as flake source

Use Nomad's `artifact` stanza to fetch a git repo, then reference it as a local flake:

```hcl
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
```

### Inline flake via template

Use Nomad's `template` stanza to write a `flake.nix` into the task directory, then reference it with a local path:

```hcl
task "inline-flake" {
  driver = "nix"

  config {
    packages = ["path:.#my-package"]
    command  = "my-package"
  }

  template {
    data = <<-EOF
      {
        description = "Inline flake example";
        inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
        outputs = { self, nixpkgs }:
        let
          pkgs = import nixpkgs { system = "x86_64-linux"; };
        in {
          packages.x86_64-linux.my-package = pkgs.hello;
          packages.x86_64-linux.default = self.packages.x86_64-linux.my-package;
        };
      }
    EOF
    destination = "flake.nix"
  }
}
```

See [`example/`](example/) for more working examples.

## Nix store garbage collection

The driver creates a Nix profile in each task's allocation directory. When Nomad garbage-collects a completed allocation, the profile symlink is removed, which unroots the store paths. However, the Nix store paths themselves remain on disk until `nix store gc` runs.

Without periodic garbage collection, the Nix store will grow indefinitely. On NixOS, enable automatic GC:

```nix
{
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 7d";
  };
}
```

Or run it manually:

```sh
nix store gc
```

## Cross-platform behavior

| Feature | Linux | macOS |
|---------|-------|-------|
| Nix package installation | Yes | Yes |
| Filesystem isolation | libcontainer (bind mounts) | sandbox-exec (SBPL profile) |
| PID/IPC namespace isolation | Yes | No (ignored) |
| Cgroup resource limits | Yes | No |
| Root required | Yes | No |
| Capabilities (cap_add/drop) | Yes | Ignored |

## How it works

1. **Build profile**: `nix profile install` creates a merged profile from the specified packages
2. **Compute closure**: `nix path-info --recursive` on the profile determines all required store paths
3. **Execute**:
   - Linux: bind-mount closure paths + profile directories into an isolated container, run the command
   - macOS: generate an SBPL sandbox profile allowing access to closure paths, wrap command with `sandbox-exec`

## Development

Enter the dev shell with `nix develop`. The following commands are available:

| Command | Description |
|---------|-------------|
| `nomad-dev-build` | Build the `nix-driver` plugin |
| `nomad-dev-agent` | Build plugin and start a local Nomad dev agent |
| `nomad-dev` | Run `nomad` commands against the local dev agent |

Quick start:

```sh
nix develop

# Terminal 1: start the dev agent
nomad-dev-agent

# Terminal 2: submit a job
nomad-dev run ./example/example-batch.hcl
```

## Acknowledgments

Based on work from [input-output-hk/nomad-driver-nix](https://github.com/input-output-hk/nomad-driver-nix), [JanMa/nomad-driver-nspawn](https://github.com/JanMa/nomad-driver-nspawn), and [KiaraGrouwstra/nomad-driver-nix2](https://github.com/KiaraGrouwstra/nomad-driver-nix2).

## License

[Mozilla Public License 2.0](LICENSE)
