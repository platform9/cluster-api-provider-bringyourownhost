// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	utilrand "k8s.io/apimachinery/pkg/util/rand"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

const (
	// credentialPollInterval is how often the BootstrapKubeconfig is read while waiting for its credential.
	credentialPollInterval = 5 * time.Second
	// credentialPollTimeout is how long onboarding waits for the credential before it gives up.
	credentialPollTimeout = 10 * time.Minute
	// bootstrapKubeconfigSuffixLen is the number of random characters at the end of the object name.
	bootstrapKubeconfigSuffixLen = 5
)

// tokenAuth selects the legacy token flow instead of the certificate flow.
var tokenAuth bool

// onboardFlow names the way onboarding gets the host's bootstrap credential.
type onboardFlow int

const (
	// flowCertificate creates a BootstrapKubeconfig and reads the credential the controller issues.
	flowCertificate onboardFlow = iota
	// flowTokenAuth is the legacy flow that reads the shared bootstrap Secret.
	flowTokenAuth
	// flowBootstrapKubeconfig uses a bootstrap kubeconfig file given by path.
	flowBootstrapKubeconfig
)

// selectOnboardFlow picks the onboarding flow. The flags are mutually exclusive, and the
// certificate flow is the default.
func selectOnboardFlow(tokenAuth bool, bootstrapKubeconfigPath string) onboardFlow {
	switch {
	case bootstrapKubeconfigPath != "":
		return flowBootstrapKubeconfig
	case tokenAuth:
		return flowTokenAuth
	default:
		return flowCertificate
	}
}

// checkCertFlowFlags rejects flags that do not apply to the certificate flow.
func checkCertFlowFlags(cmd *cobra.Command) error {
	if cmd.Flags().Changed("namespace") {
		return errors.New("--namespace is not accepted here, the namespace comes from the login")
	}
	return nil
}

// certOnboardParams are the inputs of runCertOnboarding.
type certOnboardParams struct {
	fqdn        string
	username    string
	password    string
	clientToken string
	domain      string
	tenant      string
	region      string
	// hostName is the normalized host name stored in the BootstrapKubeconfig.
	hostName string
	insecure bool
	// pollInterval and pollTimeout bound the wait for the credential Secret.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// bootstrapKubeconfigName returns the object name for a host: the host name and five random characters.
func bootstrapKubeconfigName(hostName string) string {
	return hostName + "-" + utilrand.String(bootstrapKubeconfigSuffixLen)
}

// runCertOnboarding logs in, creates a BootstrapKubeconfig for the host and returns the kubeconfig
// in its credential Secret together with the tenant namespace. The region is checked before the
// object is created, so a wrong region leaves nothing behind. An object that was created is not
// deleted on failure, its token expires by itself, and the error names it.
func runCertOnboarding(ctx context.Context, params *certOnboardParams) (kubeconfig []byte, namespace string, err error) {
	authClient := client.NewAuthClient(params.fqdn, params.clientToken, params.insecure)
	token, err := authClient.GetToken(params.username, params.password)
	if err != nil {
		return nil, "", fmt.Errorf("get authentication token: %w", err)
	}

	k8sClient := client.NewK8sClient(params.fqdn, params.domain, params.tenant, token, params.region, params.insecure)
	projectID, err := authClient.GetProjectID(params.username, params.password, params.domain, params.tenant)
	if err != nil {
		utils.LogWarn("Could not look up project ID for %s/%s, using the name-based tenant namespace: %v",
			params.domain, params.tenant, err)
	} else {
		k8sClient.SetProjectID(projectID)
	}

	if err = k8sClient.ResolveTenantNamespace(ctx); err != nil {
		return nil, "", fmt.Errorf("resolve tenant namespace: %w", err)
	}

	available, regions, err := k8sClient.CheckRegionAvailabilityViaProxy(ctx, params.region)
	if err != nil {
		return nil, "", fmt.Errorf("check region availability: %w", err)
	}
	if !available {
		return nil, "", fmt.Errorf("region %s is not available for the tenant, available regions: %v", params.region, regions)
	}

	name := bootstrapKubeconfigName(params.hostName)
	if err = k8sClient.CreateBootstrapKubeconfig(ctx, name, params.hostName); err != nil {
		return nil, "", fmt.Errorf("create BootstrapKubeconfig %s: %w", name, err)
	}
	utils.LogInfo("Created BootstrapKubeconfig %s, waiting for its credential", name)

	secretName, err := k8sClient.WaitForCredentialSecretRef(ctx, name, params.pollInterval, params.pollTimeout)
	if err != nil {
		return nil, "", fmt.Errorf("BootstrapKubeconfig %s was created and left in place: %w", name, err)
	}
	kubeconfig, err = k8sClient.GetCredentialKubeconfig(ctx, secretName)
	if err != nil {
		return nil, "", fmt.Errorf("BootstrapKubeconfig %s was created and left in place: %w", name, err)
	}
	return kubeconfig, k8sClient.Namespace(), nil
}

// writeBootstrapCredentialBytes writes the bootstrap kubeconfig to confDir and the tenant
// namespace to byohDir. It never writes ~/.byoh/config, so the agent runs its own
// bootstrap-token-to-certificate exchange on first start.
func writeBootstrapCredentialBytes(byohDir, confDir string, data []byte, namespace string) error {
	var err error
	// The agent copies this stanza's TLS settings into the kubeconfig it generates after the
	// exchange, so recording --insecure here makes it stick for the life of the host.
	if insecure {
		data, err = client.MakeKubeconfigInsecure(data)
		if err != nil {
			return fmt.Errorf("failed to apply --insecure to bootstrap kubeconfig: %w", err)
		}
	}
	if err = os.MkdirAll(confDir, service.DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create %s: %w", confDir, err)
	}
	destPath := filepath.Join(confDir, "bootstrap-kubeconfig.yaml")
	// #nosec G703 -- confDir is a package constant in production
	if err = os.WriteFile(destPath, data, 0o600); err != nil { //nolint:mnd // 0o600 is the standard mode for a secret file
		return fmt.Errorf("failed to write %s: %w", destPath, err)
	}
	namespaceFile := filepath.Join(byohDir, "namespace")
	if err = os.WriteFile(namespaceFile, []byte(namespace), service.DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to save namespace: %w", err)
	}
	return nil
}
