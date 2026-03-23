package nix

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/nomad/client/lib/cpustats"
	"github.com/hashicorp/nomad/drivers/shared/capabilities"
	"github.com/hashicorp/nomad/drivers/shared/eventer"
	"github.com/hashicorp/nomad/drivers/shared/executor"
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

	// compute contains cpu compute information (v1.11.3+)
	compute cpustats.Compute
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
		d.compute = cfg.AgentConfig.Compute()
	}
	return nil
}

func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return driverCapabilities, nil
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
