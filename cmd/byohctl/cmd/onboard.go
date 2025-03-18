// cmd/byohctl/cmd/onboard.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
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
	verbosity           string
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
	onboardCmd.Flags().StringVarP(&username, "username", "u", "", "Platform9 username")
	onboardCmd.MarkFlagRequired("username")
	onboardCmd.Flags().StringVarP(&password, "password", "p", "", "Platform9 password")
	onboardCmd.Flags().BoolVar(&passwordInteractive, "password-interactive", false, "Enter password interactively")
	onboardCmd.Flags().StringVarP(&fqdn, "fqdn", "f", "", "Platform9 FQDN")
	onboardCmd.MarkFlagRequired("fqdn")
	onboardCmd.Flags().StringVarP(&domain, "domain", "d", "default", "Platform9 domain")
	onboardCmd.Flags().StringVarP(&tenant, "tenant", "t", "service", "Platform9 tenant")
	onboardCmd.Flags().StringVar(&clientToken, "client-token", "", "Client token for authentication")
	onboardCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "minimal", "Log verbosity level (all, important, minimal, critical, none)")
	onboardCmd.MarkFlagsMutuallyExclusive("password", "password-interactive")
	onboardCmd.MarkFlagRequired("username")
	onboardCmd.MarkFlagRequired("password")
	onboardCmd.MarkFlagRequired("fqdn")
	onboardCmd.MarkFlagRequired("client-token")
	onboardCmd.MarkFlagRequired("tenant")

	rootCmd.AddCommand(onboardCmd)
}

func runOnboard(cmd *cobra.Command, args []string) {
	// Initialize loggers
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error getting user home directory: %v\n", err)
		os.Exit(1)
	}
	byohDir := filepath.Join(homeDir, ".byoh")
	// Initialize loggers with debug enabled for file logs
	if err = utils.InitLoggers(byohDir, true); err != nil {
		fmt.Printf("Error initializing loggers: %v\n", err)
		os.Exit(1)
	}
	defer utils.CloseLoggers()

	// Set console output level based on verbosity flag
	utils.SetConsoleOutputLevel(verbosity)

	// Continue with interactive password if needed
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

	utils.LogDebug("Starting host onboarding process")
	utils.LogDebug("Using FQDN: %s, Domain: %s, Tenant: %s", fqdn, domain, tenant)
	utils.LogDebug("Verbosity level set to: %s", verbosity)

	// 1. Get authentication token
	utils.LogDebug("Getting authentication token for user %s", username)
	authClient := client.NewAuthClient(fqdn, clientToken)
	token, err := authClient.GetToken(username, password)
	if err != nil {
		utils.LogError("Failed to get authentication token: %v", err)
		os.Exit(1)
	}

	// 2. Create Kubernetes client
	k8sClient := client.NewK8sClient(fqdn, domain, tenant, token)

	// 3. Save kubeconfig
	utils.LogInfo("Starting BYOH agent installation process")
	// This call will handle kubeconfig saving and agent setup together
	err = k8sClient.RunByohAgent("byoh-bootstrap-kc")
	if err != nil {
		utils.LogError("Failed to install BYOH agent: %v", err)
		os.Exit(1)
	}

	utils.LogSuccess("Successfully onboarded the host")

	timeElapsed := time.Since(start)
	utils.LogDebug("Time elapsed: %s", timeElapsed)

	// Get the user's home directory for logging agent setup info
	homeDir, err = os.UserHomeDir()
	if err != nil {
		utils.LogError("Error getting home directory: %v", err)
		return
	}

	utils.LogSuccess("BYOH Agent Service logs are available at:")
	utils.LogSuccess("   - Agent service logs: %s", service.ByohAgentLogPath)
	utils.LogSuccess("   - Check service status: sudo systemctl status pf9-byohost-agent.service")
}
