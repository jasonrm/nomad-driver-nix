package nix

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/drivers"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
)

func TestRecoverTaskMissingExecutor(t *testing.T) {
	// Use a reaped process rather than guessing an unused PID.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	for _, level := range []hclog.Level{hclog.Info, hclog.Debug} {
		t.Run(level.String(), func(t *testing.T) {
			var logs bytes.Buffer
			d := NewPlugin(hclog.New(&hclog.LoggerOptions{Level: level, Output: &logs})).(*Driver)
			defer d.signalShutdown()
			handle := drivers.NewTaskHandle(taskHandleVersion)
			handle.Config = &drivers.TaskConfig{ID: "completed-task", Name: "migrate"}
			if err := handle.SetDriverState(&TaskState{
				TaskConfig: handle.Config,
				ReattachConfig: &pstructs.ReattachConfig{
					Protocol: "grpc", Network: "unix", Addr: t.TempDir() + "/executor.sock", Pid: cmd.Process.Pid,
				},
			}); err != nil {
				t.Fatal(err)
			}

			err := d.RecoverTask(handle)
			if err == nil || !strings.Contains(err.Error(), "reattachment process not found") {
				t.Fatalf("expected missing executor error to reach Nomad, got %v", err)
			}
			if _, ok := d.tasks.Get(handle.Config.ID); ok {
				t.Fatal("failed recovery registered a task")
			}
			if strings.Contains(logs.String(), "[ERROR]") {
				t.Fatalf("expected recovery failure must not emit error-level logs: %s", &logs)
			}
			if level == hclog.Debug && !strings.Contains(logs.String(), "failed to reattach to executor") {
				t.Fatalf("missing debug diagnostic: %s", &logs)
			}
		})
	}
}
