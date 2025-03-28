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

var decommissionCmd = &cobra.Command{
	Use:   "decommission",
	Short: "Decommission a host from the pf9 kaapi management cluster",
	Long: `Decommission a host from the pf9 kaapi management cluster.
This command will:
1. Authenticate with Platform9
2. Decommission the host from the pf9 kaapi management cluster
3. If host is part of some cluster, decommission will deauthorise the host first and then decommission`,
	Example: `  byohctl decommission`,
	Run:     runDecommission,
}

func init() {
	rootCmd.AddCommand(decommissionCmd)
}

func runDecommission(cmd *cobra.Command, args []string) {

	namespace, err := client.GetNamespaceFromConfig(service.KubeconfigFilePath)
	if err != nil {
		fmt.Println("Failed to get namespace from kubeconfig: " + err.Error())
		os.Exit(1)
	}

	err = pkg.PerformHostOperation(pkg.OperationDecommission, namespace)
	if err != nil {
		fmt.Println("Failed to decommission host. " + err.Error())
		os.Exit(1)
	}

	utils.LogSuccess("Successfully decommissioned host from the pf9 kaapi management cluster")
}
