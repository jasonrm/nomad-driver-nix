package nix

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul-template/signals"
	hclog "github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/nomad/client/lib/cpustats"
	"github.com/hashicorp/nomad/drivers/shared/capabilities"
	"github.com/hashicorp/nomad/drivers/shared/eventer"
	"github.com/hashicorp/nomad/drivers/shared/executor"
	"github.com/hashicorp/nomad/drivers/shared/resolvconf"
	"github.com/hashicorp/nomad/helper/pluginutils/hclutils"
	"github.com/hashicorp/nomad/helper/pluginutils/loader"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
)

const (
	pluginName        = "nix"
	fingerprintPeriod = 30 * time.Second
	taskHandleVersion = 1

	isolationModePrivate = "private"
	isolationModeHost    = "host"
)

// PluginVersion is set from main.go at startup.
var PluginVersion = "0.2.0-dev"

var (
	PluginID = loader.PluginID{
		Name:       pluginName,
		PluginType: base.PluginTypeDriver,
	}

	pluginInfo = &base.PluginInfoResponse{
		Type:              base.PluginTypeDriver,
		PluginApiVersions: []string{drivers.ApiVersion010},
		PluginVersion:     "0.2.0-dev",
		Name:              pluginName,
	}

	configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		"no_pivot_root": hclspec.NewDefault(
			hclspec.NewAttr("no_pivot_root", "bool", false),
			hclspec.NewLiteral("false"),
		),
		"allow_privileged": hclspec.NewDefault(
			hclspec.NewAttr("allow_privileged", "bool", false),
			hclspec.NewLiteral("false"),
		),
		"default_pid_mode": hclspec.NewDefault(
			hclspec.NewAttr("default_pid_mode", "string", false),
			hclspec.NewLiteral(`"private"`),
		),
		"default_ipc_mode": hclspec.NewDefault(
			hclspec.NewAttr("default_ipc_mode", "string", false),
			hclspec.NewLiteral(`"private"`),
		),
		"default_nixpkgs": hclspec.NewDefault(
			hclspec.NewAttr("default_nixpkgs", "string", false),
			hclspec.NewLiteral(`"github:nixos/nixpkgs/nixos-25.11"`),
		),
		"allow_caps": hclspec.NewDefault(
			hclspec.NewAttr("allow_caps", "list(string)", false),
			hclspec.NewLiteral(capabilities.HCLSpecLiteral),
		),
		"allow_bind": hclspec.NewDefault(
			hclspec.NewAttr("allow_bind", "bool", false),
			hclspec.NewLiteral("true"),
		),
		"builders": hclspec.NewDefault(
			hclspec.NewAttr("builders", "list(string)", false),
			hclspec.NewLiteral(`[]`),
		),
		"extra_substituters": hclspec.NewDefault(
			hclspec.NewAttr("extra_substituters", "list(string)", false),
			hclspec.NewLiteral(`[]`),
		),
		"extra_trusted_public_keys": hclspec.NewDefault(
			hclspec.NewAttr("extra_trusted_public_keys", "list(string)", false),
			hclspec.NewLiteral(`[]`),
		),
		"post_build_hook": hclspec.NewDefault(
			hclspec.NewAttr("post_build_hook", "string", false),
			hclspec.NewLiteral(`""`),
		),
		"netrc_file": hclspec.NewDefault(
			hclspec.NewAttr("netrc_file", "string", false),
			hclspec.NewLiteral(`""`),
		),
		"namespace": hclspec.NewBlockMap("namespace", []string{"name"}, hclspec.NewObject(map[string]*hclspec.Spec{
			"builders":                  hclspec.NewAttr("builders", "list(string)", false),
			"extra_substituters":        hclspec.NewAttr("extra_substituters", "list(string)", false),
			"extra_trusted_public_keys": hclspec.NewAttr("extra_trusted_public_keys", "list(string)", false),
			"post_build_hook":           hclspec.NewAttr("post_build_hook", "string", false),
			"netrc_file":                hclspec.NewAttr("netrc_file", "string", false),
			"allow_privileged":          hclspec.NewAttr("allow_privileged", "bool", false),
		})),
	})

	taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		"command":        hclspec.NewAttr("command", "string", true),
		"args":           hclspec.NewAttr("args", "list(string)", false),
		"bind":           hclspec.NewAttr("bind", "list(map(string))", false),
		"bind_read_only": hclspec.NewAttr("bind_read_only", "list(map(string))", false),
		"pid_mode":       hclspec.NewAttr("pid_mode", "string", false),
		"ipc_mode":       hclspec.NewAttr("ipc_mode", "string", false),
		"cap_add":        hclspec.NewAttr("cap_add", "list(string)", false),
		"cap_drop":       hclspec.NewAttr("cap_drop", "list(string)", false),
		"nixpkgs":        hclspec.NewAttr("nixpkgs", "string", false),
		"packages":       hclspec.NewAttr("packages", "list(string)", false),
		"sandbox": hclspec.NewDefault(
			hclspec.NewAttr("sandbox", "bool", false),
			hclspec.NewLiteral("true"),
		),
	})

	driverCapabilities = &drivers.Capabilities{
		SendSignals: true,
		Exec:        true,
		FSIsolation: drivers.FSIsolationNone,
		NetIsolationModes: []drivers.NetIsolationMode{
			drivers.NetIsolationModeHost,
			drivers.NetIsolationModeGroup,
		},
		MountConfigs: drivers.MountConfigSupportAll,
	}
)

