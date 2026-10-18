// This file is copied into Nomad's vendored executor by package.nix.
package executor

import (
	"os"
	"time"

	"github.com/hashicorp/go-hclog"
)

// Allow Nomad's existing 15-second process-exit wait plus resource destruction.
// This bounds signal cleanup only; task kill_timeout and RPCs are unchanged.
const executorSignalShutdownTimeout = 20 * time.Second

// The standalone executor entrypoint replaces this before starting its server.
// Library users (including upstream executor tests) retain upstream behavior.
// Never change this hook after serving starts.
var handleExecutorSignal = func(shutdown func() error) {
	_ = shutdown()
}

func shutdownAndExit(shutdown func() error, logger hclog.Logger, timeout time.Duration) {
	// Keep the watchdog armed until os.Exit, including while reporting errors.
	// It covers the entire Shutdown call, including deferred container.Destroy.
	time.AfterFunc(timeout, func() {
		exitWithShutdownError(logger, "executor signal cleanup timed out", "timeout", timeout)
	})
	if err := shutdown(); err != nil {
		exitWithShutdownError(logger, "executor signal cleanup failed", "error", err)
	}
	os.Exit(0)
}

func exitWithShutdownError(logger hclog.Logger, message string, args ...interface{}) {
	// Allocation filesystems can stall during host shutdown. Give failure
	// logging a small budget, but never let it defeat the shutdown watchdog.
	logged := make(chan struct{})
	go func() {
		logger.Error(message, append([]interface{}{"pid", os.Getpid()}, args...)...)
		close(logged)
	}()
	select {
	case <-logged:
	case <-time.After(100 * time.Millisecond):
	}
	os.Exit(1)
}
