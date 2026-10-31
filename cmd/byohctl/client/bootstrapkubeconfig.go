// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// bootstrapKubeconfigAPIVersion is the API group and version of BootstrapKubeconfig.
	bootstrapKubeconfigAPIVersion = "infrastructure.cluster.x-k8s.io/v1beta1"
	// credentialKubeconfigKey is the data key of the credential Secret that holds the kubeconfig.
	credentialKubeconfigKey = "kubeconfig"
)

// StatusError is returned when the oidc-proxy answers a request with an unexpected status.
type StatusError struct {
	Action     string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *StatusError) Error() string {
	return fmt.Sprintf("failed to %s (status %d): %s", e.Action, e.StatusCode, e.Body)
}

// errNamespaceNotResolved is returned when a call needs the tenant ref before ResolveTenantNamespace ran.
var errNamespaceNotResolved = errors.New("tenant namespace is not resolved, call ResolveTenantNamespace first")

// resolvedRef returns the oidc-proxy key and tenant namespace found by ResolveTenantNamespace.
func (c *K8sClient) resolvedRef() tenantRef {
	return tenantRef{key: c.key, namespace: c.namespace}
}

// tenantRequest sends one request through the oidc-proxy at the resolved key. The path is
// relative to the region, for example "api/v1/namespaces/<ns>/secrets/<name>". It returns the
// status and body of the answer.
func (c *K8sClient) tenantRequest(ctx context.Context, method, path string, payload []byte) (status int, body []byte, err error) {
	endpoint := fmt.Sprintf("https://%s/oidc-proxy/%s/%s/%s", c.fqdn, c.key, c.regionName, path)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("create %s request for %s: %w", method, path, err)
	}
	req.Header.Add("Authorization", "Bearer "+c.bearerToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read response of %s %s: %w", method, path, err)
	}
	return resp.StatusCode, body, nil
}

// bootstrapKubeconfigPath returns the region-relative path of the BootstrapKubeconfig collection
// in the resolved namespace.
func (c *K8sClient) bootstrapKubeconfigPath() string {
	return fmt.Sprintf("apis/%s/namespaces/%s/bootstrapkubeconfigs", bootstrapKubeconfigAPIVersion, c.namespace)
}

// ResolveTenantNamespace finds the tenant namespace by reading the kube-root-ca.crt ConfigMap
// through each candidate tenant ref. It records the namespace the ConfigMap reports, which can
// differ from the candidate in the URL, and the key that served it.
func (c *K8sClient) ResolveTenantNamespace(ctx context.Context) error {
	body, ref, err := c.getFromTenant(ctx, "configmaps/kube-root-ca.crt")
	if err != nil {
		return err
	}

	var configMap struct {
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err = json.Unmarshal(body, &configMap); err != nil {
		return fmt.Errorf("parse configmap kube-root-ca.crt: %w", err)
	}

	c.key = ref.key
	c.namespace = ref.namespace
	if configMap.Metadata.Namespace != "" {
		c.namespace = configMap.Metadata.Namespace
	}
	return nil
}

// CreateBootstrapKubeconfig creates a BootstrapKubeconfig in the resolved tenant namespace. The
// object has no apiserver and no CA: the controller derives both.
func (c *K8sClient) CreateBootstrapKubeconfig(ctx context.Context, name, hostName string) error {
	if c.key == "" {
		return errNamespaceNotResolved
	}

	object := infrastructurev1beta1.BootstrapKubeconfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: bootstrapKubeconfigAPIVersion,
			Kind:       "BootstrapKubeconfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.namespace,
		},
		Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
			HostName: hostName,
		},
	}
	payload, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("marshal BootstrapKubeconfig %s: %w", name, err)
	}

	status, body, err := c.tenantRequest(ctx, http.MethodPost, c.bootstrapKubeconfigPath(), payload)
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return &StatusError{Action: "create BootstrapKubeconfig " + name, StatusCode: status, Body: string(body)}
	}
	return nil
}

