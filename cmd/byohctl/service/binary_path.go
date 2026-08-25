package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// osExecutable is a var, not a direct os.Executable call, so tests can override it.
var osExecutable = os.Executable

// statPath is a var, not a direct os.Stat call, so tests can simulate the canonical path
// already being occupied without touching the real filesystem.
var statPath = os.Stat

// CheckCanonicalPath reports whether the running binary is at CanonicalBinaryPath.
func CheckCanonicalPath() (ok bool, message string) {
	exe, err := osExecutable()
	if err != nil {
		return true, ""
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	if resolved == CanonicalBinaryPath {
		return true, ""
	}

	if _, err := statPath(CanonicalBinaryPath); err == nil {
		// Something's already there, so it's package-owned - that's the whole point of
		// packaging byohctl - and therefore canonical regardless of which binary happens to
		// carry a newer version string. Never suggest overwriting it: that would fight
		// dpkg/rpm's own bookkeeping for the file, and the correct way to update it is an
		// agent package upgrade, not a manual install.
		return false, fmt.Sprintf(
			"running from %s, not %s. Use `byohctl` (via PATH) instead of this copy.",
			resolved, CanonicalBinaryPath,
		)
	}

	return false, fmt.Sprintf(
		"running from %s, not %s - this copy will not receive updates shipped in the pf9-byohost-agent package. "+
			"To fix: sudo install -m 0755 %s %s",
		resolved, CanonicalBinaryPath, resolved, CanonicalBinaryPath,
	)
}