// isolationMode returns the task-level override if set, otherwise the driver default.
func isolationMode(defaultMode, taskMode string) string {
	if taskMode != "" {
		return taskMode
	}
	return defaultMode
}

// Driver implements the Nomad driver plugin interface for running Nix-built tasks.
type Driver struct {
	eventer        *eventer.Eventer
	config         Config
	nomadConfig    *base.ClientDriverConfig
	tasks          *taskStore
	ctx            context.Context
	signalShutdown context.CancelFunc
	logger         hclog.Logger

	fingerprintSuccess *bool
	fingerprintLock    sync.Mutex
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

func NewPlugin(logger hclog.Logger) drivers.DriverPlugin {
	ctx, cancel := context.WithCancel(context.Background())
	logger = logger.Named(pluginName)
	return &Driver{
		eventer:        eventer.NewEventer(ctx, logger),
		tasks:          newTaskStore(),
		ctx:            ctx,
		signalShutdown: cancel,
		logger:         logger,
	}
}

// compute returns a cpustats.Compute from the nomad config, or a no-op if unavailable.
func (d *Driver) compute() cpustats.Compute {
	if d.nomadConfig != nil && d.nomadConfig.Topology != nil {
		return d.nomadConfig.Topology.Compute()
	}
	return cpustats.Compute{}
}

func (d *Driver) setFingerprintSuccess() {
	d.fingerprintLock.Lock()
	v := true
	d.fingerprintSuccess = &v
	d.fingerprintLock.Unlock()
}

func (d *Driver) setFingerprintFailure() {
	d.fingerprintLock.Lock()
	v := false
	d.fingerprintSuccess = &v
	d.fingerprintLock.Unlock()
}

func (d *Driver) fingerprintSuccessful() bool {
	d.fingerprintLock.Lock()
	defer d.fingerprintLock.Unlock()
	return d.fingerprintSuccess == nil || *d.fingerprintSuccess
}

func (d *Driver) PluginInfo() (*base.PluginInfoResponse, error) {
	info := *pluginInfo
	info.PluginVersion = PluginVersion
	return &info, nil
}

func (d *Driver) ConfigSchema() (*hclspec.Spec, error) {
	return configSpec, nil
}

func (d *Driver) SetConfig(cfg *base.Config) error {
	var config Config
	if len(cfg.PluginConfig) != 0 {
		if err := base.MsgPackDecode(cfg.PluginConfig, &config); err != nil {
			return err
		}
	}
	if err := config.validate(); err != nil {
		return err
	}
	d.logger.Info("Got config", "driver_config", hclog.Fmt("%+v", config))
	d.config = config

	if cfg != nil && cfg.AgentConfig != nil {
		d.nomadConfig = cfg.AgentConfig.Driver
	}
	return nil
}

func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return driverCapabilities, nil
}

