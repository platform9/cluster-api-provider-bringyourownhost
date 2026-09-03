// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// cmd/byohctl/cmd/onboard.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v2"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

var (
	username                string
	password                string
	passwordInteractive     bool
	fqdn                    string
	domain                  string
	tenant                  string
	clientToken             string
	verbosity               string
	regionName              string
	configFile              string
	bootstrapKubeconfigPath string
	hostNamespace           string
)

var bootstrapAgentConfDir = "/etc/pf9-byohost-agent.service.d"

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
		&bootstrapKubeconfigPath, &hostNamespace,
	)
	rootCmd.AddCommand(onboardCmd)
}

// AddOnboardFlags adds all flags for the onboard command to the given cobra.Command.
func AddOnboardFlags(cmd *cobra.Command,
	fqdn *string, username *string, password *string, passwordInteractive *bool,
	clientToken *string, domain *string, tenant *string, verbosity *string, regionName *string, configFile *string,
	bootstrapKubeconfigPath *string, hostNamespace *string,
) {
	cmd.Flags().StringVarP(fqdn, "url", "u", "", "Platform9 FQDN")
	cmd.Flags().StringVarP(username, "username", "e", "", "Platform9 username")
	cmd.Flags().StringVarP(password, "password", "p", "", "Platform9 password")
	cmd.Flags().BoolVar(passwordInteractive, "password-interactive", false, "Enter password interactively")
	cmd.Flags().StringVarP(clientToken, "client-token", "c", "", "Client token for authentication")
	cmd.Flags().StringVarP(domain, "domain", "d", "default", "Platform9 domain")
	cmd.Flags().StringVarP(tenant, "tenant", "t", "service", "Platform9 tenant")
	cmd.Flags().StringVarP(verbosity, "verbosity", "v", "minimal", "Log verbosity level. Requires one of: all, important, minimal, critical.\nOmitting the flag will show minimal verbosity")
	cmd.MarkFlagsMutuallyExclusive("password", "password-interactive")
	cmd.Flags().StringVarP(regionName, "region", "r", "", "Platform9 region where you want to onboard this host")
	cmd.Flags().StringVarP(configFile, "config", "f", "", "Path to onboarding config YAML file")
	cmd.Flags().StringVar(bootstrapKubeconfigPath, "bootstrap-kubeconfig", "", "Path to a bootstrap kubeconfig")
	cmd.Flags().StringVar(hostNamespace, "namespace", "default", "Namespace to register this host in (only used with --bootstrap-kubeconfig)")
	cmd.MarkFlagsMutuallyExclusive("bootstrap-kubeconfig", "username")
	cmd.MarkFlagsMutuallyExclusive("bootstrap-kubeconfig", "client-token")
	cmd.MarkFlagsMutuallyExclusive("bootstrap-kubeconfig", "url")

	// Hidden until the CSR approver validates requester identity against the requested CN and the
	// ByoHost ownership webhook is re-enabled (currently disabled, see cd421b5) -- until then a
	// bootstrap token's holder isn't actually scoped to onboarding just their own host. Functional,
	// just not advertised: --help/usage won't show these, but the flags still work if invoked.
	_ = cmd.Flags().MarkHidden("bootstrap-kubeconfig")
	_ = cmd.Flags().MarkHidden("namespace")
}

// osReadFile is a variable so tests can replace it with a mock, same pattern as execCommand in
// cmd/byohctl/service/agent.go.
var osReadFile = os.ReadFile

// supportedUbuntuVersions lists the Ubuntu VERSION_ID values a host may run. It is dictated by
// which byoh-bundles actually get published: keep it in sync with the UBUNTU_VERSION case arms in
// .ci/build-push-bundle.sh, which TestSupportedUbuntuVersionsMatchBundleScript enforces.
// Onboarding a host outside this set succeeds but leaves it unable to install Kubernetes, so the
// failure only surfaces much later, during cluster provisioning.
var supportedUbuntuVersions = []string{"20.04", "22.04", "24.04"}

// osReleasePaths are searched in order. /usr/lib/os-release is the fallback for stateless
// systems, matching agent/registration/host_registrar.go's getOperatingSystem.
var osReleasePaths = []string{"/etc/os-release", "/usr/lib/os-release"}

