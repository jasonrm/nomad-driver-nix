#!/usr/bin/env bash
set -euo pipefail

# Update upstream exec driver files from the nomad repository.
#
# handle.go and state.go get a package rename only.
# driver.go is transformed into driver_upstream.go with nix-overridden
# declarations removed, so that only passthrough methods remain.

REF=v1.11.3
BASE_URL="https://github.com/hashicorp/nomad/raw/${REF}/drivers/exec"

cd "$(dirname "$0")"

echo "Fetching upstream exec driver @ ${REF}..."

# handle.go and state.go: package rename only
for f in handle.go state.go; do
    curl -fsSL "${BASE_URL}/${f}" -o "${f}"
    sed -i '' 's/^package exec$/package nix/' "${f}"
    echo "  ${f} updated"
done

# driver.go -> driver_upstream.go
# Download, rename package, then strip declarations that nix overrides.
curl -fsSL "${BASE_URL}/driver.go" -o driver_upstream.go

sed -i '' 's/^package exec$/package nix/' driver_upstream.go

# Add header comment after package line
sed -i '' '/^package nix$/a\
\
// This file contains upstream exec driver methods that are used as-is by the\
// nix driver. It is generated from the upstream nomad exec driver and should\
// only need a package rename and pruning of nix-overridden declarations when\
// updating. See pull-upstream.sh.
' driver_upstream.go

# Remove declarations that nix overrides.
# We use go/ast-aware deletion via a simple sed-based approach:
# delete known const/var/type/func blocks by matching their boundaries.

# Helper: delete lines between two patterns (inclusive)
del_between() {
    local start="$1" end="$2" file="$3"
    sed -i '' "/${start}/,/${end}/d" "${file}"
}

# Delete the const block (pluginName, fingerprintPeriod, taskHandleVersion)
del_between '^const ($' '^)$' driver_upstream.go

# Delete the var block (PluginID, PluginConfig, pluginInfo, configSpec, taskConfigSpec, driverCapabilities)
del_between '^var ($' '^)$' driver_upstream.go

# Delete type declarations
del_between '^// Driver fork/execs' '^}$' driver_upstream.go  # Driver struct
del_between '^// Config is the driver' '^}$' driver_upstream.go  # Config struct
del_between '^func (c \*Config) validate' '^}$' driver_upstream.go  # Config.validate
del_between '^// TaskConfig is the driver' '^}$' driver_upstream.go  # TaskConfig struct
del_between '^func (tc \*TaskConfig) validate' '^}$' driver_upstream.go  # TaskConfig.validate
del_between '^// TaskState is the state' '^}$' driver_upstream.go  # TaskState struct
del_between '^type UserIDValidator' '^}$' driver_upstream.go  # UserIDValidator interface

# Delete function declarations that nix overrides
del_between '^// NewExecDriver' '^}$' driver_upstream.go  # NewExecDriver
del_between '^func (d \*Driver) PluginInfo' '^}$' driver_upstream.go
del_between '^func (d \*Driver) ConfigSchema' '^}$' driver_upstream.go
del_between '^func (d \*Driver) SetConfig' '^}$' driver_upstream.go
del_between '^func (d \*Driver) TaskConfigSchema' '^}$' driver_upstream.go
del_between '^// Capabilities is returned' '^}$' driver_upstream.go  # Capabilities
del_between '^func (d \*Driver) buildFingerprint' '^}$' driver_upstream.go
del_between '^func (d \*Driver) StartTask' '^}$' driver_upstream.go

echo "  driver_upstream.go updated"

# Run goimports to fix imports
if command -v goimports &>/dev/null; then
    goimports -w driver_upstream.go handle.go state.go
    echo "  goimports applied"
else
    echo "  WARNING: goimports not found, imports may need manual cleanup"
fi

echo "Done. Review driver_upstream.go and run 'go build ./nix/' to verify."
