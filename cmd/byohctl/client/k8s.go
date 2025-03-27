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
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
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

// Client wraps the Kubernetes clientset and dynamic client.
type Client struct {
	Clientset     *kubernetes.Clientset
	DynamicClient dynamic.Interface
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

// GetNamespaceFromConfig returns the namespace from the kubeconfig
func GetNamespaceFromConfig(kubeconfigPath string) (string, error) {
	// Read the kubeconfig file and get the namespace
	data, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return "", err
	}

	var config service.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("error parsing kubeconfig: %v", err)
	}
	for _, context := range config.Contexts {
		if context.Name == config.CurrentContext {
			return context.Context.Namespace, nil
		}
	}
	return "", fmt.Errorf("namespace not found in kubeconfig")
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

// GetK8sClient returns a new Kubernetes client from given kubeconfig
func GetK8sClient(kubeconfigPath string) (*Client, error) {

	// Build the config from the kubeconfig file.
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %v", err)
	}

	// Create a new Kubernetes client that can be used to interact with Kubernetes resources.
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	// Create a new dynamic client that can be used to interact with custom resources.
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating dynamic client: %v", err)
	}

	return &Client{
		Clientset:     client,
		DynamicClient: dynamicClient,
	}, nil
}

// GetByoHosts gets ByoHost object in the given namespace.
func (client *Client) GetByoHostObject(namespace string) (*infrastructurev1beta1.ByoHost, error) {
	byohostGVR := schema.GroupVersionResource{
		Group:    "infrastructure.cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "byohosts",
	}

	hostName, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("error getting hostname: %v", err)
	}

	// Get the byohost object
	unstructuredObj, err := client.DynamicClient.Resource(byohostGVR).Namespace(namespace).Get(context.Background(), hostName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting ByoHosts: %v", err)
	}

	// Convert the unstructured object to ByoHost
	byoHost := &infrastructurev1beta1.ByoHost{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.UnstructuredContent(), byoHost)
	if err != nil {
		return nil, fmt.Errorf("error converting ByoHosts: %v", err)
	}

	return byoHost, nil
}

// DeleteByoHostObject deletes the ByoHost object in the given namespace.
func (client *Client) DeleteByoHostObject(namespace string) error {
	byohostGVR := schema.GroupVersionResource{
		Group:    "infrastructure.cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "byohosts",
	}

	hostName, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("error getting hostname: %v", err)
	}

	// Delete the byohost object
	err = client.DynamicClient.Resource(byohostGVR).Namespace(namespace).Delete(context.Background(), hostName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}

	return nil
}

// AnnotateMachineObject annotates the machine object with the given annotation
func (client *Client) AnnotateMachineObject(machineObj *unstructured.Unstructured, namespace, annotationKey, annotationValue string) error {
	machineGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machines",
	}

	annotations := machineObj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations[annotationKey] = annotationValue
	machineObj.SetAnnotations(annotations)

	// Update the machine object
	_, err := client.DynamicClient.Resource(machineGVR).Namespace(namespace).Update(context.TODO(), machineObj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating machine object: %v", err)
	}

	return nil
}

// ScaleDownMachineDeployment scales down the machine deployment by 1
func (client *Client) ScaleDownMachineDeployment(machineObj *unstructured.Unstructured, namespace string) error {

	// Get machine deployment name from machine object
	machineDeploymentName := machineObj.GetLabels()["cluster.x-k8s.io/deployment-name"]

	if machineDeploymentName == "" {
		return fmt.Errorf("machine object does not have a machine deployment name as a label.")
	}
	deploymentGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machinedeployments",
	}

	// Get the machine deployment object
	unstructuredDeploymentObj, err := client.DynamicClient.Resource(deploymentGVR).Namespace(namespace).Get(context.TODO(), machineDeploymentName, metav1.GetOptions{})
	if err != nil {
		fmt.Errorf("error getting machine deployment object: %v", err)
		return err
	}
	machineDeploymentObj := &capiv1beta1.MachineDeployment{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredDeploymentObj.UnstructuredContent(), machineDeploymentObj)
	if err != nil {
		return fmt.Errorf("error converting machine deployment object: %v", err)
	}

	*machineDeploymentObj.Spec.Replicas = *machineDeploymentObj.Spec.Replicas - 1

	updatedUnstructuredDeploymentObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(machineDeploymentObj)
	if err != nil {
		return fmt.Errorf("error converting machine deployment object: %v", err)
	}

	updatedUnstructured := &unstructured.Unstructured{
		Object: updatedUnstructuredDeploymentObj,
	}

	// Update the machine deployment object
	_, err = client.DynamicClient.Resource(deploymentGVR).Namespace(namespace).Update(context.TODO(), updatedUnstructured, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating machine deployment object: %v", err)
	}

	return nil
}

// GetMachineObject returns the machine object
func (client *Client) GetUnstructuredMachineObject(namespace, machineName string) (*unstructured.Unstructured, error) {
	machineGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machines",
	}

	// Get the machine object
	unstructuredMachineObj, err := client.DynamicClient.Resource(machineGVR).Namespace(namespace).Get(context.TODO(), machineName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting machine object: %v", err)
	}

	return unstructuredMachineObj, nil
}

// GetMachineDeploymentReplicaCount returns the replica count of the machine deployment
func (client *Client) GetMachineDeploymentReplicaCount(machineObj *unstructured.Unstructured, namespace string) (int32, error) {

	// Get machine deployment name from machine object
	machineDeploymentName := machineObj.GetLabels()["cluster.x-k8s.io/deployment-name"]

	if machineDeploymentName == "" {
		return 0, fmt.Errorf("machine object does not have a machine deployment name as a label.")
	}
	deploymentGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machinedeployments",
	}

	// Get the machine deployment object
	unstructuredDeploymentObj, err := client.DynamicClient.Resource(deploymentGVR).Namespace(namespace).Get(context.TODO(), machineDeploymentName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("error getting machine deployment object: %v", err)
	}
	machineDeploymentObj := &capiv1beta1.MachineDeployment{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredDeploymentObj.UnstructuredContent(), machineDeploymentObj)
	if err != nil {
		return 0, fmt.Errorf("error converting machine deployment object: %v", err)
	}

	return *machineDeploymentObj.Spec.Replicas, nil
}

// WaitForMachineRefToBeUnset waits for the machineRef to be unset from the byohost object status field
func (client *Client) WaitForMachineRefToBeUnset(byoHost *infrastructurev1beta1.ByoHost, namespace string) error {
	startTime := time.Now()

	for {
		// Check if we've exceeded the timeout
		if time.Since(startTime) > service.WaitForMachineRefToBeUnsetTimeout {
			return fmt.Errorf("timeout waiting for machineRef to be unset")
		}

		// Get the current byohost object
		byoHost, err := client.GetByoHostObject(namespace)
		if err != nil {
			return fmt.Errorf("error getting byohost object: %v", err)
		}

		// Check if machineRef is nil or no longer references the machine
		if byoHost.Status.MachineRef == nil {
			utils.LogSuccess("MachineRef successfully unset")
			return nil
		}

		// Wait a bit before checking again
		utils.LogInfo("Waiting for machineRef to be unset...")
		time.Sleep(5 * time.Second)
	}
}
