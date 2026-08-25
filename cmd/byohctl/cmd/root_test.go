// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
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

// TestInsecureIsPersistent guards the flag being reachable from every subcommand.
// deauthorise and decommission talk to the same DU as onboard, so a flag registered only on
// onboard would leave them broken on an on-prem deployment.
func TestInsecureIsPersistent(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("insecure")
	require.NotNil(t, flag, "--insecure must be registered as a persistent flag on rootCmd")
	require.Equal(t, "false", flag.DefValue, "--insecure must default to off")

	for _, name := range []string{"onboard", "deauthorise", "decommission"} {
		t.Run(name, func(t *testing.T) {
			var found bool
			for _, sub := range rootCmd.Commands() {
				if sub.Name() == name {
					found = true
					require.NotNil(t, sub.InheritedFlags().Lookup("insecure"),
						"%s must inherit --insecure", name)
				}
			}
			require.True(t, found, "subcommand %s not registered", name)
		})
	}
}

// TestInsecureWarningNamesPersistence keeps the warning honest. The dangerous part is not
// that this command skips verification, it is that the host keeps skipping it afterwards --
// if that sentence ever gets trimmed, the operator loses the only notice they get.
func TestInsecureWarningNamesPersistence(t *testing.T) {
	require.Contains(t, insecureWarning, "kubeconfig")
	require.Contains(t, insecureWarning, "after onboarding")
}

func TestMergeConfigWithFlagsInsecure(t *testing.T) {
	t.Run("config file enables it", func(t *testing.T) {
		origInsecure := insecure
		insecure = false
		t.Cleanup(func() { insecure = origInsecure })

		mergeConfigWithFlags(&OnboardConfig{Insecure: true})
		require.True(t, insecure)
	})

	t.Run("config file does not disable an enabled flag", func(t *testing.T) {
		origInsecure := insecure
		insecure = true
		t.Cleanup(func() { insecure = origInsecure })

		mergeConfigWithFlags(&OnboardConfig{Insecure: false})
		require.True(t, insecure, "--insecure on the CLI must win over an absent config key")
	})

	t.Run("stays off when neither sets it", func(t *testing.T) {
		origInsecure := insecure
		insecure = false
		t.Cleanup(func() { insecure = origInsecure })

		mergeConfigWithFlags(&OnboardConfig{})
		require.False(t, insecure)
	})
}