// isRetryableStatus reports whether a failed read of the BootstrapKubeconfig can succeed later.
// Other client errors, such as 400, 401 and 403, will not change by waiting.
func isRetryableStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// WaitForCredentialSecretRef reads the named BootstrapKubeconfig every interval until its status
// names the credential Secret, and returns that name. Transport errors, 404, 429 and 5xx answers
// are retried until timeout. Other error statuses end the wait at once. A timeout error names the
// object and the last error seen.
func (c *K8sClient) WaitForCredentialSecretRef(ctx context.Context, name string, interval, timeout time.Duration) (string, error) {
	if c.key == "" {
		return "", errNamespaceNotResolved
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	timedOut := func(lastErr error) error {
		if lastErr == nil {
			return fmt.Errorf("BootstrapKubeconfig %s has no credential Secret after %s: %w", name, timeout, ctx.Err())
		}
		return fmt.Errorf("BootstrapKubeconfig %s has no credential Secret after %s, last error: %v: %w", name, timeout, lastErr, ctx.Err())
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		status, body, err := c.tenantRequest(ctx, http.MethodGet, c.bootstrapKubeconfigPath()+"/"+name, nil)
		switch {
		case ctx.Err() != nil:
			return "", timedOut(lastErr)
		case err != nil:
			lastErr = err
		case status == http.StatusOK:
			var object struct {
				Status struct {
					CredentialSecretRef string `json:"credentialSecretRef"`
				} `json:"status"`
			}
			if err = json.Unmarshal(body, &object); err != nil {
				return "", fmt.Errorf("parse BootstrapKubeconfig %s: %w", name, err)
			}
			if object.Status.CredentialSecretRef != "" {
				return object.Status.CredentialSecretRef, nil
			}
		default:
			statusErr := &StatusError{Action: "get BootstrapKubeconfig " + name, StatusCode: status, Body: string(body)}
			if !isRetryableStatus(status) {
				return "", statusErr
			}
			lastErr = statusErr
		}

		select {
		case <-ctx.Done():
			return "", timedOut(lastErr)
		case <-ticker.C:
		}
	}
}

// GetCredentialKubeconfig reads the named credential Secret and returns the decoded kubeconfig.
func (c *K8sClient) GetCredentialKubeconfig(ctx context.Context, secretName string) ([]byte, error) {
	if c.key == "" {
		return nil, errNamespaceNotResolved
	}

	path := fmt.Sprintf("api/v1/namespaces/%s/secrets/%s", c.namespace, secretName)
	status, body, err := c.tenantRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &StatusError{Action: "get Secret " + secretName, StatusCode: status, Body: string(body)}
	}

	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err = json.Unmarshal(body, &secret); err != nil {
		return nil, fmt.Errorf("parse Secret %s: %w", secretName, err)
	}
	encoded, ok := secret.Data[credentialKubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("secret %s has no %q key", secretName, credentialKubeconfigKey)
	}
	kubeconfig, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode %q of Secret %s: %w", credentialKubeconfigKey, secretName, err)
	}
	return kubeconfig, nil
}

// CheckRegionAvailabilityViaProxy checks the region against the region-config ConfigMap read
// through the oidc-proxy, so no kubeconfig is needed. It returns the known regions when
// regionName is not among them.
func (c *K8sClient) CheckRegionAvailabilityViaProxy(ctx context.Context, regionName string) (found bool, knownRegions []string, err error) {
	if c.key == "" {
		return false, nil, errNamespaceNotResolved
	}

	path := fmt.Sprintf("api/v1/namespaces/%s/configmaps/region-config", c.namespace)
	status, body, err := c.tenantRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, nil, err
	}
	if status != http.StatusOK {
		return false, nil, &StatusError{Action: "get ConfigMap region-config", StatusCode: status, Body: string(body)}
	}

	var configMap struct {
		Data map[string]string `json:"data"`
	}
	if err = json.Unmarshal(body, &configMap); err != nil {
		return false, nil, fmt.Errorf("parse ConfigMap region-config: %w", err)
	}
	regionsStr, ok := configMap.Data["regions"]
	if !ok {
		return false, nil, errors.New("region configmap does not have regions key")
	}
	found, knownRegions = matchRegion(regionsStr, regionName)
	return found, knownRegions, nil
}
