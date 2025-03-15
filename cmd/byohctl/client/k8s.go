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
	// DefaultTempDir is the default temporary directory for storing files
	DefaultTempDir = "/tmp/pf9"
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
	
	utils.LogInfo("Fetching secret '%s' from namespace '%s'", secretName, c.getNamespace())

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
	if err := json.Unmarshal(body, &secret); err != nil {
		return nil, utils.LogErrorf("error parsing secret: %v", err)
	}

	utils.LogSuccess("Successfully retrieved secret")
	return &secret, nil
}

// ensureTempDirectory ensures the temp directory exists
func ensureTempDirectory(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		utils.LogInfo("Creating directory: %s", path)
		if err := os.MkdirAll(path, DefaultDirPerms); err != nil {
			return fmt.Errorf("error creating directory: %v", err)
		}
	}
	return nil
}

// SaveKubeConfig retrieves and saves the kubeconfig to a temp location
func (c *K8sClient) SaveKubeConfig(secretName string) error {
	start := time.Now()
	defer utils.TrackTime(start, "Saving kubeconfig")

	// Get secret containing kubeconfig
	utils.LogInfo("Getting secret containing kubeconfig")
	secret, err := c.GetSecret(secretName)
	if err != nil {
		return utils.LogErrorf("failed to get secret: %v", err)
	}

	// Extract and decode kubeconfig data
	encodedConfig, ok := secret.Data["config"]
	if !ok {
		return utils.LogErrorf("no config found in secret")
	}

	utils.LogInfo("Decoding kubeconfig content")
	decodedConfig, err := base64.StdEncoding.DecodeString(encodedConfig)
	if err != nil {
		return utils.LogErrorf("error decoding config: %v", err)
	}

	// Ensure temp directory exists
	if err := ensureTempDirectory(DefaultTempDir); err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Write kubeconfig to temp location
	filePath := filepath.Join(DefaultTempDir, "bootstrap-kubeconfig.yaml")
	utils.LogDebug("Writing kubeconfig to: %s", filePath)
	
	if err := os.WriteFile(filePath, decodedConfig, DefaultFilePerms); err != nil {
		return utils.LogErrorf("error writing kubeconfig: %v", err)
	}

	utils.LogSuccess("Successfully saved kubeconfig to %s", filePath)
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
func (c *K8sClient) RunByohAgent() error {
	start := time.Now()
	defer utils.TrackTime(start, "BYOH Agent Setup")

	utils.LogInfo("Starting BYOH Agent installation")

	// Step 1: Verify DNS resolution before proceeding
	if _, err := c.CheckDNSResolution(); err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 2: Prepare agent directory
	byohDir, err := service.PrepareAgentDirectory()
	if err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 3: Copy kubeconfig
	kubeConfigPath := filepath.Join(DefaultTempDir, "bootstrap-kubeconfig.yaml")
	if err := service.ConfigureAgent(byohDir, kubeConfigPath); err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 4: Install prerequisites
	if err := service.InstallPrerequisites(); err != nil {
		utils.LogWarn("Prerequisite installation had issues: %v", err)
		// Continue anyway as this might not be fatal
	}

	// Step 5: Setup agent (download binary)
	agentBinary, err := service.SetupAgent(byohDir)
	if err != nil {
		return utils.LogErrorf("%v", err)
	}

	// Step 6: Start agent
	namespace := c.getNamespace()
	utils.LogInfo("Using namespace: %s", namespace)
	
	// Try direct method first
	err = service.StartAgent(byohDir, agentBinary, namespace)
	if err != nil {
		// If the direct method fails, try the script-based backup method
		utils.LogWarn("Direct execution failed: %v. Trying script-based method...", err)
		if scriptErr := service.StartAgentWithScript(byohDir, agentBinary, namespace); scriptErr != nil {
			// Both methods failed, provide comprehensive error
			utils.LogDebug("BYOH Agent Setup took %v", time.Since(start))
			return utils.LogErrorf("failed to run BYOH agent using both methods: \n- Direct method: %v\n- Script method: %v", 
				err, scriptErr)
		}
	}

	// Everything succeeded
	utils.LogSuccess("BYOH Agent setup completed successfully in %v", time.Since(start))
	utils.LogInfo("Agent is running in the background with namespace: %s", namespace)
	utils.LogInfo("Check agent logs in: %s/logs/agent.log", byohDir)
	
	return nil
}
