package nix

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/nomad/drivers/shared/capabilities"
	"github.com/hashicorp/nomad/helper/pluginutils/hclutils"
	"github.com/hashicorp/nomad/plugins/drivers"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
)

const (
	isolationModePrivate = "private"
	isolationModeHost    = "host"
)

// isolationMode returns the task-level override if set, otherwise the driver default.
func isolationMode(defaultMode, taskMode string) string {
	if taskMode != "" {
		return taskMode
	}
	return defaultMode
}

// NamespaceNixConfig holds per-namespace overrides for Nix builder/cache settings.
type NamespaceNixConfig struct {
	Builders               []string `codec:"builders"`
	ExtraSubstituters      []string `codec:"extra_substituters"`
	ExtraTrustedPublicKeys []string `codec:"extra_trusted_public_keys"`
	PostBuildHook          string   `codec:"post_build_hook"`
	NetrcFile              string   `codec:"netrc_file"`
	AllowPrivileged        *bool    `codec:"allow_privileged"`
}

// Config is the driver configuration set by the SetConfig RPC call.
type Config struct {
	NoPivotRoot            bool                          `codec:"no_pivot_root"`
	AllowPrivileged        bool                          `codec:"allow_privileged"`
	DefaultModePID         string                        `codec:"default_pid_mode"`
	DefaultModeIPC         string                        `codec:"default_ipc_mode"`
	DefaultNixpkgs         string                        `codec:"default_nixpkgs"`
	AllowCaps              []string                      `codec:"allow_caps"`
	AllowBind              bool                          `codec:"allow_bind"`
	Builders               []string                      `codec:"builders"`
	ExtraSubstituters      []string                      `codec:"extra_substituters"`
	ExtraTrustedPublicKeys []string                      `codec:"extra_trusted_public_keys"`
	PostBuildHook          string                        `codec:"post_build_hook"`
	NetrcFile              string                        `codec:"netrc_file"`
	Namespaces             map[string]*NamespaceNixConfig `codec:"namespace"`
}

// NixOptionsForNamespace returns NixOptions with global settings merged with
// any per-namespace overrides. All fields are appended (namespace values added
// after globals).
func (c *Config) NixOptionsForNamespace(ns string) *NixOptions {
	opts := &NixOptions{
		Builders:               c.Builders,
		ExtraSubstituters:      c.ExtraSubstituters,
		ExtraTrustedPublicKeys: c.ExtraTrustedPublicKeys,
		PostBuildHook:          c.PostBuildHook,
		NetrcFile:              c.NetrcFile,
	}
	if nsc, ok := c.Namespaces[ns]; ok {
		if len(nsc.Builders) > 0 {
			opts.Builders = append(opts.Builders, nsc.Builders...)
		}
		if len(nsc.ExtraSubstituters) > 0 {
			opts.ExtraSubstituters = append(opts.ExtraSubstituters, nsc.ExtraSubstituters...)
		}
		if len(nsc.ExtraTrustedPublicKeys) > 0 {
			opts.ExtraTrustedPublicKeys = append(opts.ExtraTrustedPublicKeys, nsc.ExtraTrustedPublicKeys...)
		}
		if nsc.PostBuildHook != "" {
			opts.PostBuildHook = nsc.PostBuildHook
		}
		if nsc.NetrcFile != "" {
			opts.NetrcFile = nsc.NetrcFile
		}
	}
	return opts
}

// AllowPrivilegedForNamespace returns whether privileged mode is allowed for
// the given namespace. Per-namespace setting overrides the global default.
func (c *Config) AllowPrivilegedForNamespace(ns string) bool {
	if nsc, ok := c.Namespaces[ns]; ok && nsc.AllowPrivileged != nil {
		return *nsc.AllowPrivileged
	}
	return c.AllowPrivileged
}

func (c *Config) validate() error {
	if runtime.GOOS == "linux" {
		switch c.DefaultModePID {
		case isolationModePrivate, isolationModeHost:
		default:
			return fmt.Errorf("default_pid_mode must be %q or %q, got %q", isolationModePrivate, isolationModeHost, c.DefaultModePID)
		}

		switch c.DefaultModeIPC {
		case isolationModePrivate, isolationModeHost:
		default:
			return fmt.Errorf("default_ipc_mode must be %q or %q, got %q", isolationModePrivate, isolationModeHost, c.DefaultModeIPC)
		}

		badCaps := capabilities.Supported().Difference(capabilities.New(c.AllowCaps))
		if !badCaps.Empty() {
			return fmt.Errorf("allow_caps configured with capabilities not supported by system: %s", badCaps)
		}
	}

	return nil
}

// TaskConfig is the driver configuration of a task within a job.
type TaskConfig struct {
	Command      string             `codec:"command"`
	Args         []string           `codec:"args"`
	Bind         hclutils.MapStrStr `codec:"bind"`
	BindReadOnly hclutils.MapStrStr `codec:"bind_read_only"`
	ModePID      string             `codec:"pid_mode"`
	ModeIPC      string             `codec:"ipc_mode"`
	Nixpkgs      string             `codec:"nixpkgs"`
	CapAdd       []string           `codec:"cap_add"`
	CapDrop      []string           `codec:"cap_drop"`
	Packages     []string           `codec:"packages"`
	Sandbox      bool               `codec:"sandbox"`
}

func (tc *TaskConfig) validate(dc *Config) error {
	if runtime.GOOS == "linux" {
		switch tc.ModePID {
		case "", isolationModePrivate, isolationModeHost:
		default:
			return fmt.Errorf("pid_mode must be %q or %q, got %q", isolationModePrivate, isolationModeHost, tc.ModePID)
		}

		switch tc.ModeIPC {
		case "", isolationModePrivate, isolationModeHost:
		default:
			return fmt.Errorf("ipc_mode must be %q or %q, got %q", isolationModePrivate, isolationModeHost, tc.ModeIPC)
		}

		supported := capabilities.Supported()
		badAdds := supported.Difference(capabilities.New(tc.CapAdd))
		if !badAdds.Empty() {
			return fmt.Errorf("cap_add configured with capabilities not supported by system: %s", badAdds)
		}
		badDrops := supported.Difference(capabilities.New(tc.CapDrop))
		if !badDrops.Empty() {
			return fmt.Errorf("cap_drop configured with capabilities not supported by system: %s", badDrops)
		}
	}

	if !dc.AllowBind {
		if len(tc.Bind) > 0 || len(tc.BindReadOnly) > 0 {
			return fmt.Errorf("bind and bind_read_only are deactivated for the %s driver", pluginName)
		}
	}

	return nil
}

// TaskState is the state which is encoded in the handle returned in
// StartTask. This information is needed to rebuild the task state and handler
// during recovery.
type TaskState struct {
	ReattachConfig *pstructs.ReattachConfig
	TaskConfig     *drivers.TaskConfig
	Pid            int
	StartedAt      time.Time
}

// taskEnvList returns a filtered environment variable list for task execution.
// Nomad's cfg.Env includes host environment variables by default. The nix
// driver runs tasks in an isolated environment and should not leak the host
// env. We only keep NOMAD_* (Nomad metadata/service discovery) and PATH
// (which the driver sets to the nix profile bin directory).
func taskEnvList(env map[string]string) []string {
	l := make([]string, 0, len(env))
	for k, v := range env {
		if strings.HasPrefix(k, "NOMAD_") || k == "PATH" {
			l = append(l, k+"="+v)
		}
	}
	sort.Strings(l)
	return l
}