func (d *Driver) Fingerprint(ctx context.Context) (<-chan *drivers.Fingerprint, error) {
	ch := make(chan *drivers.Fingerprint)
	go d.handleFingerprint(ctx, ch)
	return ch, nil
}

func (d *Driver) handleFingerprint(ctx context.Context, ch chan<- *drivers.Fingerprint) {
	defer close(ch)
	ticker := time.NewTimer(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			ticker.Reset(fingerprintPeriod)
			ch <- d.buildFingerprint()
		}
	}
}

func (d *Driver) buildFingerprint() *drivers.Fingerprint {
	fp := &drivers.Fingerprint{
		Attributes:        map[string]*pstructs.Attribute{},
		Health:            drivers.HealthStateHealthy,
		HealthDescription: drivers.DriverHealthy,
	}

	// Check that nix is on PATH
	nixPath, err := exec.LookPath("nix")
	if err != nil {
		fp.Health = drivers.HealthStateUnhealthy
		fp.HealthDescription = "nix binary not found on PATH"
		if d.fingerprintSuccessful() {
			d.logger.Warn(fp.HealthDescription)
		}
		d.setFingerprintFailure()
		return fp
	}

	// Report nix version
	ver, err := nixVersion()
	if err != nil {
		d.logger.Warn("could not determine nix version", "error", err)
	} else {
		fp.Attributes["driver.nix.nix_version"] = pstructs.NewStringAttribute(ver)
	}

	fp.Attributes["driver.nix"] = pstructs.NewBoolAttribute(true)
	fp.Attributes["driver.nix.nix_path"] = pstructs.NewStringAttribute(nixPath)

	switch runtime.GOOS {
	case "linux":
		d.fingerprintLinux(fp)
	case "darwin":
		d.fingerprintDarwin(fp)
	default:
		fp.Health = drivers.HealthStateUndetected
		fp.HealthDescription = fmt.Sprintf("nix driver unsupported on %s", runtime.GOOS)
		d.setFingerprintFailure()
		return fp
	}

	d.setFingerprintSuccess()
	return fp
}

func (d *Driver) fingerprintLinux(fp *drivers.Fingerprint) {
	if os.Getuid() != 0 {
		fp.Health = drivers.HealthStateUndetected
		fp.HealthDescription = "nix driver requires root on Linux for isolation"
		d.setFingerprintFailure()
		return
	}
	fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("libcontainer")
}

func (d *Driver) fingerprintDarwin(fp *drivers.Fingerprint) {
	if sandboxAvailable() {
		fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("sandbox")
	} else {
		fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("none")
	}
}

