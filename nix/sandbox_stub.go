//go:build !darwin

package nix

// sandboxAvailable returns false on non-darwin platforms.
func sandboxAvailable() bool {
	return false
}

// generateSBPL is a no-op on non-darwin platforms.
func generateSBPL(closurePaths []string, taskDir string, allocDir string, profileBinPath string) string {
	return ""
}
