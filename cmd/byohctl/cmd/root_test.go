package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
)

// The canonical-path warning is real (unmocked) here: a `go test` binary is
// never actually /usr/bin/byohctl, so CheckCanonicalPath reliably returns a
// warning in any test environment - exactly what's needed to prove onboard
// suppresses it while other commands don't.
func runPersistentPreRun(t *testing.T, cmdName string) string {
	t.Helper()
	origByohDir := service.ByohDir
	service.ByohDir = t.TempDir()
	defer func() { service.ByohDir = origByohDir }()

	fakeCmd := &cobra.Command{Use: cmdName}
	if err := rootCmd.PersistentPreRunE(fakeCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE failed: %v", err)
	}
	utils.CloseLoggers()

	debugLog, err := os.ReadFile(filepath.Join(service.ByohDir, "byoh-agent-debug.log"))
	if err != nil {
		t.Fatalf("failed to read debug log: %v", err)
	}
	return string(debugLog)
}

func TestPersistentPreRunSkipsCanonicalPathWarningForOnboard(t *testing.T) {
	t.Run("onboard does not get the warning", func(t *testing.T) {
		log := runPersistentPreRun(t, "onboard")
		if strings.Contains(log, "sudo install -m 0755") {
			t.Errorf("expected no canonical-path warning for onboard, got log: %q", log)
		}
	})

	t.Run("other commands do get the warning", func(t *testing.T) {
		log := runPersistentPreRun(t, "version")
		if !strings.Contains(log, "sudo install -m 0755") {
			t.Errorf("expected a canonical-path warning for a non-onboard command, got log: %q", log)
		}
	})
}
