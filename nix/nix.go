package nix

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/helper/pluginutils/hclutils"
)

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// NixOptions holds optional nix CLI flags for remote builders and substituters.
type NixOptions struct {
	Builders               []string
	ExtraSubstituters      []string
	ExtraTrustedPublicKeys []string
	PostBuildHook          string
	NetrcFile              string
}

func (o *NixOptions) Args() []string {
	var args []string
	if len(o.Builders) > 0 {
		args = append(args, "--builders", strings.Join(o.Builders, " ; "))
	}
	if len(o.ExtraSubstituters) > 0 {
		args = append(args, "--extra-substituters", strings.Join(o.ExtraSubstituters, " "))
	}
	if len(o.ExtraTrustedPublicKeys) > 0 {
		args = append(args, "--extra-trusted-public-keys", strings.Join(o.ExtraTrustedPublicKeys, " "))
	}
	if o.PostBuildHook != "" {
		args = append(args, "--post-build-hook", o.PostBuildHook)
	}
	if o.NetrcFile != "" {
		args = append(args, "--netrc-file", o.NetrcFile)
	}
	return args
}

// ProgressFunc is called with human-readable progress messages during nix operations.
type ProgressFunc func(message string)

// nixAction represents a parsed nix internal-json log event.
type nixAction struct {
	Action string          `json:"action"`
	ID     uint64          `json:"id,omitempty"`
	Level  int             `json:"level,omitempty"`
	Type   int             `json:"type,omitempty"`
	Text   string          `json:"text,omitempty"`
	Msg    string          `json:"msg,omitempty"`
	Fields json.RawMessage `json:"fields,omitempty"`
}

// Nix activity type constants (from nix logging.hh).
const (
	actCopyPath      = 100
	actFileTransfer  = 101
	actCopyPaths     = 103
	actBuilds        = 104
	actBuild         = 105
	actSubstitute    = 108
	actPostBuildHook = 110
	actFetchTree     = 112
)

// Nix result type constants.
const (
	resBuildLogLine = 101
	resSetPhase     = 104
)

// NixPrepResult holds the results of preparing Nix packages for a task.
type NixPrepResult struct {
	// ProfilePath is the resolved Nix profile store path.
	ProfilePath string
	// Mounts maps host paths to container paths (for isolated execution).
	Mounts hclutils.MapStrStr
	// BinPath is the profile's bin directory (for non-isolated PATH).
	BinPath string
	// ClosurePaths lists all store paths in the closure.
	ClosurePaths []string
}

func prepareNixPackages(taskDir string, packages []string, nixpkgs string, opts *NixOptions, logger hclog.Logger, progress ProgressFunc) (*NixPrepResult, error) {
	mounts := make(hclutils.MapStrStr)

	profileLink := filepath.Join(taskDir, "current-profile")
	// Remove any stale profile from a previous attempt (e.g., task restart)
	// to avoid "already added" warnings from nix profile add. Nix creates
	// generation links (current-profile-*-link) alongside the main symlink.
	if matches, err := filepath.Glob(profileLink + "*"); err == nil {
		for _, m := range matches {
			os.Remove(m)
		}
	}
	profile, err := nixBuildProfile(taskDir, packages, profileLink, opts, logger, progress)
	if err != nil {
		return nil, fmt.Errorf("build of nix profile failed: %v", err)
	}

	// Get all store paths in the closure directly from the profile.
	// This replaces the old closureNix approach that required x86_64-linux.
	requisites, err := nixRequisites(profile, opts)
	if err != nil {
		return nil, fmt.Errorf("couldn't determine nix requisites: %v", err)
	}

	logger.Info("nix", "closure-paths", len(requisites.Paths), "closure-size", formatSize(requisites.TotalSize), "profile", profile)
	if progress != nil {
		progress(fmt.Sprintf("Nix closure: %d paths, %s (profile: %s)", len(requisites.Paths), formatSize(requisites.TotalSize), profile))
	}

	mounts[profile] = profile

	if entries, err := os.ReadDir(profile); err != nil {
		return nil, fmt.Errorf("couldn't read profile directory: %w", err)
	} else {
		for _, entry := range entries {
			if name := entry.Name(); name != "etc" {
				mounts[filepath.Join(profile, name)] = "/" + name
				continue
			}

			etcEntries, err := os.ReadDir(filepath.Join(profile, "etc"))
			if err != nil {
				return nil, fmt.Errorf("couldn't read profile's /etc directory: %w", err)
			}

			for _, etcEntry := range etcEntries {
				etcName := etcEntry.Name()
				mounts[filepath.Join(profile, "etc", etcName)] = "/etc/" + etcName
			}
		}
	}

	for _, requisite := range requisites.Paths {
		mounts[requisite] = requisite
	}

	return &NixPrepResult{
		ProfilePath:  profile,
		Mounts:       mounts,
		BinPath:      filepath.Join(profile, "bin"),
		ClosurePaths: requisites.Paths,
	}, nil
}

