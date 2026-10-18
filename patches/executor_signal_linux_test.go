//go:build linux && cgo

package executor

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/lib/cpustats"
	nomadexecutor "github.com/hashicorp/nomad/drivers/shared/executor"
)

func TestIsolatedExecutorSignalExit(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			_, client, _, cmd := startTestExecutor(t, true)
			// Upstream installs its signal subscription in a goroutine. Allow
			// it to start before signaling; real task executors are long-lived.
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !client.Exited() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !client.Exited() {
				t.Fatal("executor stayed alive after signal cleanup")
			}
			if !cmd.ProcessState.Success() {
				t.Fatalf("expected successful cleanup, got %s", cmd.ProcessState)
			}
		})
	}
}

func TestIsolatedExecutorRPCAndReattach(t *testing.T) {
	executor, client, rpc, _ := startTestExecutor(t, true)
	if err := executor.Shutdown("SIGTERM", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Version(); err != nil {
		t.Fatalf("ordinary Shutdown RPC terminated executor: %v", err)
	}
	reattach := client.ReattachConfig()
	// Close only the transport, not rpc.Close(), which requests server exit.
	if err := rpc.Conn.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, recoveredClient, err := nomadexecutor.ReattachToExecutor(reattach, hclog.NewNullLogger(), cpustats.Compute{})
	if err != nil {
		t.Fatalf("executor did not survive disconnect: %v", err)
	}
	t.Cleanup(recoveredClient.Kill)
	if _, err := recovered.Version(); err != nil {
		t.Fatalf("reattached executor is unusable: %v", err)
	}
	if client.Exited() {
		t.Fatal("executor exited on connection loss")
	}
}
