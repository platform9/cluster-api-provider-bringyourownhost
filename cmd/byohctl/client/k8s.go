package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

const (
	// DefaultTimeout is the default timeout for HTTP requests
	DefaultTimeout = 30 * time.Second
	// DefaultFilePerms is the default file permissions
	DefaultFilePerms = 0644
	// DefaultDirPerms is the default directory permissions
	DefaultDirPerms = 0755
)

// K8sClient handles Kubernetes API operations
type K8sClient struct {
	client      *http.Client
	fqdn        string
	domain      string
	tenant      string
	bearerToken string
}

// NewK8sClient creates a new Kubernetes client with provided credentials
func NewK8sClient(fqdn, domain, tenant, token string) *K8sClient {
	client := &K8sClient{
		client:      &http.Client{Timeout: DefaultTimeout},
		fqdn:        fqdn,
		domain:      domain,
		tenant:      tenant,
		bearerToken: token,
	}
	return client
}

// getNamespace returns the namespace for the client
func (c *K8sClient) getNamespace() string {
	fqdnPrefix := strings.Split(c.fqdn, ".")[0]
	return fmt.Sprintf("%s-%s-%s", fqdnPrefix, c.domain, c.tenant)
}

// GetSecret retrieves a secret from the Kubernetes API
func (c *K8sClient) GetSecret(secretName string) (*types.Secret, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	utils.LogInfo("Fetching secret '%s'", secretName)

	namespace := c.getNamespace()
	secretEndpoint := fmt.Sprintf("https://%s/oidc-proxy/%s/api/v1/namespaces/%s/secrets/%s",
		c.fqdn, namespace, namespace, secretName)

	req, err := http.NewRequestWithContext(ctx, "GET", secretEndpoint, nil)
	if err != nil {
		return nil, utils.LogErrorf("error creating request: %v", err)
	}

	req.Header.Add("Authorization", "Bearer "+c.bearerToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, utils.LogErrorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, utils.LogErrorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, utils.LogErrorf("error getting secret (status %d): %s", resp.StatusCode, string(body))
	}

	var secret types.Secret
	err = json.Unmarshal(body, &secret)
	if err != nil {
		return nil, utils.LogErrorf("error parsing secret: %v", err)
	}

	utils.LogSuccess("Successfully retrieved secret")
	return &secret, nil
}

// SaveKubeConfig saves the kubeconfig from the secret to disk
func (c *K8sClient) SaveKubeConfig(secretName string) error {
	// Step 1: Get secret
	secret, err := c.GetSecret(secretName)
	if err != nil {
		return fmt.Errorf("failed to get secret: %v", err)
	}
	utils.LogSuccess("Successfully retrieved secret")

	// Step 2: Get kubeconfig from secret
	kubeconfigString, ok := secret.Data["config"]
	if !ok {
		return fmt.Errorf("kubeconfig not found in secret (missing 'config' key)")
	}

	// Step 3: Decode the base64 encoded kubeconfig
	decodedKubeconfig, err := base64.StdEncoding.DecodeString(kubeconfigString)
	if err != nil {
		return fmt.Errorf("failed to decode kubeconfig: %v", err)
	}

	// Step 4: Write kubeconfig to disk
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %v", err)
	}

	// Create directory if it doesn't exist
	byohDir := filepath.Join(homeDir, service.ByohConfigDir)
	if err := os.MkdirAll(byohDir, service.DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	// Write decoded kubeconfig
	kubeconfigPath := filepath.Join(byohDir, "config")
	if err := os.WriteFile(kubeconfigPath, decodedKubeconfig, service.DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %v", err)
	}

	utils.LogSuccess("Successfully saved kubeconfig to %s", kubeconfigPath)
	return nil
}

// CheckDNSResolution verifies that DNS resolution works for the FQDN
func (c *K8sClient) CheckDNSResolution() ([]string, error) {
	utils.LogInfo("Verifying DNS resolution for %s", c.fqdn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var r net.Resolver
	addrs, err := r.LookupHost(ctx, c.fqdn)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed for %s: %v", c.fqdn, err)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("DNS resolution returned empty result for %s", c.fqdn)
	}

	utils.LogSuccess("DNS resolution successful: %v", addrs)
	return addrs, nil
}

// RunByohAgent sets up and starts the BYOH agent
func (c *K8sClient) RunByohAgent(secretName string) error {
	start := time.Now()
	defer utils.TrackTime(start, "BYOH Agent Setup")

	utils.LogInfo("Starting BYOH Agent installation")

	// Step 1: Verify DNS resolution before proceeding
	if _, err := c.CheckDNSResolution(); err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 2: Prepare directory structure
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return utils.LogErrorf("failed to get home directory: %v", err)
	}

	byohDir := filepath.Join(homeDir, service.ByohConfigDir)
	if err := service.PrepareAgentDirectory(byohDir); err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 3: Save kubeconfig - only do this once
	if err := c.SaveKubeConfig(secretName); err != nil {
		return utils.LogErrorf("failed to save kubeconfig: %v", err)
	}

	// Step 4: Configure agent with the kubeconfig
	kubeConfigPath := filepath.Join(byohDir, "config")
	kubeConfigData, err := os.ReadFile(kubeConfigPath)
	if err != nil {
		return utils.LogErrorf("failed to read kubeconfig at %s: %v", kubeConfigPath, err)
	}

	namespace := c.getNamespace()
	if err := service.ConfigureAgent(namespace, string(kubeConfigData)); err != nil {
		return utils.LogErrorf("failed to configure agent: %v", err)
	}

	// Create a packages directory for downloads
	pkgDir := filepath.Join(byohDir, "packages")
	if err := os.MkdirAll(pkgDir, service.DefaultDirPerms); err != nil {
		utils.LogWarn("Failed to create packages directory: %v", err)
		// Fall back to using byohDir if packages dir creation fails
		pkgDir = byohDir
	}

	// Step 5: Setup agent (download and install) - SetupAgent already calls ensureRequiredPackages
	_, err = service.SetupAgent(pkgDir)
	if err != nil {
		utils.LogError("Failed to setup agent: %v", err)
		
		// Run diagnostics when the agent fails
		utils.LogInfo("Running diagnostics to identify issues...")
		if diagErr := service.DiagnoseAgent(); diagErr != nil {
			utils.LogError("Diagnostics also encountered errors: %v", diagErr)
		}
		
		return fmt.Errorf("failed to setup agent: %v", err)
	}

	// Step 6: Ensure log directory exists
	logDir := filepath.Dir(service.ByohAgentLogPath)
	if err := os.MkdirAll(logDir, service.DefaultDirPerms); err != nil {
		utils.LogWarn("Failed to create log directory: %v", err)
	}

	// Success message with concise, helpful information
	utils.LogSuccess("BYOH Agent setup completed successfully in %v", time.Since(start))
	utils.LogInfo("Agent logs are available at: %s", service.ByohAgentLogPath)
	utils.LogInfo("Check agent status with: systemctl status %s", service.ByohAgentServiceName)

	return nil
}
