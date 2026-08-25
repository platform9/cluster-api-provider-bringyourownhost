// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/pkg"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
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
	decommissionCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "minimal", "Log verbosity level (all, important, minimal, critical, none)")
	decommissionCmd.Flags().BoolVarP(&decommissionForce, "force", "f", false, "Force decommission of the host.")
}

func runDecommission(cmd *cobra.Command, args []string) {

	utils.SetConsoleOutputLevel(verbosity)

	if _, err := os.Stat(service.KubeconfigFilePath); os.IsNotExist(err) {
		fmt.Printf("kubeconfig file not found at %s. Please onboard the host first.\n", service.KubeconfigFilePath)
		os.Exit(1)
	}

	namespace, err := client.GetNamespaceFromConfig(service.KubeconfigFilePath)
	if err != nil {
		fmt.Println("Failed to get namespace from kubeconfig: " + err.Error())
		os.Exit(1)
	}

	k8sClient, err := client.GetK8sClient(service.KubeconfigFilePath)
	if err != nil {
		fmt.Println("Failed to get Kubernetes client: " + err.Error())
		os.Exit(1)
	}

	err = pkg.PerformHostOperation(k8sClient, pkg.DefaultHostIO{}, pkg.OperationDecommission, namespace, decommissionForce)
	if err != nil {
		fmt.Println("Failed to decommission host. " + err.Error())
		os.Exit(1)
	}

	utils.LogSuccess("Successfully decommissioned host from the pf9 kaapi management cluster")
}
