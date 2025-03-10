// cmd/byohctl/cmd/onboard.go
package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	username            string
	password            string
	passwordInteractive bool
	fqdn                string
	domain              string
	tenant              string
	clientToken         string
	containerdSock      string
	agentImage          string
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
	onboardCmd.Flags().BoolVarP(&passwordInteractive, "interactive", "i", false, "Prompt for password interactively")
	onboardCmd.Flags().StringVarP(&fqdn, "fqdn", "f", "", "Platform9 FQDN")
	onboardCmd.Flags().StringVarP(&domain, "domain", "d", "default", "Domain name")
	onboardCmd.Flags().StringVarP(&tenant, "tenant", "t", "", "Tenant name")
	onboardCmd.Flags().StringVarP(&clientToken, "client-token", "c", "", "Client token for authentication")
	onboardCmd.Flags().StringVar(&containerdSock, "containerd-sock", client.DefaultContainerdSock, "Path to containerd socket")
	onboardCmd.Flags().StringVar(&agentImage, "agent-image", client.DefaultAgentImage, "Agent container image to use")

	onboardCmd.MarkFlagsMutuallyExclusive("password", "interactive")

	onboardCmd.MarkFlagRequired("username")
	onboardCmd.MarkFlagRequired("password")
	onboardCmd.MarkFlagRequired("fqdn")
	onboardCmd.MarkFlagRequired("client-token")
	onboardCmd.MarkFlagRequired("tenant")

	rootCmd.AddCommand(onboardCmd)
}

func runOnboard(cmd *cobra.Command, args []string) {
	if passwordInteractive {
		fmt.Print("Enter Password: ")
		pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			utils.LogError("Failed to read password: %v", err)
			os.Exit(1)
		}
		fmt.Println() // Add newline after password input
		password = string(pwBytes)
	}
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

	// 2. Create Kubernetes client with options
	var clientOptions []client.Option
	// Only add options if non-default values are provided
	if containerdSock != client.DefaultContainerdSock {
		clientOptions = append(clientOptions, client.WithContainerdSock(containerdSock))
	}

	if agentImage != client.DefaultAgentImage {
		clientOptions = append(clientOptions, client.WithAgentImage(agentImage))
	}

	k8sClient := client.NewK8sClient(fqdn, domain, tenant, token, clientOptions...)

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
