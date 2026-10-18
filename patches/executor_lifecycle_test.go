//go:build linux || darwin

package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/nomad/client/lib/cpustats"
	nomadexecutor "github.com/hashicorp/nomad/drivers/shared/executor"
	"github.com/hashicorp/nomad/plugins/base"
)

// Importing Nomad's executor registers its real subprocess entrypoint in this
// test executable. Isolated tests run without a task and need no root/cgroups.
func startTestExecutor(t *testing.T, isolated bool) (nomadexecutor.Executor, *plugin.Client, *plugin.GRPCClient, *exec.Cmd) {
	t.Helper()
	if isolated && os.Getenv("NOMAD_TEST_PATCHED_EXECUTOR") != "1" {
		t.Skip("requires Nix build's patched vendor tree")
	}
	config, err := json.Marshal(&nomadexecutor.ExecutorConfig{
		LogFile: filepath.Join(t.TempDir(), "executor.out"), FSIsolation: isolated, LogLevel: "debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := hclog.NewNullLogger()
	cmd := exec.Command(os.Args[0], "executor", string(config))
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  base.Handshake,
		Plugins:          nomadexecutor.GetPluginMap(logger, isolated, cpustats.Compute{}),
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           logger, StartTimeout: 5 * time.Second,
	})
	t.Cleanup(client.Kill)
	rpc, err := client.Client()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rpc.Dispense("executor")
	if err != nil {
		t.Fatal(err)
	}
	executor := raw.(nomadexecutor.Executor)
	if _, err := executor.Version(); err != nil {
		t.Fatal(err)
	}
	return executor, client, rpc.(*plugin.GRPCClient), cmd
}

// A real workload in the universal executor exercises RPC lifecycle semantics
// without requiring a privileged Linux container. Linux isolated workloads and
// a full Nomad agent restart additionally need the documented host-level tests.
func TestExecutorWorkloadSurvivesDisconnect(t *testing.T) {
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		t.Skip("root Linux execution requires Nomad-managed cgroups; run this resource-free workload test unprivileged")
	}
	executor, client, rpc, _ := startTestExecutor(t, false)
	// Also clean up a process if Launch fails after starting it.
	t.Cleanup(func() { _ = executor.Shutdown("", 0) })
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	for _, name := range []string{"stdout", "stderr"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	state, err := executor.Launch(&nomadexecutor.ExecCommand{
		Cmd: os.Args[0], Args: []string{"-test.run=^TestExecutorWorkloadHelper$"},
		Env:     append(os.Environ(), "NOMAD_EXECUTOR_WORKLOAD_READY="+ready),
		TaskDir: dir, StdoutPath: filepath.Join(dir, "stdout"), StderrPath: filepath.Join(dir, "stderr"),
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(state.Pid)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = process.Kill()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workload did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	reattach := client.ReattachConfig()
	// Simulate agent connection loss without sending shutdown to the server.
	if err := rpc.Conn.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, recoveredClient, err := nomadexecutor.ReattachToExecutor(reattach, hclog.NewNullLogger(), cpustats.Compute{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recoveredClient.Kill)
	if err := process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("original workload PID did not survive disconnect: %v", err)
	}
	if err := recovered.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("reattached executor cannot reach its workload: %v", err)
	}
	if err := recovered.Shutdown("SIGTERM", time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exit, err := recovered.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped = true
	if exit.Pid != state.Pid || exit.Signal != int(syscall.SIGTERM) {
		t.Fatalf("unexpected workload exit: %#v", exit)
	}
	if _, err := recovered.Version(); err != nil {
		t.Fatalf("normal task-stop RPC exited executor: %v", err)
	}
}

func TestExecutorWorkloadHelper(t *testing.T) {
	ready := os.Getenv("NOMAD_EXECUTOR_WORKLOAD_READY")
	if ready == "" {
		return
	}
	if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}