func nixBuildProfile(taskDir string, flakes []string, link string, opts *NixOptions, logger hclog.Logger, progress ProgressFunc) (string, error) {
	args := []string{
		"--extra-experimental-features", "nix-command",
		"--extra-experimental-features", "flakes",
		"-v",
		"--print-build-logs",
		"--log-format", "internal-json",
	}
	args = append(args, opts.Args()...)
	args = append(args, "profile", "add", "--profile", link)
	args = append(args, flakes...)
	cmd := exec.Command("nix", args...)
	cmd.Dir = taskDir

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start nix: %v", err)
	}

	var stderrBuf bytes.Buffer
	scanner := bufio.NewScanner(io.TeeReader(stderrPipe, &stderrBuf))

	// Buffer for combining multi-line msg events (e.g. "this derivation will be built:"
	// followed by indented store paths).
	var pendingMsgs []string
	flushPendingMsg := func() {
		if len(pendingMsgs) > 0 && progress != nil {
			progress(strings.Join(pendingMsgs, " "))
			pendingMsgs = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		nixJSON, isNixJSON := strings.CutPrefix(line, "@nix ")
		if !isNixJSON {
			logger.Info("nix", "output", line)
			continue
		}

		var action nixAction
		if err := json.Unmarshal([]byte(nixJSON), &action); err != nil {
			logger.Debug("nix", "unparseable-json", line)
			continue
		}

		switch action.Action {
		case "msg":
			msg := ansiEscapeRe.ReplaceAllString(action.Msg, "")
			// Nix verbosity: 0=Error, 1=Warn, 2=Notice, 3=Info, 4=Talkative, 5+=Debug
			switch {
			case action.Level <= 1:
				logger.Warn("nix", "msg", msg)
			case action.Level <= 3:
				logger.Info("nix", "msg", msg)
			default:
				logger.Trace("nix", "msg", msg)
			}
			if action.Level <= 3 {
				// Indented lines (e.g. store paths) belong with the previous header
				if strings.HasPrefix(msg, " ") || strings.HasPrefix(msg, "\t") {
					if len(pendingMsgs) > 0 {
						pendingMsgs = append(pendingMsgs, strings.TrimSpace(msg))
					}
				} else {
					flushPendingMsg()
					// Collapse internal newlines (e.g. lock file warnings)
					collapsed := strings.Join(strings.Fields(msg), " ")
					pendingMsgs = append(pendingMsgs, collapsed)
				}
			}
		case "start":
			flushPendingMsg()
			text := ansiEscapeRe.ReplaceAllString(action.Text, "")
			switch action.Type {
			case actBuild:
				logger.Info("nix", "build-start", text)
				if progress != nil {
					progress(text)
				}
			case actFetchTree:
				logger.Info("nix", "fetch-start", text)
				if progress != nil {
					progress(text)
				}
			case actCopyPaths:
				logger.Info("nix", "copy-paths", text)
				if progress != nil && text != "" {
					progress(text)
				}
			case actBuilds:
				logger.Info("nix", "builds", text)
				if progress != nil && text != "" {
					progress(text)
				}
			case actSubstitute:
				desc := text
				if desc == "" {
					var fields []string
					if err := json.Unmarshal(action.Fields, &fields); err == nil && len(fields) >= 2 {
						desc = fmt.Sprintf("substituting '%s' from '%s'", fields[0], fields[1])
					}
				}
				logger.Debug("nix", "substitute", desc)
			case actCopyPath:
				logger.Info("nix", "copy-path", text)
				if progress != nil {
					progress(text)
				}
			case actPostBuildHook:
				logger.Info("nix", "post-build-hook", text)
				if progress != nil {
					progress(text)
				}
			case actFileTransfer:
				logger.Info("nix", "download", text)
				if progress != nil {
					progress(text)
				}
			default:
				logger.Debug("nix", "activity", text, "type", action.Type)
			}
		case "result":
			switch action.Type {
			case resBuildLogLine:
				var fields []string
				if err := json.Unmarshal(action.Fields, &fields); err == nil && len(fields) > 0 {
					logger.Info("nix", "build-log", fields[0])
					if progress != nil {
						progress(fields[0])
					}
				}
			case resSetPhase:
				var fields []string
				if err := json.Unmarshal(action.Fields, &fields); err == nil && len(fields) > 0 {
					logger.Info("nix", "phase", fields[0])
				}
			}
		case "stop":
			// Activity completed
		}
	}
	flushPendingMsg()

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("%v failed: %s. Err: %v", cmd.Args, stderrBuf.String(), err)
	}

	if target, err := os.Readlink(link); err == nil {
		return os.Readlink(filepath.Join(filepath.Dir(link), target))
	} else {
		return "", err
	}
}

