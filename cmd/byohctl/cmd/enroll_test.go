// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrav1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
)

func TestComputeHostName(t *testing.T) {
	origHostname := osHostname
	t.Cleanup(func() { osHostname = origHostname })

	tests := []struct {
		name           string
		hostnameFn     func() (string, error)
		want           hostname.Name
		wantErrPhrases []string
	}{
		{
			name:       "normalizes successfully",
			hostnameFn: func() (string, error) { return "My-Host.example.com", nil },
			want:       "my-host.example.com",
		},
		{
			name:           "os.Hostname fails",
			hostnameFn:     func() (string, error) { return "", errors.New("no hostname") },
			wantErrPhrases: []string{"failed to read this host's name", "no hostname"},
		},
		{
			name:       "name does not normalize",
			hostnameFn: func() (string, error) { return "bad_host!name", nil },
			// Normalize's error names both the raw input and the normalized attempt; this
			// wraps that error rather than replacing it, so both must still be present.
			wantErrPhrases: []string{"bad_host!name", "bad-host!name"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osHostname = tt.hostnameFn

			got, err := computeHostName()

			if len(tt.wantErrPhrases) > 0 {
				require.Error(t, err)
				for _, phrase := range tt.wantErrPhrases {
					assert.Contains(t, err.Error(), phrase)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInstallAndEnroll(t *testing.T) {
	origSetupAgent := setupAgent
	origEnrollHostFunc := enrollHostFunc
	t.Cleanup(func() {
		setupAgent = origSetupAgent
		enrollHostFunc = origEnrollHostFunc
	})

	tests := []struct {
		name                     string
		setupAgentErr            error
		usingBootstrapKubeconfig bool
		wantCalls                []string
		wantErrPhrase            string
	}{
		{
			name:      "installs before enrolling",
			wantCalls: []string{"setup", "enroll"},
		},
		{
			name:                     "skips enrollment for the bootstrap-kubeconfig escape hatch",
			usingBootstrapKubeconfig: true,
			wantCalls:                []string{"setup"},
		},
		{
			name:          "a setup failure aborts before enrollment ever runs",
			setupAgentErr: errors.New("dpkg is locked"),
			wantCalls:     []string{"setup"},
			wantErrPhrase: "failed to setup agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			setupAgent = func(pkgDir string) error {
				calls = append(calls, "setup")
				return tt.setupAgentErr
			}
			enrollHostFunc = func(_ context.Context, _ *client.K8sClient, _ string, _ hostname.Name, _ string) error {
				calls = append(calls, "enroll")
				return nil
			}

			err := installAndEnroll(t.Context(), t.TempDir(), t.TempDir(), "host1", "region1", tt.usingBootstrapKubeconfig, nil)

			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErrPhrase != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrPhrase)
				return
			}
			require.NoError(t, err)
		})
	}
}

// enrollHostTestNamespace is the tenant namespace client.NewK8sClient's fixture below guesses,
// derived the same way K8sClient.getNamespace does: fqdn prefix "test", domain "default", tenant
// "service".
const enrollHostTestNamespace = "test-default-service"

// newEnrollHostFixture builds a fake management client -- seeded with secret when non-nil -- and
// a replacement for the getK8sClient seam that returns it instead of reading a real kubeconfig
// file from disk.
func newEnrollHostFixture(t *testing.T, secret *corev1.Secret) (*client.K8sClient, func(string) (*client.Client, error)) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, infrav1beta1.AddToScheme(scheme))
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	clientset := k8sfake.NewSimpleClientset()
	if secret != nil {
		clientset = k8sfake.NewSimpleClientset(secret)
	}

	mgmtClient := &client.Client{
		DynamicClient: dynamicClient,
		Clientset:     clientset,
	}

	k8sClient := client.NewK8sClient("test.platform9.com", "default", "service", "token", "region1", false)

	return k8sClient, func(string) (*client.Client, error) { return mgmtClient, nil }
}

func newCredentialSecret(hostName, kubeconfig string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: enrollHostTestNamespace,
			Name:      "host1" + infrav1beta1.CredentialSecretNameSuffix,
		},
		Data: map[string][]byte{
			infrav1beta1.CredentialSecretHostNameKey:   []byte(hostName),
			infrav1beta1.CredentialSecretKubeconfigKey: []byte(kubeconfig),
		},
	}
}

func TestEnrollHost_Success(t *testing.T) {
	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	origGetK8sClient := getK8sClient
	t.Cleanup(func() { getK8sClient = origGetK8sClient })

	const kubeconfigContent = "apiVersion: v1\nkind: Config\n"
	k8sClient, fakeGetK8sClient := newEnrollHostFixture(t, newCredentialSecret("host1", kubeconfigContent))
	getK8sClient = fakeGetK8sClient

	byohDir := t.TempDir()
	err := enrollHost(t.Context(), k8sClient, byohDir, "host1", "region1")
	require.NoError(t, err)

	writtenNamespace, err := os.ReadFile(filepath.Join(byohDir, "namespace"))
	require.NoError(t, err)
	assert.Equal(t, enrollHostTestNamespace, string(writtenNamespace))

	writtenKubeconfig, err := os.ReadFile(bootstrapKubeconfigDestPath())
	require.NoError(t, err)
	assert.Equal(t, kubeconfigContent, string(writtenKubeconfig))
}

func TestEnrollHost_HostNameMismatchAborts(t *testing.T) {
	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	origGetK8sClient := getK8sClient
	t.Cleanup(func() { getK8sClient = origGetK8sClient })

	k8sClient, fakeGetK8sClient := newEnrollHostFixture(t, newCredentialSecret("some-other-host", "apiVersion: v1\nkind: Config\n"))
	getK8sClient = fakeGetK8sClient

	byohDir := t.TempDir()
	err := enrollHost(t.Context(), k8sClient, byohDir, "host1", "region1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some-other-host")

	// Step 6 (write the namespace file) already ran before the mismatch was caught in step 8.
	_, err = os.ReadFile(filepath.Join(byohDir, "namespace"))
	require.NoError(t, err)

	// Step 9 (write the bootstrap kubeconfig) must never run once the cross-check fails.
	_, err = os.ReadFile(bootstrapKubeconfigDestPath())
	assert.True(t, os.IsNotExist(err))
}

func TestEnrollHost_PollTimesOutCleanly(t *testing.T) {
	origInterval, origTimeout := credentialPollInterval, credentialPollTimeout
	credentialPollInterval = 5 * time.Millisecond
	credentialPollTimeout = 30 * time.Millisecond
	t.Cleanup(func() {
		credentialPollInterval = origInterval
		credentialPollTimeout = origTimeout
	})

	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	origGetK8sClient := getK8sClient
	t.Cleanup(func() { getK8sClient = origGetK8sClient })

	// No credential Secret is ever seeded, so the poll can only time out.
	k8sClient, fakeGetK8sClient := newEnrollHostFixture(t, nil)
	getK8sClient = fakeGetK8sClient

	byohDir := t.TempDir()
	err := enrollHost(t.Context(), k8sClient, byohDir, "host1", "region1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	// The namespace write from step 6 still happened even though the poll afterward failed.
	_, err = os.ReadFile(filepath.Join(byohDir, "namespace"))
	require.NoError(t, err)

	_, err = os.ReadFile(bootstrapKubeconfigDestPath())
	assert.True(t, os.IsNotExist(err))
}
