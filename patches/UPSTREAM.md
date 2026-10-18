# Upstream report draft: isolated executor survives signal cleanup

Not yet submitted upstream.

## Summary

With Nomad v1.11.3's embedded Linux/cgo executor, host shutdown sends SIGTERM
to orphaned executors. `LibcontainerExecutor.catchSignals()` calls
`Shutdown("SIGINT", 0)` and breaks out of its signal loop, but the standalone
executor's `plugin.Serve()` continues running. The host waits for those PIDs
until its shutdown timeout. This differs from intentionally keeping workloads
alive when only the Nomad agent exits.

Temporary instrumentation in nomad-driver-nix observed four executor processes:
all received SIGTERM, all returned from cleanup without error in roughly
5–13 ms, and systemd was still waiting for those same four PIDs ten seconds
later. Upstream `main` still contained this signal-loop behavior when checked.

## Proposed fix

Route signal-triggered cleanup through a package-local hook. Its default keeps
library behavior unchanged; the standalone `executor` entrypoint installs a
handler that exits 0 on successful cleanup or 1 on error. A 20-second watchdog
covers cleanup and resource destruction. Error logging is best-effort with a
100 ms budget, so blocked logging cannot defeat the watchdog.

Do not put process exit in `Executor.Shutdown`: normal task-stop RPCs also call
that method. Do not exit on transport loss: agent restart must permit
reattachment. No changes to existing task signals or grace periods are needed.

## Reproduction / regression tests

`executor_signal_linux_test.go` starts the actual isolated executor in a child
process via go-plugin. It sends SIGTERM/SIGINT and requires exit status 0 within
five seconds. This needs Linux/cgo but no task, privileges, or writable cgroups.
It also checks that a Shutdown RPC leaves the server reachable and that closing
only the gRPC transport allows reattachment.

`signal_shutdown_test.go` exercises real subprocess exits with successful,
failing, and blocked cleanup, blocked error logging, and unchanged library use.
`executor_lifecycle_test.go` launches a real workload using the universal
executor, disconnects/reconnects, verifies the original PID is alive, and stops
it through RPC without terminating its executor. These tests do not substitute
for a real Linux isolated-workload/agent-restart/reboot test.

For this repository, `nix build` applies the vendored patch and enables the
Linux integration tests through `NOMAD_TEST_PATCHED_EXECUTOR=1`. Plain Go builds
do not patch the dependency, so those isolated-executor tests skip by default.
The portable subprocess and universal-workload tests run with `go test ./patches`.

To port upstream, incorporate `signal_shutdown.go`, apply the two source hunks
without the `vendor/github.com/hashicorp/nomad/` prefix, and adapt the integration
tests' package/imports and gating. Remove this downstream patch when adopting
an upstream release with equivalent behavior.
