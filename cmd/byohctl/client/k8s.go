// cmd/byohctl/client/k8s.go
package client

import (
    "context"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io/ioutil"
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
    defer utils.TrackTime(start, "Running BYOH agent")

    utils.LogInfo("Creating containerd client")
    client, err := containerd.New("/run/containerd/containerd.sock")
    if err != nil {
        return fmt.Errorf("failed to create containerd client: %v", err)
    }
    defer client.Close()

    ctx := namespaces.WithNamespace(context.Background(), "default")
    image := "docker.io/snhpf9/byoh-image"

    utils.LogInfo("Pulling image: %s", image)
    pullImage, err := client.Pull(ctx, image, containerd.WithPullUnpack)
    if err != nil {
        return fmt.Errorf("failed to pull image: %v", err)
    }

    utils.LogInfo("Creating container")
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
                    Destination: "/etc/pf9-byohost-agent.service.d/bootstrap-kubeconfig.yaml",
                    Options:     []string{"rbind", "rw"},
                },
            }),
            oci.WithProcessArgs("/install.sh"),
        ),
    )
    if err != nil {
        return fmt.Errorf("failed to create container: %v", err)
    }
    defer container.Delete(ctx, containerd.WithSnapshotCleanup)

    utils.LogInfo("Creating container task")
    task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
    if err != nil {
        return fmt.Errorf("failed to create task: %v", err)
    }
    defer task.Delete(ctx)

    utils.LogInfo("Starting container task")
    if err := task.Start(ctx); err != nil {
        return fmt.Errorf("failed to start task: %v", err)
    }

    utils.LogInfo("Waiting for task completion")
    statusC, err := task.Wait(ctx)
    if err != nil {
        return fmt.Errorf("failed to wait for task: %v", err)
    }

    status := <-statusC
    code, _, err := status.Result()
    if err != nil {
        return fmt.Errorf("failed to get task result: %v", err)
    }

    if code != 0 {
        return fmt.Errorf("task exited with non-zero status code: %d", code)
    }

    utils.LogSuccess("Successfully completed BYOH agent installation")
    return nil
}