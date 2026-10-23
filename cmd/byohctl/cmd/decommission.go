// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/pkg"
	"github.com/spf13/cobra"
)

var decommissionForce bool

var decommissionCmd = &cobra.Command{
	Use:   "decommission",
	Short: "Decommission a host from the pf9 kaapi management cluster",
	Long: `Decommission a host from the pf9 kaapi management cluster.
This command will:
1. Authenticate with Platform9
2. Decommission the host from the pf9 kaapi management cluster
3. If host is part of some cluster, decommission will deauthorise the host first and then decommission`,
	Example: `  byohctl decommission -v all`,
	Run:     runDecommission,
}

func init() {
	rootCmd.AddCommand(decommissionCmd)
	decommissionCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "minimal", "Log verbosity level. Requires one of: all, important, minimal, critical.\nOmitting the flag will show minimal verbosity")
	decommissionCmd.Flags().BoolVarP(&decommissionForce, "force", "f", false, "Forcefully decommission the host.")
}

func runDecommission(cmd *cobra.Command, args []string) {
	runHostOperation(cmd, pkg.OperationDecommission, decommissionForce, "Failed to decommission host. ", "Successfully decommissioned host from the pf9 kaapi management cluster")
}