func (d *Driver) RecoverTask(handle *drivers.TaskHandle) error {
	if handle == nil {
		return fmt.Errorf("handle cannot be nil")
	}

	if _, ok := d.tasks.Get(handle.Config.ID); ok {
		d.logger.Trace("nothing to recover; task already exists",
			"task_id", handle.Config.ID,
			"task_name", handle.Config.Name,
		)
		return nil
	}

	var taskState TaskState
	if err := handle.GetDriverState(&taskState); err != nil {
		d.logger.Error("failed to decode task state from handle", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to decode task state from handle: %v", err)
	}

	plugRC, err := pstructs.ReattachConfigToGoPlugin(taskState.ReattachConfig)
	if err != nil {
		d.logger.Error("failed to build ReattachConfig from task state", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to build ReattachConfig from task state: %v", err)
	}

	exec, pluginClient, err := executor.ReattachToExecutor(plugRC,
		d.logger.With("task_name", handle.Config.Name, "alloc_id", handle.Config.AllocID),
		d.compute())
	if err != nil {
		d.logger.Error("failed to reattach to executor", "error", err, "task_id", handle.Config.ID)
		return fmt.Errorf("failed to reattach to executor: %v", err)
	}

	h := &taskHandle{
		exec:         exec,
		pid:          taskState.Pid,
		pluginClient: pluginClient,
		taskConfig:   taskState.TaskConfig,
		procState:    drivers.TaskStateRunning,
		startedAt:    taskState.StartedAt,
		exitResult:   &drivers.ExitResult{},
		logger:       d.logger,
	}

	d.tasks.Set(taskState.TaskConfig.ID, h)

	go h.run()
	return nil
}

func (d *Driver) StartTask(cfg *drivers.TaskConfig) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	if _, ok := d.tasks.Get(cfg.ID); ok {
		return nil, nil, fmt.Errorf("task with ID %q already started", cfg.ID)
	}

	var driverConfig TaskConfig
	if err := cfg.DecodeDriverConfig(&driverConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to decode driver config: %v", err)
	}
	if driverConfig.Bind == nil {
		driverConfig.Bind = make(hclutils.MapStrStr)
	}
	if driverConfig.BindReadOnly == nil {
		driverConfig.BindReadOnly = make(hclutils.MapStrStr)
	}

	if err := driverConfig.validate(&d.config); err != nil {
		return nil, nil, fmt.Errorf("failed driver config validation: %v", err)
	}

	d.logger.Info("starting task", "driver_cfg", hclog.Fmt("%+v", driverConfig))
	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg

	// Determine nixpkgs
	nixpkgs := driverConfig.Nixpkgs
	if nixpkgs == "" {
		nixpkgs = d.config.DefaultNixpkgs
	}
	for i := range driverConfig.Packages {
		if strings.HasPrefix(driverConfig.Packages[i], "#") {
			driverConfig.Packages[i] = nixpkgs + driverConfig.Packages[i]
		}
	}

	// Always include coreutils for a baseline environment (env, cat, ls, etc.)
	driverConfig.Packages = append(driverConfig.Packages, nixpkgs+"#coreutils")

	// Emit build event
	d.eventer.EmitEvent(&drivers.TaskEvent{
		TaskID:    cfg.ID,
		AllocID:   cfg.AllocID,
		TaskName:  cfg.Name,
		Timestamp: time.Now(),
		Message: fmt.Sprintf(
			"Building Nix packages and preparing environment (using nixpkgs from flake: %s)",
			nixpkgs,
		),
		Annotations: map[string]string{
			"packages": strings.Join(driverConfig.Packages, " "),
		},
	})

	taskDirs := cfg.TaskDir()
	progress := func(message string) {
		d.eventer.EmitEvent(&drivers.TaskEvent{
			TaskID:    cfg.ID,
			AllocID:   cfg.AllocID,
			TaskName:  cfg.Name,
			Timestamp: time.Now(),
			Message:   message,
		})
	}
	nixResult, err := prepareNixPackages(taskDirs.Dir, driverConfig.Packages, nixpkgs, d.config.NixOptionsForNamespace(cfg.Namespace), d.logger, progress)
	if err != nil {
		return nil, nil, err
	}

	switch runtime.GOOS {
	case "linux":
		return d.startTaskLinux(cfg, &driverConfig, handle, nixResult)
	case "darwin":
		return d.startTaskDarwin(cfg, &driverConfig, handle, nixResult)
	default:
		return nil, nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func (d *Driver) startTaskLinux(cfg *drivers.TaskConfig, driverConfig *TaskConfig, handle *drivers.TaskHandle, nixResult *NixPrepResult) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	useSandbox := driverConfig.Sandbox || !d.config.AllowPrivilegedForNamespace(cfg.Namespace)
	pluginLogFile := filepath.Join(cfg.TaskDir().Dir, "executor.out")
	executorConfig := &executor.ExecutorConfig{
		LogFile:     pluginLogFile,
		LogLevel:    "debug",
		FSIsolation: useSandbox,
	}

	exec, pluginClient, err := executor.CreateExecutor(
		d.logger.With("task_name", handle.Config.Name, "alloc_id", handle.Config.AllocID),
		d.nomadConfig, executorConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %v", err)
	}

	user := cfg.User
	if user == "" {
		user = "nobody"
	}

	// Add nix closure mounts
	for host, task := range nixResult.Mounts {
		cfg.Mounts = append(cfg.Mounts, &drivers.MountConfig{
			TaskPath:        task,
			HostPath:        host,
			Readonly:        true,
			PropagationMode: "private",
		})
	}

	// System files
	etcpaths := []string{
		"/etc/nsswitch.conf",
		"/etc/passwd",
	}

	if cfg.DNS != nil {
		dnsMount, err := resolvconf.GenerateDNSMount(cfg.TaskDir().Dir, cfg.DNS)
		if err != nil {
			pluginClient.Kill()
			return nil, nil, fmt.Errorf("failed to build mount for resolv.conf: %v", err)
		}
		cfg.Mounts = append(cfg.Mounts, dnsMount)
	} else {
		etcpaths = append(etcpaths, "/etc/resolv.conf")
	}

	for _, f := range etcpaths {
		if _, ok := nixResult.Mounts[f]; !ok {
			cfg.Mounts = append(cfg.Mounts, &drivers.MountConfig{
				TaskPath:        f,
				HostPath:        f,
				Readonly:        true,
				PropagationMode: "private",
			})
		}
	}

	// Mount /usr/bin/env if available in the profile, since #!/usr/bin/env
	// is the standard shebang convention for portable scripts.
	envPath := filepath.Join(nixResult.BinPath, "env")
	if _, err := os.Stat(envPath); err == nil {
		cfg.Mounts = append(cfg.Mounts, &drivers.MountConfig{
			TaskPath:        "/usr/bin/env",
			HostPath:        envPath,
			Readonly:        true,
			PropagationMode: "private",
		})
	}

	d.logger.Info("adding mounts for Nix closure and system files", "mount_count", len(cfg.Mounts))

	// Bind mounts from task config
	for host, task := range driverConfig.Bind {
		cfg.Mounts = append(cfg.Mounts, &drivers.MountConfig{
			TaskPath:        task,
			HostPath:        host,
			Readonly:        false,
			PropagationMode: "private",
		})
	}
	for host, task := range driverConfig.BindReadOnly {
		cfg.Mounts = append(cfg.Mounts, &drivers.MountConfig{
			TaskPath:        task,
			HostPath:        host,
			Readonly:        true,
			PropagationMode: "private",
		})
	}

	cfg.Env["PATH"] = "/bin"

	// Resolve the command to an absolute path using the nix profile's bin
	// directory. The executor's lookupBin validates the command on the host
	// filesystem before the container's bind mounts are set up. Since nix
	// store paths are mounted at their original locations inside the
	// container (and exist on the host), using the absolute store path
	// satisfies both the pre-launch check and in-container execution.
	cmd := driverConfig.Command
	if !filepath.IsAbs(cmd) {
		absCmd := filepath.Join(nixResult.BinPath, cmd)
		if _, err := os.Stat(absCmd); err == nil {
			cmd = absCmd
		}
	}

	caps, err := capabilities.Calculate(
		capabilities.NomadDefaults(), d.config.AllowCaps, driverConfig.CapAdd, driverConfig.CapDrop,
	)
	if err != nil {
		pluginClient.Kill()
		return nil, nil, err
	}

	execCmd := &executor.ExecCommand{
		Cmd:              cmd,
		Args:             driverConfig.Args,
		Env:              taskEnvList(cfg.Env),
		User:             user,
		ResourceLimits:   true,
		NoPivotRoot:      d.config.NoPivotRoot,
		Resources:        cfg.Resources,
		TaskDir:          cfg.TaskDir().Dir,
		StdoutPath:       cfg.StdoutPath,
		StderrPath:       cfg.StderrPath,
		Mounts:           cfg.Mounts,
		Devices:          cfg.Devices,
		NetworkIsolation: cfg.NetworkIsolation,
		ModePID:          isolationMode(d.config.DefaultModePID, driverConfig.ModePID),
		ModeIPC:          isolationMode(d.config.DefaultModeIPC, driverConfig.ModeIPC),
		Capabilities:     caps,
	}

	d.logger.Info("launching with", "exec_cmd", hclog.Fmt("%+v", execCmd))

	ps, err := exec.Launch(execCmd)
	if err != nil {
		pluginClient.Kill()
		return nil, nil, fmt.Errorf("failed to launch command with executor: %v", err)
	}

	return d.finishStartTask(cfg, handle, exec, pluginClient, ps)
}

func (d *Driver) startTaskDarwin(cfg *drivers.TaskConfig, driverConfig *TaskConfig, handle *drivers.TaskHandle, nixResult *NixPrepResult) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	pluginLogFile := filepath.Join(cfg.TaskDir().Dir, "executor.out")
	executorConfig := &executor.ExecutorConfig{
		LogFile:  pluginLogFile,
		LogLevel: "debug",
	}

	exec, pluginClient, err := executor.CreateExecutor(
		d.logger.With("task_name", handle.Config.Name, "alloc_id", handle.Config.AllocID),
		d.nomadConfig, executorConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %v", err)
	}

	user := cfg.User
	if user == "" {
		user = ""
	}

	// Log warnings for Linux-only options
	if driverConfig.ModePID != "" {
		d.logger.Warn("pid_mode is ignored on macOS", "pid_mode", driverConfig.ModePID)
	}
	if driverConfig.ModeIPC != "" {
		d.logger.Warn("ipc_mode is ignored on macOS", "ipc_mode", driverConfig.ModeIPC)
	}
	if len(driverConfig.CapAdd) > 0 || len(driverConfig.CapDrop) > 0 {
		d.logger.Warn("cap_add/cap_drop are ignored on macOS")
	}

	// Set PATH to the nix profile's bin directory only
	cfg.Env["PATH"] = nixResult.BinPath

	// Resolve the command to an absolute path using the nix profile's bin
	// directory, so the executor can find it without relying on PATH lookup.
	cmd := driverConfig.Command
	if !filepath.IsAbs(cmd) {
		absCmd := filepath.Join(nixResult.BinPath, cmd)
		if _, err := os.Stat(absCmd); err == nil {
			cmd = absCmd
		}
	}
	args := driverConfig.Args

	// If sandbox-exec is available and sandbox is enabled, wrap the command.
	// Sandbox can be disabled per-task with sandbox=false, but only if
	// allow_privileged is set in the agent/namespace config.
	useSandbox := driverConfig.Sandbox || !d.config.AllowPrivilegedForNamespace(cfg.Namespace)
	if useSandbox && sandboxAvailable() {
		taskDir := cfg.TaskDir().Dir
		sbplContent := generateSBPL(nixResult.ClosurePaths, taskDir, cfg.AllocDir, nixResult.BinPath)
		sbplPath := filepath.Join(taskDir, "sandbox.sb")
		if err := os.WriteFile(sbplPath, []byte(sbplContent), 0644); err != nil {
			pluginClient.Kill()
			return nil, nil, fmt.Errorf("failed to write sandbox profile: %v", err)
		}

		// Wrap: sandbox-exec -f <profile> <original-command> <args...>
		newArgs := []string{"-f", sbplPath, cmd}
		newArgs = append(newArgs, args...)
		cmd = "sandbox-exec"
		args = newArgs
		d.logger.Info("wrapping command with sandbox-exec", "profile", sbplPath)
	}

	execCmd := &executor.ExecCommand{
		Cmd:              cmd,
		Args:             args,
		Env:              taskEnvList(cfg.Env),
		User:             user,
		Resources:        cfg.Resources,
		TaskDir:          cfg.TaskDir().Dir,
		StdoutPath:       cfg.StdoutPath,
		StderrPath:       cfg.StderrPath,
		Mounts:           cfg.Mounts,
		Devices:          cfg.Devices,
		NetworkIsolation: cfg.NetworkIsolation,
	}

	d.logger.Info("launching with", "exec_cmd", hclog.Fmt("%+v", execCmd))

	ps, err := exec.Launch(execCmd)
	if err != nil {
		pluginClient.Kill()
		return nil, nil, fmt.Errorf("failed to launch command with executor: %v", err)
	}

	return d.finishStartTask(cfg, handle, exec, pluginClient, ps)
}

func (d *Driver) finishStartTask(cfg *drivers.TaskConfig, handle *drivers.TaskHandle, exec executor.Executor, pluginClient *plugin.Client, ps *executor.ProcessState) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	h := &taskHandle{
		exec:         exec,
		pid:          ps.Pid,
		pluginClient: pluginClient,
		taskConfig:   cfg,
		procState:    drivers.TaskStateRunning,
		startedAt:    time.Now().Round(time.Millisecond),
		logger:       d.logger,
	}

	driverState := TaskState{
		ReattachConfig: pstructs.ReattachConfigFromGoPlugin(pluginClient.ReattachConfig()),
		Pid:            ps.Pid,
		TaskConfig:     cfg,
		StartedAt:      h.startedAt,
	}

	if err := handle.SetDriverState(&driverState); err != nil {
		d.logger.Error("failed to start task, error setting driver state", "error", err)
		_ = exec.Shutdown("", 0)
		pluginClient.Kill()
		return nil, nil, fmt.Errorf("failed to set driver state: %v", err)
	}

	d.tasks.Set(cfg.ID, h)
	go h.run()
	return handle, nil, nil
}

