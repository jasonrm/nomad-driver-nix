//go:build darwin

package nix

import (
	"fmt"
	"os/exec"
	"strings"
)

// sandboxAvailable checks if sandbox-exec is available on macOS.
func sandboxAvailable() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// generateSBPL creates a Seatbelt (SBPL) profile that restricts file access
// to the specified closure paths and task directory.
func generateSBPL(closurePaths []string, taskDir string, allocDir string, profileBinPath string) string {
	var sb strings.Builder

	sb.WriteString("(version 1)\n")
	sb.WriteString("(deny default)\n\n")

	// Process control
	sb.WriteString("; Process control\n")
	sb.WriteString("(allow process-fork)\n")
	sb.WriteString("(allow signal (target self))\n")
	sb.WriteString("(allow sysctl-read)\n\n")

	// Limit process-exec to nix closure paths and /usr/bin/env (for shebangs)
	sb.WriteString("; Executable paths\n")
	for _, p := range closurePaths {
		sb.WriteString(fmt.Sprintf("(allow process-exec (subpath \"%s\"))\n", p))
	}
	sb.WriteString("(allow process-exec (literal \"/usr/bin/env\"))\n")
	sb.WriteString("\n")

	// Mach IPC — allow only common system services
	sb.WriteString("; Mach IPC\n")
	sb.WriteString("(allow mach-lookup (global-name-regex #\"^com\\.apple\\.\"))\n\n")

	// Network access
	sb.WriteString("; Network access\n")
	sb.WriteString("(allow network*)\n")
	sb.WriteString("(allow system-socket)\n\n")

	// Device access — read all, write only null/urandom/pty
	sb.WriteString("; Device access\n")
	sb.WriteString("(allow file-read* (subpath \"/dev\"))\n")
	sb.WriteString("(allow file-write* (literal \"/dev/null\"))\n")
	sb.WriteString("(allow file-write* (regex #\"^/dev/ttys\"))\n")
	sb.WriteString("(allow file-write* (regex #\"^/dev/pty\"))\n")
	// file-ioctl needed for terminal/pty operations
	sb.WriteString("(allow file-ioctl (subpath \"/dev\"))\n\n")

	// System file reads
	sb.WriteString("; System files (read-only)\n")
	sb.WriteString("(allow file-read* (literal \"/\"))\n")
	// Only specific /etc files — avoid exposing /etc/nix, /etc/ssh, etc.
	// /etc is a symlink to /private/etc on macOS, so allow both.
	sb.WriteString("(allow file-read* (literal \"/etc\"))\n")
	sb.WriteString("(allow file-read* (literal \"/private/etc\"))\n")
	sb.WriteString("(allow file-read* (literal \"/etc/hosts\"))\n")
	sb.WriteString("(allow file-read* (literal \"/private/etc/hosts\"))\n")
	sb.WriteString("(allow file-read* (literal \"/etc/protocols\"))\n")
	sb.WriteString("(allow file-read* (literal \"/private/etc/protocols\"))\n")
	sb.WriteString("(allow file-read* (literal \"/etc/services\"))\n")
	sb.WriteString("(allow file-read* (literal \"/private/etc/services\"))\n")
	// localtime is a symlink to /var/db/timezone/zoneinfo/...
	sb.WriteString("(allow file-read* (literal \"/etc/localtime\"))\n")
	sb.WriteString("(allow file-read* (literal \"/private/etc/localtime\"))\n")
	sb.WriteString("(allow file-read* (subpath \"/var/db/timezone\"))\n")
	sb.WriteString("(allow file-read* (subpath \"/private/var/db/timezone\"))\n")
	// dyld cache and system frameworks needed by all dynamically-linked binaries
	sb.WriteString("(allow file-read* (subpath \"/usr/lib\"))\n")
	sb.WriteString("(allow file-read* (subpath \"/System\"))\n\n")

	// Nix store — read only closure paths
	sb.WriteString("; Nix store closure paths\n")
	for _, p := range closurePaths {
		sb.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", p))
	}
	sb.WriteString("\n")

	// Task directory — read+write
	sb.WriteString("; Task directory\n")
	sb.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", taskDir))
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", taskDir))
	sb.WriteString("\n")

	// Alloc logs directory — write access for stdout/stderr fifos
	sb.WriteString("; Alloc log fifos\n")
	logsDir := fmt.Sprintf("%s/alloc/logs", allocDir)
	sb.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", logsDir))
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", logsDir))
	sb.WriteString("\n")

	// Temp directories — scoped to task
	sb.WriteString("; Temp directories\n")
	sb.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s/tmp\"))\n", taskDir))
	sb.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s/tmp\"))\n", taskDir))

	return sb.String()
}
