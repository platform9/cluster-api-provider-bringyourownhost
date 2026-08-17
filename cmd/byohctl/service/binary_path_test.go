package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statMissing and statPresent stand in for statPath's real os.Stat signature, so tests can
// simulate the canonical path being empty or already occupied without touching the real
// filesystem (real /usr/bin/byohctl state would otherwise vary by test machine).
func statMissing(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
func statPresent(string) (os.FileInfo, error) { return nil, nil }

func TestCheckCanonicalPath(t *testing.T) {
	origExecutable := osExecutable
	origStatPath := statPath
	defer func() {
		osExecutable = origExecutable
		statPath = origStatPath
	}()

	t.Run("running from the canonical path", func(t *testing.T) {
		osExecutable = func() (string, error) { return CanonicalBinaryPath, nil }

		ok, message := CheckCanonicalPath()

		if !ok {
			t.Errorf("expected ok, got warning: %q", message)
		}
		if message != "" {
			t.Errorf("expected no message, got %q", message)
		}
	})

	t.Run("os.Executable fails", func(t *testing.T) {
		osExecutable = func() (string, error) { return "", errors.New("boom") }

		ok, message := CheckCanonicalPath()

		if !ok {
			t.Errorf("expected ok when own path can't be determined, got warning: %q", message)
		}
		if message != "" {
			t.Errorf("expected no message, got %q", message)
		}
	})

	t.Run("running from a non-canonical path with nothing installed there yet", func(t *testing.T) {
		// Resolve the temp dir itself first: on macOS t.TempDir() lives under
		// /var, itself a symlink to /private/var, so CheckCanonicalPath's own
		// EvalSymlinks would otherwise report a different (also-correct) path
		// than the raw strayPath computed here.
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("failed to resolve temp dir: %v", err)
		}
		strayPath := filepath.Join(dir, "byohctl")
		if err := os.WriteFile(strayPath, []byte("fake binary"), 0o755); err != nil {
			t.Fatalf("failed to create stray binary: %v", err)
		}
		osExecutable = func() (string, error) { return strayPath, nil }
		statPath = statMissing

		ok, message := CheckCanonicalPath()

		if ok {
			t.Fatalf("expected a warning, got ok")
		}
		if !strings.Contains(message, strayPath) {
			t.Errorf("expected message to mention %q, got %q", strayPath, message)
		}
		if !strings.Contains(message, "sudo install -m 0755 "+strayPath+" "+CanonicalBinaryPath) {
			t.Errorf("expected message to contain the fix command, got %q", message)
		}
	})

	t.Run("running from a non-canonical path with something already installed at the canonical one", func(t *testing.T) {
		// Regression test: the canonical path is package-owned once anything is installed
		// there, so it's authoritative regardless of version numbers - the message must never
		// suggest overwriting it, only redirect the operator to use it instead.
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("failed to resolve temp dir: %v", err)
		}
		strayPath := filepath.Join(dir, "byohctl")
		if err := os.WriteFile(strayPath, []byte("fake binary"), 0o755); err != nil {
			t.Fatalf("failed to create stray binary: %v", err)
		}
		osExecutable = func() (string, error) { return strayPath, nil }
		statPath = statPresent

		ok, message := CheckCanonicalPath()

		if ok {
			t.Fatalf("expected a warning, got ok")
		}
		if strings.Contains(message, "sudo install") {
			t.Errorf("expected no install instruction when something is already installed, got %q", message)
		}
		if !strings.Contains(message, "Use `byohctl`") {
			t.Errorf("expected message to redirect the operator to the canonical copy, got %q", message)
		}
	})

	t.Run("running through a symlink resolves to the real path", func(t *testing.T) {
		dir := t.TempDir()
		realPath := filepath.Join(dir, "real-byohctl")
		if err := os.WriteFile(realPath, []byte("fake binary"), 0o755); err != nil {
			t.Fatalf("failed to create real binary: %v", err)
		}
		linkPath := filepath.Join(dir, "byohctl")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}
		osExecutable = func() (string, error) { return linkPath, nil }
		statPath = statMissing

		ok, message := CheckCanonicalPath()

		if ok {
			t.Fatalf("expected a warning, got ok")
		}
		if !strings.Contains(message, realPath) {
			t.Errorf("expected message to reference the resolved real path %q, got %q", realPath, message)
		}
		if strings.Contains(message, linkPath) && linkPath != realPath {
			t.Errorf("expected message to use the resolved path, not the symlink path %q: %q", linkPath, message)
		}
	})
}
