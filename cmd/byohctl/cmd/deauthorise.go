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

var deauthoriseCmd = &cobra.Command{
	Use:   "deauthorise",
	Short: "Deauthorise a host from the respective byo cluster",
	Long: `Deauthorise a host from the respective byo cluster.
This command will:
1. Authenticate with Platform9
2. Deauthorise the host from the byo cluster
3. Host must have been part of some cluster before deauthorisation`,
	Example: `  byohctl deauthorise`,
	Run:     runDeauthorise,
}

func init() {
	rootCmd.AddCommand(deauthoriseCmd)
}

func runDeauthorise(cmd *cobra.Command, args []string) {

	namespace, err := client.GetNamespaceFromConfig(service.KubeconfigFilePath)
	if err != nil {
		fmt.Println("Failed to get namespace from kubeconfig: " + err.Error())
		os.Exit(1)
	}

	err = pkg.PerformHostOperation(pkg.OperationDeauthorise, namespace)
	if err != nil {
		fmt.Println("Failed to deauthorise host. " + err.Error())
		os.Exit(1)
	}

	utils.LogSuccess("Successfully deauthorised host from the byo cluster")

}
