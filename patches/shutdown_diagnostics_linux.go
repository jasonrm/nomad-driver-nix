// This file is copied into Nomad's vendored executor by package.nix.
package executor

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
)

func traceExecutorSignalShutdown(logger hclog.Logger, signal os.Signal, shutdown func() error) {
	pid := os.Getpid()
	observeSignalShutdown(shutdown, func(event string) {
		message := fmt.Sprintf("executor shutdown: pid=%d signal=%s: %s", pid, signal, event)
		// A stalled allocation filesystem must not block task cleanup. These
		// best-effort file messages may be lost if the process exits immediately.
		go logger.Warn(message)
		// Late in host shutdown, even the allocation filesystem may be gone.
		// Best-effort kernel messages are visible on the console/remote KVM.
		// Never wait for space in the kernel log buffer or require access to it.
		f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		defer f.Close()
		// Error priority keeps these temporary diagnostics visible with the
		// common quiet-boot console log level (which suppresses warnings).
		_, _ = fmt.Fprintf(f, "<3>nomad-driver-nix: %s\n", message)
	}, 5*time.Second)
}
