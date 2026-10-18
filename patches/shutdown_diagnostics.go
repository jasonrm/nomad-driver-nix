// This file is copied into Nomad's vendored executor by package.nix.
package executor

import (
	"fmt"
	"time"
)

// observeSignalShutdown deliberately preserves the executor's existing behavior:
// cleanup runs synchronously, and returning does not terminate the RPC server.
// report may be called concurrently by the watchdog and the cleanup goroutine.
func observeSignalShutdown(shutdown func() error, report func(string), warnAfter time.Duration) {
	start := time.Now()
	report("signal received; cleanup starting")
	done := make(chan struct{})
	timer := time.NewTimer(warnAfter)
	defer timer.Stop()
	go func() {
		select {
		case <-done:
			return
		case <-timer.C:
			select {
			case <-done:
				return
			default:
			}
			report(fmt.Sprintf("cleanup has not returned after %s", warnAfter))
		}
	}()

	err := shutdown()
	close(done)
	// Do not wait for the watchdog: diagnostic I/O must not hold up cleanup.
	report(fmt.Sprintf("cleanup returned after %s (error=%v); signal handler returning without exiting executor", time.Since(start), err))
}
