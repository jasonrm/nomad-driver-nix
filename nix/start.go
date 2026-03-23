package nix

import (
	"fmt"
	"os"
	"path/filepath"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/drivers/shared/capabilities"
	"github.com/hashicorp/nomad/drivers/shared/executor"
	"github.com/hashicorp/nomad/drivers/shared/resolvconf"
	"github.com/hashicorp/nomad/plugins/drivers"
)

func (d *Driver) startTaskLinux(cfg *drivers.TaskConfig, driverConfig *TaskConfig, handle *drivers.TaskHandle, nixResult *NixPrepResult) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	useSandbox := driverConfig.Sandbox || !d.config.AllowPrivilegedForNamespace(cfg.Namespace)
	pluginLogFile := filepath.Join(cfg.TaskDir().Dir, "executor.out")
	executorConfig := &executor.ExecutorConfig{
		LogFile:     pluginLogFile,
		LogLevel:    "debug",
		FSIsolation: useSandbox,
		Compute:     d.compute,
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
		Compute:  d.compute,
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
