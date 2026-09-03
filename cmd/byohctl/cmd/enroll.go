// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// cmd/byohctl/cmd/enroll.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	infrav1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

// credentialPollInterval and credentialPollTimeout are variables, not constants, so tests can
// shrink them and exercise the timeout path without actually waiting on it.
var (
	// credentialPollInterval is how often byohctl checks for the credential Secret a
	// ByoHostEnrollment produces. The credential is created within a single controller
	// reconcile, so a short interval notices it soon after without hammering the API.
	credentialPollInterval = 2 * time.Second

	// credentialPollTimeout bounds the wait for that Secret. It is a small fraction of the
	// bootstrap token's default 30 minute life, so a stuck reconcile is reported quickly
	// instead of silently spending most of the token's usable window, while still being
	// generous enough to absorb a controller restart or a brief apiserver hiccup.
	credentialPollTimeout = 2 * time.Minute
)

// osHostname is a variable so tests can replace it with a mock, same pattern as osReadFile in
// cmd/byohctl/cmd/onboard.go.
var osHostname = os.Hostname

// setupAgent and enrollHostFunc are variables so tests can verify installAndEnroll's ordering
// without actually installing a package or reaching a cluster. getK8sClient is a variable so
// tests can point enrollHost at a fake dynamic client and clientset instead of a real
// kubeconfig file on disk.
var (
	setupAgent     = service.SetupAgent
	enrollHostFunc = enrollHost
	getK8sClient   = client.GetK8sClient
)

// computeHostName turns this machine's reported host name into the object name used for it
// on the management cluster. Failing here is a hard, early stop: a host name that cannot
// normalize would otherwise surface only much later, as a certificate common name the
// approver refuses.
func computeHostName() (hostname.Name, error) {
	raw, err := osHostname()
	if err != nil {
		return "", fmt.Errorf("failed to read this host's name: %w", err)
	}

	name, err := hostname.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("failed to normalize this host's name: %w", err)
	}

	return name, nil
}

// installAndEnroll installs the agent package, then -- unless an operator-supplied bootstrap
// kubeconfig is already in play -- creates the host's enrollment and waits for its credential.
// Installing first matters: the package pull and install are the slowest steps in onboarding,
// so doing them before the credential exists means neither one spends any of the bootstrap
// token's limited lifetime.
func installAndEnroll(ctx context.Context, pkgDir, byohDir string, hostName hostname.Name, regionName string, usingBootstrapKubeconfig bool, k8sClient *client.K8sClient) error {
	if err := setupAgent(pkgDir); err != nil {
		return fmt.Errorf("failed to setup agent: %w", err)
	}

	if usingBootstrapKubeconfig {
		return nil
	}

	return enrollHostFunc(ctx, k8sClient, byohDir, hostName, regionName)
}

// enrollHost creates a ByoHostEnrollment for hostName, labeled with the region, then waits
// for the credential Secret it produces and writes that credential out as the agent's
// bootstrap kubeconfig.
//
// Order matters here. The namespace file is written as soon as the enrollment's real
// namespace is known, strictly before the credential Secret is polled for, and the
// kubeconfig file is written last, once the Secret is in hand and cross-checked. The agent
// reads the namespace file once at startup and separately blocks on the kubeconfig file
// appearing (see resolveNamespace and waitForBootstrapCredential in agent/main.go); writing
// the kubeconfig first would let the agent start against the wrong namespace and never
// notice, since it does not re-read the namespace file afterwards.
//
// A failure after the enrollment is created leaves that enrollment behind rather than
// deleting it. Retrying onboarding would create a second ByoHostEnrollment for the same host
// name, which the API server's name conflict already rejects on its own, so deleting the
// first one here would not make a retry succeed, only remove a record of what was attempted.
func enrollHost(ctx context.Context, k8sClient *client.K8sClient, byohDir string, hostName hostname.Name, regionName string) error {
	mgmtClient, err := getK8sClient(service.KubeconfigFilePath)
	if err != nil {
		return fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	utils.LogInfo("Creating enrollment for host %s", hostName)
	labels := map[string]string{service.PcdKaapiRegionKey: regionName}
	namespace, err := mgmtClient.CreateByoHostEnrollment(ctx, k8sClient.Namespace(), string(hostName), labels)
	if err != nil {
		return fmt.Errorf("failed to create host enrollment: %w", err)
	}
	utils.LogSuccess("Created host enrollment in namespace %s", namespace)

	if err := writeNamespaceFile(byohDir, namespace); err != nil {
		return err
	}

	secretName := string(hostName) + infrav1beta1.CredentialSecretNameSuffix
	utils.LogInfo("Waiting for credential secret %s", secretName)
	secret, err := mgmtClient.AwaitCredentialSecret(ctx, namespace, secretName, credentialPollInterval, credentialPollTimeout)
	if err != nil {
		return fmt.Errorf("failed to fetch credential secret: %w", err)
	}

	secretHostName := string(secret.Data[infrav1beta1.CredentialSecretHostNameKey])
	if secretHostName != string(hostName) {
		return fmt.Errorf("credential secret %s is for host %q, not %q", secretName, secretHostName, hostName)
	}

	kubeconfig, ok := secret.Data[infrav1beta1.CredentialSecretKubeconfigKey]
	if !ok {
		return fmt.Errorf("credential secret %s has no %s key", secretName, infrav1beta1.CredentialSecretKubeconfigKey)
	}

	if err := writeBootstrapKubeconfigFile(kubeconfig); err != nil {
		return err
	}

	utils.LogSuccess("Wrote bootstrap credential for host %s", hostName)
	return nil
}
