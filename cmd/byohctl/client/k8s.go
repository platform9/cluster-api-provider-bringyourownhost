package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

type K8sClient struct {
	client      *http.Client
	fqdn        string
	domain      string
	tenant      string
	bearerToken string
}

func NewK8sClient(fqdn, domain, tenant, token string) *K8sClient {
	return &K8sClient{
		client:      &http.Client{Timeout: 30 * time.Second},
		fqdn:        fqdn,
		domain:      domain,
		tenant:      tenant,
		bearerToken: token,
	}
}

func (c *K8sClient) getNamespace() string {
	fqdnPrefix := strings.Split(c.fqdn, ".")[0]
	return fmt.Sprintf("%s-%s-%s", fqdnPrefix, c.domain, c.tenant)
}

func (c *K8sClient) GetSecret(secretName string) (*types.Secret, error) {
	utils.LogInfo("Fetching secret '%s' from namespace '%s'", secretName, c.getNamespace())

	namespace := c.getNamespace()
	fqdnPrefix := strings.Split(c.fqdn, ".")[0]
	clusterName := fmt.Sprintf("%s-%s-%s", fqdnPrefix, c.domain, c.tenant)

	secretEndpoint := fmt.Sprintf("https://%s/oidc-proxy/%s/api/v1/namespaces/%s/secrets/%s",
		c.fqdn, clusterName, namespace, secretName)

	req, err := http.NewRequest("GET", secretEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Add("Authorization", "Bearer "+c.bearerToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error getting secret: %s", string(body))
	}

	var secret types.Secret
	if err := json.Unmarshal(body, &secret); err != nil {
		return nil, fmt.Errorf("error parsing secret: %v", err)
	}

	utils.LogSuccess("Successfully retrieved secret")
	return &secret, nil
}

func (c *K8sClient) SaveKubeConfig(secretName string) error {
	start := time.Now()
	defer utils.TrackTime(start, "Saving kubeconfig")

	utils.LogInfo("Getting secret containing kubeconfig")
	secret, err := c.GetSecret(secretName)
	if err != nil {
		return fmt.Errorf("failed to get secret: %v", err)
	}

	encodedConfig, ok := secret.Data["config"]
	if !ok {
		return fmt.Errorf("no config found in secret")
	}

	utils.LogInfo("Decoding kubeconfig content")
	decodedConfig, err := base64.StdEncoding.DecodeString(encodedConfig)
	if err != nil {
		return fmt.Errorf("error decoding config: %v", err)
	}

	tmpPath := "/tmp/pf9"
	utils.LogInfo("Creating directory: %s", tmpPath)
	err = os.MkdirAll(tmpPath, 0755)
	if err != nil {
		return fmt.Errorf("error creating directory: %v", err)
	}

	filePath := filepath.Join(tmpPath, "bootstrap-kubeconfig.yaml")
	utils.LogInfo("Writing kubeconfig to: %s", filePath)
	err = os.WriteFile(filePath, decodedConfig, 0644)
	if err != nil {
		return fmt.Errorf("error writing kubeconfig: %v", err)
	}

	utils.LogSuccess("Successfully saved kubeconfig to %s", filePath)
	return nil
}

func (c *K8sClient) RunByohAgent() error {
	start := time.Now()
	defer utils.TrackTime(start, "BYOH Agent Setup")

	utils.LogInfo("Starting BYOH Agent installation")

	// Verify DNS resolution before proceeding
	utils.LogInfo("Verifying DNS resolution for %s", c.fqdn)
	addrs, err := net.LookupHost(c.fqdn)
	if err != nil {
		utils.LogWarn("DNS resolution check failed: %v", err)
		utils.LogInfo("Adding entry to /etc/hosts might be required")
	} else {
		utils.LogSuccess("DNS resolution successful: %v", addrs)
	}

	utils.LogInfo("Initializing containerd client")
	client, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("insufficient permissions to access containerd. Please run with sudo")
		}
		return fmt.Errorf("failed to initialize containerd client: %v", err)
	}
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), "default")

	// Ensure required directory exists
	byohDir := "/root/.byoh"
	if err := os.MkdirAll(byohDir, 0755); err != nil {
		utils.LogWarn("Failed to create directory %s: %v", byohDir, err)
	}

	image := "quay.io/platform9/pf9-byoh:byoh-agent"
	utils.LogInfo("Pulling image: %s", image)

	pullImage, err := client.Pull(ctx, image, containerd.WithPullUnpack)
	if err != nil {
		utils.LogError("Image pull failed: %v", err)
		return fmt.Errorf("failed to pull image: %v", err)
	}
	utils.LogSuccess("Image pulled successfully")

	utils.LogInfo("Creating container with required mounts")
	container, err := client.NewContainer(
		ctx,
		"byoh-agent",
		containerd.WithImage(pullImage),
		containerd.WithNewSnapshot("byoh-agent-snapshot", pullImage),
		containerd.WithNewSpec(
			oci.WithImageConfig(pullImage),
			oci.WithMounts([]specs.Mount{
				{
					Type:        "bind",
					Source:      "/tmp/pf9/bootstrap-kubeconfig.yaml",
					Destination: "/root/.byoh/config",
					Options:     []string{"rbind", "rw"},
				},
				{
					Type:        "bind",
					Source:      "/etc/resolv.conf",
					Destination: "/etc/resolv.conf",
					Options:     []string{"rbind", "ro"},
				},
				{
					Type:        "bind",
					Source:      "/etc/hosts",
					Destination: "/etc/hosts",
					Options:     []string{"rbind", "ro"},
				},
			}),
			oci.WithProcessCwd("/"),
			// Redirect output to a log file
			oci.WithProcessArgs("/bin/bash", "-c", "./install.sh > /tmp/byoh-install.log 2>&1 && sleep infinity"),
			oci.WithHostNamespace(specs.NetworkNamespace),
		),
	)
	if err != nil {
		utils.LogError("Container creation failed: %v", err)
		return fmt.Errorf("failed to create container: %v", err)
	}
	utils.LogSuccess("Container created successfully")

	utils.LogInfo("Starting agent container")
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		utils.LogError("Task creation failed: %v", err)
		return fmt.Errorf("failed to create task: %v", err)
	}

	if err := task.Start(ctx); err != nil {
		utils.LogError("Task start failed: %v", err)
		return fmt.Errorf("failed to start task: %v", err)
	}

	// Wait for 10 seconds to allow the agent to start
	utils.LogInfo("Waiting for agent to initialize (10s)...")
	time.Sleep(10 * time.Second)

	// Check if the container is still running
	status, err := task.Status(ctx)
	if err != nil {
		utils.LogError("Failed to get task status: %v", err)
	} else {
		utils.LogInfo("Container status: %s", status.Status)
		if status.Status != containerd.Running {
			utils.LogError("Agent container is not running. Status: %s", status.Status)
			return fmt.Errorf("agent container exited prematurely with status: %s", status.Status)
		}
		utils.LogSuccess("Agent container is running")
	}

	utils.LogSuccess("BYOH Agent container started successfully")
	utils.LogInfo("The agent will continue running in the background")
	utils.LogInfo("To check installation logs: sudo ctr tasks exec -t --exec-id check-logs byoh-agent cat /tmp/byoh-install.log")
	utils.LogInfo("To check agent logs: sudo ctr tasks exec -t --exec-id logs byoh-agent cat /var/log/pf9/byoh/byoh-agent.log")
	return nil
}