// UnsupportedOSError reports that the host's OS was identified but is not one the agent supports.
// Typed so callers and tests can match it with errors.As instead of matching on message text.
type UnsupportedOSError struct {
	// Detected is how the host describes itself, e.g. "Ubuntu 18.04.6 LTS".
	Detected string
}

func (e *UnsupportedOSError) Error() string {
	return fmt.Sprintf("%s is not supported for onboarding; supported versions are Ubuntu %s",
		e.Detected, strings.Join(supportedUbuntuVersions, ", "))
}

// OSDetectionError reports that the host's OS could not be identified at all: os-release is
// missing, unreadable, or lacks the fields the check needs. Distinct from UnsupportedOSError
// because the host may well be supportable -- we just can't tell.
type OSDetectionError struct {
	// Reason names the part of the identification that failed.
	Reason string
}

func (e *OSDetectionError) Error() string {
	return fmt.Sprintf("could not identify this host's OS: %s", e.Reason)
}

// checkSupportedPlatform reports why this host cannot run the BYOH agent, or nil if it can.
// Errors are *UnsupportedOSError or *OSDetectionError.
//
// There is deliberately no architecture check: byohctl is built for both amd64 and arm64, and
// gating on arch would block running the tooling on an arm64 machine for no gain here.
func checkSupportedPlatform() error {
	data, err := readOSRelease()
	if err != nil {
		return err
	}
	osRelease := parseOSRelease(data)

	// Report the distro the way the host describes itself; ID alone ("pop") is unhelpful.
	described := osRelease["PRETTY_NAME"]
	if described == "" {
		described = osRelease["ID"]
	}
	if described == "" {
		described = "unknown"
	}

	// ID, not ID_LIKE: Ubuntu derivatives are not what the bundles are built and tested against.
	if osRelease["ID"] != "ubuntu" {
		return &UnsupportedOSError{Detected: described}
	}

	version := osRelease["VERSION_ID"]
	if version == "" {
		return &OSDetectionError{Reason: "no VERSION_ID in " + strings.Join(osReleasePaths, " or ")}
	}
	// Ubuntu point releases keep the series in VERSION_ID (22.04.5 LTS reports "22.04"), so an
	// exact match is right here.
	if !slices.Contains(supportedUbuntuVersions, version) {
		return &UnsupportedOSError{Detected: described}
	}

	return nil
}