type nixPathInfo struct {
	Path             string   `json:"path"`
	NarHash          string   `json:"narHash"`
	NarSize          uint64   `json:"narSize"`
	References       []string `json:"references"`
	Deriver          string   `json:"deriver"`
	RegistrationTime uint64   `json:"registrationTime"`
	Signatures       []string `json:"signatures"`
}

// nixRequisitesResult holds the closure paths and total size.
type nixRequisitesResult struct {
	Paths     []string
	TotalSize uint64
}

func nixRequisites(path string, opts *NixOptions) (*nixRequisitesResult, error) {
	args := []string{
		"--extra-experimental-features", "nix-command",
		"--extra-experimental-features", "flakes",
	}
	args = append(args, opts.Args()...)
	args = append(args, "path-info", "--json", "--recursive", path)
	cmd := exec.Command("nix", args...)

	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout

	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v failed: %s. Err: %v", cmd.Args, stderr.String(), err)
	}

	// nix path-info --json output format changed across versions:
	// older nix returns an array: [{path: ...}, ...]
	// newer nix (≥2.19) returns an object keyed by store path: {"/nix/store/...": {...}, ...}
	data := stdout.Bytes()
	result := &nixRequisitesResult{}

	// Try object format first (newer nix)
	var objResult map[string]*nixPathInfo
	if err := json.Unmarshal(data, &objResult); err == nil {
		for storePath, info := range objResult {
			result.Paths = append(result.Paths, storePath)
			result.TotalSize += info.NarSize
		}
	} else {
		// Fall back to array format (older nix)
		var arrResult []*nixPathInfo
		if err := json.Unmarshal(data, &arrResult); err != nil {
			return nil, err
		}
		for _, r := range arrResult {
			result.Paths = append(result.Paths, r.Path)
			result.TotalSize += r.NarSize
		}
	}

	return result, nil
}

// nixVersion returns the installed nix version string.
func nixVersion() (string, error) {
	cmd := exec.Command("nix", "--version")
	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	// Output is like "nix (Nix) 2.18.1"
	out := strings.TrimSpace(stdout.String())
	parts := strings.Fields(out)
	if len(parts) >= 3 {
		return parts[len(parts)-1], nil
	}
	return out, nil
}

func formatSize(bytes uint64) string {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	case bytes >= mib:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(mib))
	default:
		return fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	}
}
