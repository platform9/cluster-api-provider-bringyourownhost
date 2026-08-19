// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
)

// insecure is set by the --insecure persistent flag. It is package-level rather than
// threaded through because both the onboard flow and the kubeconfig writers need it.
var insecure bool

// insecureWarning is printed whenever --insecure is in effect. It names the lasting
// consequence, not just the immediate one: the setting is written into the kubeconfig the
// host agent uses, so it governs the agent for the life of the host rather than for the
// duration of this command.
const insecureWarning = `WARNING: --insecure disables TLS certificate verification for management plane
         API calls and is recorded in the host agent's kubeconfig, so it stays in
         effect for this host after onboarding completes. Use only on trusted networks.`

var rootCmd = &cobra.Command{
	Use:   "byohctl",
	Short: "BYOH control tool for Platform9",
	Long: `BYOH (Bring Your Own Host) control tool for Platform9.
This tool helps onboard hosts to your Platform9 deployment.`,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Initialize loggers
		if err := utils.InitLoggers(service.ByohDir, true); err != nil {
			return fmt.Errorf("failed to initialize loggers: %v", err)
		}
		// onboard's own package install is about to make this warning's advice moot
		// (it installs the canonical copy itself), so skip it there - it would otherwise
		// fire on every single onboarding, the one invocation where it's guaranteed noise.
		if cmd.Name() != "onboard" {
			if ok, msg := service.CheckCanonicalPath(); !ok {
				utils.LogWarn("%s", msg)
			}
		}
		if insecure {
			fmt.Fprintln(cmd.ErrOrStderr(), insecureWarning)
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&insecure, "insecure", false,
		"Skip TLS certificate verification for management plane API calls. Also recorded in the "+
			"host agent's kubeconfig, so it persists after onboarding. Intended for on-prem "+
			"deployments serving a self-signed or private-CA certificate.")
}

func Execute() error {
	return rootCmd.Execute()
}