// readOSRelease returns the contents of the first readable path in osReleasePaths. On a non-Linux
// host (byohctl also builds for macOS) no such file exists, so this is what rejects it.
func readOSRelease() ([]byte, error) {
	for _, path := range osReleasePaths {
		data, err := osReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	return nil, &OSDetectionError{Reason: "no readable " + strings.Join(osReleasePaths, " or ")}
}

// parseOSRelease turns os-release contents into its KEY=VALUE pairs, unquoting values.
// Split out from checkSupportedPlatform so the parsing is unit-testable on its own, same as
// parseNTPSynchronized below.
func parseOSRelease(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values
}

// runCommandWithStdout is a variable so tests can replace it with a mock,
// same pattern as execCommand in cmd/byohctl/service/agent.go.
var runCommandWithStdout = service.RunWithStdout

// isNTPSynchronized is best-effort -- the caller only warns, never blocks.
func isNTPSynchronized() bool {
	out, err := runCommandWithStdout("timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err != nil {
		return false
	}
	return parseNTPSynchronized(out)
}

// parseNTPSynchronized interprets the output of
// `timedatectl show -p NTPSynchronized --value`, which prints exactly "yes"
// or "no". Split out from isNTPSynchronized so this logic is unit-testable
// without mocking exec.Command across the cmd/service package boundary.
func parseNTPSynchronized(output string) bool {
	return strings.TrimSpace(output) == "yes"
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
	Insecure    bool   `yaml:"insecure"`
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

// bootstrapKubeconfigDestPath is where the agent's systemd drop-in points --bootstrap-kubeconfig.
// Both ways byohctl can produce that file -- an operator-supplied file via --bootstrap-kubeconfig,
// or a credential Secret from a ByoHostEnrollment this run created -- write to this same path, so
// the agent does not need to know which one produced its bootstrap credential.
func bootstrapKubeconfigDestPath() string {
	return filepath.Join(bootstrapAgentConfDir, "bootstrap-kubeconfig.yaml")
}

// writeBootstrapKubeconfigFile writes the raw kubeconfig bytes the agent's --bootstrap-kubeconfig
// flag points at. This is always the last write onboarding does to it: the agent waits for this
// file to exist before doing anything else (see waitForBootstrapCredential in agent/main.go), so
// writing it is what lets the agent proceed.
func writeBootstrapKubeconfigFile(kubeconfig []byte) error {
	if err := os.MkdirAll(bootstrapAgentConfDir, service.DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create %s: %w", bootstrapAgentConfDir, err)
	}
	destPath := bootstrapKubeconfigDestPath()
	if err := os.WriteFile(destPath, kubeconfig, 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", destPath, err)
	}
	return nil
}

// writeNamespaceFile records the tenant namespace the agent should register into, in the file the
// agent reads once at startup (see resolveNamespace in agent/main.go).
func writeNamespaceFile(byohDir, namespace string) error {
	namespaceFile := filepath.Join(byohDir, "namespace")
	if err := os.WriteFile(namespaceFile, []byte(namespace), service.DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to save namespace: %w", err)
	}
	return nil
}

// Never writes ~/.byoh/config thus agent's own bootstrap-token-to-certificate exchange
// runs on first start
func writeBootstrapCredential(byohDir, bootstrapKubeconfigPath, namespace string) error {
	data, err := os.ReadFile(bootstrapKubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to read bootstrap kubeconfig %s: %w", bootstrapKubeconfigPath, err)
	}
	// The agent copies this stanza's TLS settings into the kubeconfig it generates after the
	// bootstrap-token-to-certificate exchange (agent/registration/csr.go), so recording
	// --insecure here is what makes it stick for the life of the host.
	if insecure {
		data, err = client.MakeKubeconfigInsecure(data)
		if err != nil {
			return fmt.Errorf("failed to apply --insecure to bootstrap kubeconfig: %w", err)
		}
	}
	if err := writeBootstrapKubeconfigFile(data); err != nil {
		return err
	}
	return writeNamespaceFile(byohDir, namespace)
}

// authenticateWithPlatform9 either hands the operator-supplied bootstrap credential to the agent
// (the --bootstrap-kubeconfig escape hatch) or authenticates with Platform9, saves the resulting
// kubeconfig, and confirms regionName is available for the tenant. It returns the client used to
// authenticate, or nil when the escape hatch was used, since there was nothing to authenticate.
func authenticateWithPlatform9(ctx context.Context, byohDir string, usingBootstrapKubeconfig bool) (*client.K8sClient, error) {
	if usingBootstrapKubeconfig {
		utils.LogDebug("Using bootstrap kubeconfig %s, namespace %s", bootstrapKubeconfigPath, hostNamespace)
		utils.LogInfo("Handing bootstrap credential to the agent")
		if err := writeBootstrapCredential(byohDir, bootstrapKubeconfigPath, hostNamespace); err != nil {
			return nil, fmt.Errorf("failed to write bootstrap credential: %w", err)
		}
		return nil, nil
	}

	utils.LogDebug("Using FQDN: %s, Domain: %s, Tenant: %s", fqdn, domain, tenant)

	utils.LogDebug("Getting authentication token for user %s", username)
	authClient := client.NewAuthClient(fqdn, clientToken, insecure)
	token, err := authClient.GetToken(username, password)
	if err != nil {
		return nil, fmt.Errorf("failed to get authentication token: %w", err)
	}

	k8sClient := client.NewK8sClient(fqdn, domain, tenant, token, regionName, insecure)

	utils.LogInfo("Saving kubeconfig from bootstrap secret")
	if err := k8sClient.SaveKubeConfig("byoh-bootstrap-kc"); err != nil {
		return nil, fmt.Errorf("failed to save kubeconfig: %w", err)
	}

	// Check if region where user wants to onboard to is available for this tenant or not.
	// If not available, roll back the onboarding process.
	available, regions, err := k8sClient.CheckRegionAvailability(ctx, regionName)
	if err != nil {
		if delErr := k8sClient.DeleteSavedKubeconfig(); delErr != nil {
			utils.LogError("Failed to delete saved kubeconfig while rolling back onboarding process: %v", delErr)
		}
		return nil, fmt.Errorf("failed to check region availability, rolling back onboarding process: %w", err)
	}
	if !available {
		if len(regions) > 0 {
			utils.LogInfo("Available regions: %v", regions)
		}
		if delErr := k8sClient.DeleteSavedKubeconfig(); delErr != nil {
			utils.LogError("Failed to delete saved kubeconfig while rolling back onboarding process: %v", delErr)
		}
		return nil, fmt.Errorf("region %s is not available for the tenant, rolling back onboarding process", regionName)
	}

	return k8sClient, nil
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
	// Boolean, so there is no "unset" sentinel to compare against: the flag can only turn
	// this on, never off, matching how the string flags above take precedence.
	if cfg.Insecure {
		insecure = true
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

	usingBootstrapKubeconfig := bootstrapKubeconfigPath != ""

	missing := []string{}
	if !usingBootstrapKubeconfig {
		if fqdn == "" {
			missing = append(missing, "--url (or config file 'url")
		}
		if username == "" {
			missing = append(missing, "--username (or config file 'username')")
		}
		if clientToken == "" {
			missing = append(missing, "--client-token (or config file 'client-token')")
		}
	}
	if regionName == "" {
		missing = append(missing, "--region (or config file 'region')")
	}
	if len(missing) > 0 {
		fmt.Printf("Error: missing required flags: %s\n", strings.Join(missing, ", "))
		os.Exit(1)
	}

	utils.LogDebug("Final onboarding values: url=%s, username=%s, domain=%s, tenant=%s, region=%s, verbosity=%s",
		fqdn, username, domain, tenant, regionName, verbosity)

	// Gate on OS and architecture before touching the host or contacting Platform9, so an
	// unsupported host exits having changed nothing.
	if err := checkSupportedPlatform(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Computed early, alongside the other host-qualification checks above: a host name that
	// cannot normalize is a hard failure, and failing before Platform9 is contacted leaves
	// nothing to roll back.
	hostName, err := computeHostName()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Advisory only -- not required for correctness, see isNTPSynchronized.
	if !isNTPSynchronized() {
		fmt.Println("Warning: this host's clock does not appear to be NTP-synchronized.")
		fmt.Println("         This won't block onboarding, but an out-of-sync clock can cause")
		fmt.Println("         confusing log timestamps and TLS certificate validity errors.")
		fmt.Println("         Consider enabling time sync, e.g.: sudo timedatectl set-ntp true")
	}

	// Continue with interactive password if needed
	if !usingBootstrapKubeconfig && passwordInteractive {
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
	utils.LogDebug("Verbosity level set to: %s", verbosity)

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

	// k8sClient is reused below by the enrollment step after the agent is installed, rather
	// than re-authenticating with Platform9; it is nil when the --bootstrap-kubeconfig escape
	// hatch was used, since there was nothing to authenticate.
	k8sClient, err := authenticateWithPlatform9(cmd.Context(), byohDir, usingBootstrapKubeconfig)
	if err != nil {
		utils.LogError("%v", err)
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

	// Save this host's normalized name for the agent to read at startup. Both this file and
	// the region file above must exist before the agent package is installed below: the
	// package's after-install script reads the region file at install time, and the agent
	// reads the hostname file on every start, including a restart of an already-onboarded host.
	hostNameFile := filepath.Join(byohDir, hostname.FileName)
	if err := os.WriteFile(hostNameFile, []byte(hostName), service.DefaultFilePerms); err != nil {
		utils.LogError("Failed to save host name: %v", err)
		os.Exit(1)
	}

	// Create packages directory for downloads
	pkgDir := filepath.Join(byohDir, "packages")
	if err := os.MkdirAll(pkgDir, service.DefaultDirPerms); err != nil {
		utils.LogError("Failed to create packages directory: %v", err)
		os.Exit(1)
	}

	// Setup agent (download and install), then -- unless the --bootstrap-kubeconfig escape
	// hatch is in play -- create the host's enrollment and wait for its credential. See
	// installAndEnroll for why installing has to come first.
	utils.LogInfo("Setting up BYOH agent")
	if err := installAndEnroll(cmd.Context(), pkgDir, byohDir, hostName, regionName, usingBootstrapKubeconfig, k8sClient); err != nil {
		utils.LogError("%v", err)
		os.Exit(1)
	}

	utils.LogSuccess("Successfully onboarded the host")

	timeElapsed := time.Since(start)
	utils.LogDebug("Time elapsed: %s", timeElapsed)

	utils.LogSuccess("BYOH Agent Service logs are available at:")
	utils.LogSuccess("   - Agent service logs: %s", service.ByohAgentLogPath)
	utils.LogSuccess("   - Check service status: sudo systemctl status pf9-byohost-agent.service")
}
