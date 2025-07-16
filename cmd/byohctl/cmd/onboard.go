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
	"gopkg.in/yaml.v2"
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
	regionName          string
	configFile          string
)

var onboardCmd = &cobra.Command{
	Use:   "onboard",
	Short: "Onboard a host to Platform9",
	Long: `Onboard a host to Platform9 management plane.
This command will:
1. Authenticate with Platform9
2. Get required configuration
3. Setup the host for management

You can provide input values via CLI flags or a YAML config file using --config/-f, or a combination of both..
CLI flags take precedence over config file values.`,
	Example: `  byohctl onboard -u your-fqdn.platform9.com -e admin@platform9.com -c client-token
  byohctl onboard -u your-fqdn.platform9.com -e admin@platform9.com -c client-token -d custom-domain -t custom-tenant
  byohctl onboard --config onboard-config.yaml
  byohctl onboard --config onboard-config.yaml --username overrideuser`,
	Run: runOnboard,
}

func init() {
	AddOnboardFlags(
		onboardCmd,
		&fqdn, &username, &password, &passwordInteractive,
		&clientToken, &domain, &tenant, &verbosity, &regionName, &configFile,
	)
	rootCmd.AddCommand(onboardCmd)
}

// AddOnboardFlags adds all flags for the onboard command to the given cobra.Command.
func AddOnboardFlags(cmd *cobra.Command,
	fqdn *string, username *string, password *string, passwordInteractive *bool,
	clientToken *string, domain *string, tenant *string, verbosity *string, regionName *string, configFile *string,
) {
	cmd.Flags().StringVarP(fqdn, "url", "u", "", "Platform9 FQDN")
	cmd.MarkFlagRequired("url")
	cmd.Flags().StringVarP(username, "username", "e", "", "Platform9 username")
	cmd.MarkFlagRequired("username")
	cmd.Flags().StringVarP(password, "password", "p", "", "Platform9 password")
	cmd.Flags().BoolVar(passwordInteractive, "password-interactive", false, "Enter password interactively")
	cmd.Flags().StringVarP(clientToken, "client-token", "c", "", "Client token for authentication")
	cmd.MarkFlagRequired("client-token")
	cmd.Flags().StringVarP(domain, "domain", "d", "default", "Platform9 domain")
	cmd.Flags().StringVarP(tenant, "tenant", "t", "service", "Platform9 tenant")
	cmd.Flags().StringVarP(verbosity, "verbosity", "v", "minimal", "Log verbosity level (all, important, minimal, critical, none)")
	cmd.MarkFlagsMutuallyExclusive("password", "password-interactive")
	cmd.Flags().StringVarP(regionName, "region", "r", "", "Platform9 region where you want to onboard this host")
	cmd.MarkFlagRequired("region")
	cmd.Flags().StringVarP(configFile, "config", "f", "", "Path to onboarding config YAML file")
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

type OnboardConfig struct {
	URL         string `yaml:"url"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	ClientToken string `yaml:"client-token"`
	Domain      string `yaml:"domain"`
	Tenant      string `yaml:"tenant"`
	Verbosity   string `yaml:"verbosity"`
	Region      string `yaml:"region"`
}

func LoadOnboardConfig(path string) (*OnboardConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg OnboardConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Helper to merge config values with CLI flags
func mergeConfigWithFlags(cfg *OnboardConfig) {
	if fqdn == "" {
		fqdn = cfg.URL
	}
	if username == "" {
		username = cfg.Username
	}
	if password == "" {
		password = cfg.Password
	}
	if clientToken == "" {
		clientToken = cfg.ClientToken
	}
	if domain == "default" && cfg.Domain != "" {
		domain = cfg.Domain
	}
	if tenant == "service" && cfg.Tenant != "" {
		tenant = cfg.Tenant
	}
	if verbosity == "minimal" && cfg.Verbosity != "" {
		verbosity = cfg.Verbosity
	}
	if regionName == "" {
		regionName = cfg.Region
	}
}

func runOnboard(cmd *cobra.Command, args []string) {
	// If config file is provided, load it and use values as defaults for unset flags
	if configFile != "" {
		cfg, err := LoadOnboardConfig(configFile)
		if err != nil {
			fmt.Printf("Error loading config file: %v\n", err)
			os.Exit(1)
		}
		mergeConfigWithFlags(cfg)
	}

	missing := []string{}
	if fqdn == "" {
		missing = append(missing, "--url")
	}
	if username == "" {
		missing = append(missing, "--username")
	}
	if clientToken == "" {
		missing = append(missing, "--client-token")
	}
	if regionName == "" {
		missing = append(missing, "--region")
	}
	if len(missing) > 0 {
		fmt.Printf("Error: missing required flags: %s\n", strings.Join(missing, ", "))
		os.Exit(1)
	}

	utils.LogDebug("Final onboarding values: url=%s, username=%s, domain=%s, tenant=%s, region=%s, verbosity=%s",
		fqdn, username, domain, tenant, regionName, verbosity)

	// Step 8: (Unit tests for config/flag precedence should be added/updated in onboard_test.go)

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

	// Get authentication token
	utils.LogDebug("Getting authentication token for user %s", username)
	authClient := client.NewAuthClient(fqdn, clientToken)
	token, err := authClient.GetToken(username, password)
	if err != nil {
		utils.LogError("Failed to get authentication token: %v", err)
		os.Exit(1)
	}

	// Create Kubernetes client
	k8sClient := client.NewK8sClient(fqdn, domain, tenant, token, regionName)

	// Prepare directories
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

	// Save kubeconfig
	utils.LogInfo("Saving kubeconfig from bootstrap secret")
	if err := k8sClient.SaveKubeConfig("byoh-bootstrap-kc"); err != nil {
		utils.LogError("Failed to save kubeconfig: %v", err)
		os.Exit(1)
	}

	// Check if region where user wants to onboard to is available for this tenant or not
	// If not available, roll back the onboarding process
	available, regions, err := k8sClient.CheckRegionAvailability(regionName)
	if err != nil {
		utils.LogError("Failed to check region availability, rolling back onboarding process: %v", err)
		if err := k8sClient.DeleteSavedKubeconfig(); err != nil {
			utils.LogError("Failed to delete saved kubeconfig while rolling back onboarding process: %v", err)
		}
		os.Exit(1)
	}
	if !available {
		utils.LogError("Region %s is not available for the tenant, rolling back onboarding process", regionName)
		if len(regions) > 0 {
			utils.LogInfo("Available regions: %v", regions)
		}
		if err := k8sClient.DeleteSavedKubeconfig(); err != nil {
			utils.LogError("Failed to delete saved kubeconfig while rolling back onboarding process: %v", err)
		}
		os.Exit(1)
	}

	// Save region name in a temp file in byohDir
	/*
		Agent deb will read this file in a agent-after-install script, export the region label variable,
		then it will be passed as a label flag to the pf9-byohost-agent binary.
		This file will be removed as a part of agent-before-remove script.
	*/
	regionFile := filepath.Join(byohDir, "region")
	regionLabel := service.PcdKaapiRegionKey + "=" + regionName
	if err := os.WriteFile(regionFile, []byte(regionLabel), service.DefaultFilePerms); err != nil {
		utils.LogError("Failed to save region name: %v", err)
		os.Exit(1)
	}

	// Create packages directory for downloads
	pkgDir := filepath.Join(byohDir, "packages")
	if err := os.MkdirAll(pkgDir, service.DefaultDirPerms); err != nil {
		utils.LogError("Failed to create packages directory: %v", err)
		os.Exit(1)
	}

	// Setup agent (download and install)
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
