// cmd/byohctl/cmd/onboard.go
package cmd

import (
	"os"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
)

var (
	username    string
	password    string
	fqdn        string
	domain      string
	tenant      string
	clientToken string
)

var rootCmd = &cobra.Command{
	Use:   "byohctl",
	Short: "BYOH control tool for Platform9",
	Long: `BYOH (Bring Your Own Host) control tool for Platform9.
This tool helps onboard hosts to your Platform9 deployment.`,
}

func Execute() error {
	return rootCmd.Execute()
}

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Onboard a host to Platform9",
	Long: `Onboard a host to Platform9 management plane.
This command will:
1. Authenticate with Platform9
2. Get required configuration
3. Setup the host for management`,
	Example: `  byohctl onboard -u admin@platform9.com -p password -f your-fqdn.platform9.com
  byohctl onboard -u admin@platform9.com -p password -f your-fqdn.platform9.com -d custom-domain -t custom-tenant`,
	Run: runOnboard,
}

func init() {
	onboardCmd.Flags().StringVarP(&username, "username", "u", "", "Username for authentication")
	onboardCmd.Flags().StringVarP(&password, "password", "p", "", "Password for authentication")
	onboardCmd.Flags().StringVarP(&fqdn, "fqdn", "f", "", "Platform9 FQDN")
	onboardCmd.Flags().StringVarP(&domain, "domain", "d", "default", "Domain name")
	onboardCmd.Flags().StringVarP(&tenant, "tenant", "t", "s", "Tenant name")
	onboardCmd.Flags().StringVarP(&clientToken, "client-token", "c", "", "Client token for authentication")

	onboardCmd.MarkFlagRequired("username")
	onboardCmd.MarkFlagRequired("password")
	onboardCmd.MarkFlagRequired("fqdn")
	onboardCmd.MarkFlagRequired("client-token")

	rootCmd.AddCommand(onboardCmd)
}

func runOnboard(cmd *cobra.Command, args []string) {
	start := time.Now()
	defer utils.TrackTime(start, "Total onboarding process")

	utils.LogInfo("Starting host onboarding process")
	utils.LogInfo("Using FQDN: %s, Domain: %s, Tenant: %s", fqdn, domain, tenant)

	// 1. Get authentication token
	authClient := client.NewAuthClient(fqdn, clientToken)
	token, err := authClient.GetToken(username, password)
	if err != nil {
		utils.LogError("Failed to get authentication token: %v", err)
		os.Exit(1)
	}

	// 2. Create Kubernetes client
	k8sClient := client.NewK8sClient(fqdn, domain, tenant, token)

	// 3. Save kubeconfig
	utils.LogInfo("Saving kubeconfig file")
	err = k8sClient.SaveKubeConfig("byoh-bootstrap-kc")
	if err != nil {
		utils.LogError("Failed to save kubeconfig: %v", err)
		os.Exit(1)
	}

	// 4. Run BYOH agent
	utils.LogInfo("Starting BYOH agent")
	err = k8sClient.RunByohAgent()
	if err != nil {
		utils.LogError("Failed to run BYOH agent: %v", err)
		os.Exit(1)
	}

	utils.LogSuccess("Successfully onboarded the host")
}
