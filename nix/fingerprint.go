package nix

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/hashicorp/nomad/plugins/drivers"
	pstructs "github.com/hashicorp/nomad/plugins/shared/structs"
)

func (d *Driver) buildFingerprint() *drivers.Fingerprint {
	fp := &drivers.Fingerprint{
		Attributes:        map[string]*pstructs.Attribute{},
		Health:            drivers.HealthStateHealthy,
		HealthDescription: drivers.DriverHealthy,
	}

	// Check that nix is on PATH
	nixPath, err := exec.LookPath("nix")
	if err != nil {
		fp.Health = drivers.HealthStateUnhealthy
		fp.HealthDescription = "nix binary not found on PATH"
		if d.fingerprintSuccessful() {
			d.logger.Warn(fp.HealthDescription)
		}
		d.setFingerprintFailure()
		return fp
	}

	// Report nix version
	ver, err := nixVersion()
	if err != nil {
		d.logger.Warn("could not determine nix version", "error", err)
	} else {
		fp.Attributes["driver.nix.nix_version"] = pstructs.NewStringAttribute(ver)
	}

	fp.Attributes["driver.nix"] = pstructs.NewBoolAttribute(true)
	fp.Attributes["driver.nix.nix_path"] = pstructs.NewStringAttribute(nixPath)

	switch runtime.GOOS {
	case "linux":
		d.fingerprintLinux(fp)
	case "darwin":
		d.fingerprintDarwin(fp)
	default:
		fp.Health = drivers.HealthStateUndetected
		fp.HealthDescription = fmt.Sprintf("nix driver unsupported on %s", runtime.GOOS)
		d.setFingerprintFailure()
		return fp
	}

	d.setFingerprintSuccess()
	return fp
}

func (d *Driver) fingerprintLinux(fp *drivers.Fingerprint) {
	if os.Getuid() != 0 {
		fp.Health = drivers.HealthStateUndetected
		fp.HealthDescription = "nix driver requires root on Linux for isolation"
		d.setFingerprintFailure()
		return
	}
	fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("libcontainer")
}

func (d *Driver) fingerprintDarwin(fp *drivers.Fingerprint) {
	if sandboxAvailable() {
		fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("sandbox")
	} else {
		fp.Attributes["driver.nix.isolation"] = pstructs.NewStringAttribute("none")
	}
}
