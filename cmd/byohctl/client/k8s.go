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
	tenant := strings.ReplaceAll(c.tenant, "_", "-")
	return fmt.Sprintf("%s-%s-%s", fqdnPrefix, c.domain, tenant)
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

// SaveKubeConfig saves the kubeconfig from the secret to the user's BYOH directory
func (c *K8sClient) SaveKubeConfig(secretName string) error {
	// Step 1: Get secret
	secret, err := c.GetSecret(secretName)
	if err != nil {
		return fmt.Errorf("failed to get secret: %v", err)
	}

	// Step 2: Get kubeconfig from secret
	kubeconfigString, ok := secret.Data["config"]
	if !ok {
		return fmt.Errorf("kubeconfig not found in secret")
	}

	// Step 3: Decode kubeconfig
	kubeconfig, err := base64.StdEncoding.DecodeString(string(kubeconfigString))
	if err != nil {
		return fmt.Errorf("failed to decode kubeconfig: %v", err)
	}

	// Step 4: Create byohDir if it doesn't exist
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %v", err)
	}

	byohDir := filepath.Join(homeDir, service.ByohConfigDir)

	// Step 5: Write kubeconfig to byohDir
	kubeconfigPath := filepath.Join(byohDir, "config")

	// Write kubeconfig to byohDir
	if err = os.WriteFile(kubeconfigPath, kubeconfig, service.DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %v", err)
	}

	// Success
	utils.LogSuccess("Successfully wrote kubeconfig to %s", kubeconfigPath)
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
