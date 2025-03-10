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

const (
	DefaultContainerdSock = "/run/containerd/containerd.sock"
	DefaultAgentImage     = "quay.io/platform9/pf9-byoh:byoh-agent"
	ByohConfigDir         = "/root/.byoh"
	ByohConfigFile        = "config"
)

type K8sClient struct {
	client         *http.Client
	fqdn           string
	domain         string
	tenant         string
	bearerToken    string
	containerdSock string
	agentImage     string
}

func NewK8sClient(fqdn, domain, tenant, token string, options ...Option) *K8sClient {
	client := &K8sClient{
		client:         &http.Client{Timeout: 30 * time.Second},
		fqdn:           fqdn,
		domain:         domain,
		tenant:         tenant,
		bearerToken:    token,
		containerdSock: DefaultContainerdSock,
		agentImage:     DefaultAgentImage,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

type Option func(*K8sClient)

func WithContainerdSock(path string) Option {
	return func(c *K8sClient) {
		c.containerdSock = path
	}
}

func WithAgentImage(image string) Option {
	return func(c *K8sClient) {
		c.agentImage = image
	}
}

func (c *K8sClient) getNamespace() string {
	fqdnPrefix := strings.Split(c.fqdn, ".")[0]
	return fmt.Sprintf("%s-%s-%s", fqdnPrefix, c.domain, c.tenant)
}

func (c *K8sClient) GetSecret(secretName string) (*types.Secret, error) {
	utils.LogInfo("Fetching secret '%s' from namespace '%s'", secretName, c.getNamespace())

	namespace := c.getNamespace()

	secretEndpoint := fmt.Sprintf("https://%s/oidc-proxy/%s/api/v1/namespaces/%s/secrets/%s",
		c.fqdn, namespace, namespace, secretName)

	req, err := http.NewRequest("GET", secretEndpoint, nil)
	if err != nil {
		return nil, utils.LogErrorf("error creating request: %v", err)
	}

	req.Header.Add("Authorization", "Bearer "+c.bearerToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, utils.LogErrorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, utils.LogErrorf("error reading response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, utils.LogErrorf("error getting secret: %s", string(body))
	}

	var secret types.Secret
	if err := json.Unmarshal(body, &secret); err != nil {
		return nil, utils.LogErrorf("error parsing secret: %v", err)
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
		return utils.LogErrorf("failed to get secret: %v", err)
	}

	encodedConfig, ok := secret.Data["config"]
	if !ok {
		return utils.LogErrorf("no config found in secret")
	}

	utils.LogInfo("Decoding kubeconfig content")
	decodedConfig, err := base64.StdEncoding.DecodeString(encodedConfig)
	if err != nil {
		return utils.LogErrorf("error decoding config: %v", err)
	}

	tmpPath := "/tmp/pf9"
	utils.LogInfo("Creating directory: %s", tmpPath)
	err = os.MkdirAll(tmpPath, 0755)
	if err != nil {
		return utils.LogErrorf("error creating directory: %v", err)
	}

	filePath := filepath.Join(tmpPath, "bootstrap-kubeconfig.yaml")
	utils.LogDebug("Writing kubeconfig to: %s", filePath)
	err = os.WriteFile(filePath, decodedConfig, 0644)
	if err != nil {
		return utils.LogErrorf("error writing kubeconfig: %v", err)
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
		return utils.LogErrorf("DNS resolution failed for %s: %v", c.fqdn, err)
	} else {
		utils.LogSuccess("DNS resolution successful: %v", addrs)
	}

	utils.LogInfo("Initializing containerd client")
	client, err := containerd.New(c.containerdSock)
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return utils.LogErrorf("insufficient permissions to access containerd. Please run with sudo")
		}
		return utils.LogErrorf("failed to initialize containerd client: %v", err)
	}
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), "default")

	// Ensure required directory exists
	if err := os.MkdirAll(ByohConfigDir, 0755); err != nil {
		utils.LogWarn("Failed to create directory %s: %v", ByohConfigDir, err)
	}

	utils.LogDebug("Pulling image: %s", c.agentImage)
	pullImage, err := client.Pull(ctx, c.agentImage, containerd.WithPullUnpack)

	if err != nil {
		utils.LogError("Image pull failed: %v", err)
		return utils.LogErrorf("failed to pull image: %v", err)
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
					Destination: filepath.Join(ByohConfigDir, ByohConfigFile),
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
		return utils.LogErrorf("failed to create container: %v", err)
	}
	utils.LogSuccess("Container created successfully")

	utils.LogInfo("Starting agent container")
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		utils.LogError("Task creation failed: %v", err)
		return utils.LogErrorf("failed to create task: %v", err)
	}

	if err := task.Start(ctx); err != nil {
		utils.LogError("Task start failed: %v", err)
		return utils.LogErrorf("failed to start task: %v", err)
	}

	// Wait for 10 seconds to allow the agent to start
	utils.LogInfo("Waiting for agent to initialize (10s)...")
	time.Sleep(10 * time.Second)

	// Check if the container is still running
	utils.LogDebug("Verifying container status")
	var status containerd.Status
	for attempts := 0; attempts < 5; attempts++ {
		status, err = task.Status(ctx)
		if err == nil {
			break
		}
		utils.LogWarn("Failed to get task status (attempt %d/3): %v", attempts+1, err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		utils.LogError("Failed to get task status: %v", err)
	} else {
		utils.LogDebug("Container status: %s", status.Status)
		if status.Status != containerd.Running {
			utils.LogError("Agent container is not running. Status: %s", status.Status)
			return utils.LogErrorf("agent container exited prematurely with status: %s", status.Status)
		}
		utils.LogSuccess("Agent container is running")
	}

	utils.LogSuccess("BYOH Agent container started successfully")
	utils.LogInfo("The agent will continue running in the background")
	utils.LogInfo("To check installation logs: sudo ctr tasks exec -t --exec-id check-logs byoh-agent cat /tmp/byoh-install.log")
	utils.LogInfo("To check agent logs: sudo ctr tasks exec -t --exec-id logs byoh-agent cat /var/log/pf9/byoh/byoh-agent.log")
	return nil
}
