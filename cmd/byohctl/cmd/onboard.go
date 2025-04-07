// cmd/byohctl/cmd/onboard.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Onboard a host to Platform9",
	Long: `Onboard a host to Platform9 management plane.
This command will:
1. Authenticate with Platform9
2. Get required configuration
3. Setup the host for management`,
	Example: `  byohctl onboard -u your-fqdn.platform9.com -e admin@platform9.com -c client-token
  byohctl onboard -u your-fqdn.platform9.com -e admin@platform9.com -c client-token -d custom-domain -t custom-tenant`,
	Run: runOnboard,
}

func init() {
	onboardCmd.Flags().StringVarP(&fqdn, "url", "u", "", "Platform9 FQDN")
	onboardCmd.MarkFlagRequired("url")
	onboardCmd.Flags().StringVarP(&username, "username", "e", "", "Platform9 username")
	onboardCmd.MarkFlagRequired("username")
	onboardCmd.Flags().StringVarP(&password, "password", "p", "", "Platform9 password")
	onboardCmd.Flags().BoolVar(&passwordInteractive, "password-interactive", false, "Enter password interactively")
	onboardCmd.Flags().StringVarP(&clientToken, "client-token", "c", "", "Client token for authentication")
	onboardCmd.MarkFlagRequired("client-token")
	onboardCmd.Flags().StringVarP(&domain, "domain", "d", "default", "Platform9 domain")
	onboardCmd.Flags().StringVarP(&tenant, "tenant", "t", "service", "Platform9 tenant")
	onboardCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "minimal", "Log verbosity level (all, important, minimal, critical, none)")
	onboardCmd.MarkFlagsMutuallyExclusive("password", "password-interactive")

	rootCmd.AddCommand(onboardCmd)
}

// Check if running on Ubuntu
func isUbuntuSystem() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "Ubuntu")
}

func runOnboard(cmd *cobra.Command, args []string) {
	// Check if running on Ubuntu system
	if !isUbuntuSystem() {
		fmt.Println("Error: This command requires an Ubuntu system")
		os.Exit(1)
	}

	// Continue with interactive password if needed
	if passwordInteractive {
		fmt.Print("Enter Password: ")
		pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			utils.LogError("Failed to read password: %v", err)
			os.Exit(1)
		}
		if len(pwBytes) == 0 {
			utils.LogError("Password cannot be empty")
			os.Exit(1)
		}
		fmt.Println() // Add newline after password input
		password = string(pwBytes)
	}

	// Check if service present
	out, err := service.RunWithStdout(service.Systemctl, service.SystemctlServiceExists...)
	if err != nil {
		utils.LogSuccess("Byoh service is not installed, proceeding with onboarding")
	} else if strings.Contains(out, service.ByohAgentServiceName) {
		utils.LogError("pf9-byohost-agent service is already installed on this host. Host already onboarded in some tenant.")
		os.Exit(1)
	}

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

	// 3. Prepare directories
	utils.LogInfo("Preparing directory structure for BYOH agent")
	homeDir, err = os.UserHomeDir()
	if err != nil {
		utils.LogError("Error getting home directory: %v", err)
		os.Exit(1)
	}
	byohDir = filepath.Join(homeDir, service.ByohConfigDir)
	if err := service.PrepareAgentDirectory(byohDir); err != nil {
		utils.LogError("Failed to prepare agent directory: %v", err)
		os.Exit(1)
	}

	// 4. Save kubeconfig
	utils.LogInfo("Saving kubeconfig from bootstrap secret")
	if err := k8sClient.SaveKubeConfig("byoh-bootstrap-kc"); err != nil {
		utils.LogError("Failed to save kubeconfig: %v", err)
		os.Exit(1)
	}

	// 5. Create packages directory for downloads
	pkgDir := filepath.Join(byohDir, "packages")
	if err := os.MkdirAll(pkgDir, service.DefaultDirPerms); err != nil {
		utils.LogError("Failed to create packages directory: %v", err)
		os.Exit(1)
	}

	// 6. Setup agent (download and install)
	utils.LogInfo("Setting up BYOH agent")
	err = service.SetupAgent(pkgDir)
	if err != nil {
		utils.LogError("Failed to setup agent: %v", err)
		os.Exit(1)
	}

	utils.LogSuccess("Successfully onboarded the host")

	timeElapsed := time.Since(start)
	utils.LogDebug("Time elapsed: %s", timeElapsed)

	utils.LogSuccess("BYOH Agent Service logs are available at:")
	utils.LogSuccess("   - Agent service logs: %s", service.ByohAgentLogPath)
	utils.LogSuccess("   - Check service status: sudo systemctl status pf9-byohost-agent.service")
}