func (d *Driver) WaitTask(ctx context.Context, taskID string) (<-chan *drivers.ExitResult, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	ch := make(chan *drivers.ExitResult)
	go d.handleWait(ctx, handle, ch)
	return ch, nil
}

func (d *Driver) handleWait(ctx context.Context, handle *taskHandle, ch chan *drivers.ExitResult) {
	defer close(ch)
	var result *drivers.ExitResult
	ps, err := handle.exec.Wait(ctx)
	if err != nil {
		result = &drivers.ExitResult{
			Err: fmt.Errorf("executor: error waiting on process: %v", err),
		}
	} else {
		result = &drivers.ExitResult{
			ExitCode: ps.ExitCode,
			Signal:   ps.Signal,
		}
	}

	select {
	case <-ctx.Done():
	case <-d.ctx.Done():
	case ch <- result:
	}
}

func (d *Driver) StopTask(taskID string, timeout time.Duration, signal string) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if err := handle.exec.Shutdown(signal, timeout); err != nil {
		if handle.pluginClient.Exited() {
			return nil
		}
		return fmt.Errorf("executor Shutdown failed: %v", err)
	}

	return nil
}

func (d *Driver) DestroyTask(taskID string, force bool) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if handle.IsRunning() && !force {
		return fmt.Errorf("cannot destroy running task")
	}

	if !handle.pluginClient.Exited() {
		if err := handle.exec.Shutdown("", 0); err != nil {
			handle.logger.Error("destroying executor failed", "error", err)
		}

		handle.pluginClient.Kill()
	}

	d.tasks.Delete(taskID)
	return nil
}

