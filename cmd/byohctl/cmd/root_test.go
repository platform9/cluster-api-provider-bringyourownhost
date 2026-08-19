// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
