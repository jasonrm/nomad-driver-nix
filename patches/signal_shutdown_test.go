//go:build linux || darwin

package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
)

// Run the real os.Exit paths in a subprocess; an injected fake exit would miss
// the original bug (cleanup returns but the process remains alive).
func TestSignalShutdownProcess(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		for _, mode := range []string{"success", "error", "blocked", "blocked-log", "error-log", "library"} {
			t.Run(sig.String()+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSignalShutdownHelper$")
				// The race runtime otherwise sleeps for one second inside os.Exit,
				// longer than our deliberately shortened test watchdog.
				cmd.Env = append(os.Environ(), "NOMAD_SHUTDOWN_HELPER="+mode, "GORACE=atexit_sleep_ms=0")
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				})
				scanner := bufio.NewScanner(stdout)
				if !scanner.Scan() || scanner.Text() != "ready" {
					_ = cmd.Wait()
					t.Fatalf("subprocess not ready: %s", stderr.String())
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				var output []string
				for scanner.Scan() {
					output = append(output, scanner.Text())
				}
				err = cmd.Wait()
				if ctx.Err() != nil {
					t.Fatalf("executor failed to exit within deadline: %s", stderr.String())
				}
				wantCode := 1
				if mode == "success" || mode == "library" {
					wantCode = 0
				}
				if got := cmd.ProcessState.ExitCode(); got != wantCode {
					t.Fatalf("exit code %d, want %d: %v\n%s", got, wantCode, err, stderr.String())
				}
				wantOutput := "cleanup"
				if mode == "library" {
					wantOutput += "\nlibrary-returned"
				}
				if strings.Join(output, "\n") != wantOutput {
					t.Fatalf("unexpected cleanup/control flow: %q, want %q", output, wantOutput)
				}
				if mode == "error" && !strings.Contains(stderr.String(), "executor signal cleanup failed") {
					t.Fatalf("missing cleanup error: %s", stderr.String())
				}
				if mode == "blocked" && !strings.Contains(stderr.String(), "executor signal cleanup timed out") {
					t.Fatalf("missing timeout error: %s", stderr.String())
				}
			})
		}
	}
}

type blockedLogWriter struct{}

func (blockedLogWriter) Write([]byte) (int, error) { select {} }

func TestSignalShutdownHelper(t *testing.T) {
	mode := os.Getenv("NOMAD_SHUTDOWN_HELPER")
	if mode == "" {
		return
	}
	logger := hclog.New(&hclog.LoggerOptions{Output: os.Stderr})
	if mode == "blocked-log" || mode == "error-log" {
		logger = hclog.New(&hclog.LoggerOptions{Output: blockedLogWriter{}})
	}
	if mode != "library" {
		handleExecutorSignal = func(shutdown func() error) {
			shutdownAndExit(shutdown, logger, 200*time.Millisecond)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	fmt.Println("ready")
	<-signals
	handleExecutorSignal(func() error {
		fmt.Println("cleanup")
		switch mode {
		case "blocked", "blocked-log":
			select {}
		case "error", "error-log":
			return errors.New("injected cleanup failure")
		default:
			return nil
		}
	})
	if mode == "library" {
		// Embedding the library must not take down its hosting process.
		fmt.Println("library-returned")
		os.Exit(0)
	}
	os.Exit(42) // A production signal handler must never return.
}