func (d *Driver) InspectTask(taskID string) (*drivers.TaskStatus, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	return handle.TaskStatus(), nil
}

func (d *Driver) TaskStats(ctx context.Context, taskID string, interval time.Duration) (<-chan *drivers.TaskResourceUsage, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	return handle.exec.Stats(ctx, interval)
}

func (d *Driver) TaskEvents(ctx context.Context) (<-chan *drivers.TaskEvent, error) {
	return d.eventer.TaskEvents(ctx)
}

func (d *Driver) SignalTask(taskID string, signal string) error {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	sig := os.Interrupt
	if s, ok := signals.SignalLookup[signal]; ok {
		sig = s
	} else {
		d.logger.Warn("unknown signal to send to task, using SIGINT instead", "signal", signal, "task_id", handle.taskConfig.ID)
	}
	return handle.exec.Signal(sig)
}

func (d *Driver) ExecTask(taskID string, cmd []string, timeout time.Duration) (*drivers.ExecTaskResult, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("error cmd must have at least one value")
	}
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	args := []string{}
	if len(cmd) > 1 {
		args = cmd[1:]
	}

	out, exitCode, err := handle.exec.Exec(time.Now().Add(timeout), cmd[0], args)
	if err != nil {
		return nil, err
	}

	return &drivers.ExecTaskResult{
		Stdout: out,
		ExitResult: &drivers.ExitResult{
			ExitCode: exitCode,
		},
	}, nil
}

var _ drivers.ExecTaskStreamingRawDriver = (*Driver)(nil)

func (d *Driver) ExecTaskStreamingRaw(ctx context.Context,
	taskID string,
	command []string,
	tty bool,
	stream drivers.ExecTaskStream) error {

	if len(command) == 0 {
		return fmt.Errorf("error cmd must have at least one value")
	}
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	return handle.exec.ExecStreaming(ctx, command, tty, stream)
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
