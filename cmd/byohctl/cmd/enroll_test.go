// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrav1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
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
	tests := []struct {
		name                     string
		installAgentErr          error
		usingBootstrapKubeconfig bool
		wantCalls                []string
		wantErrPhrase            string
	}{
		{
			name:      "installs before enrolling",
			wantCalls: []string{"install", "enroll"},
		},
		{
			name:                     "skips enrollment for the bootstrap-kubeconfig escape hatch",
			usingBootstrapKubeconfig: true,
			wantCalls:                []string{"install"},
		},
		{
			name:            "a setup failure aborts before enrollment ever runs",
			installAgentErr: errors.New("dpkg is locked"),
			wantCalls:       []string{"install"},
			wantErrPhrase:   "failed to setup agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			installAgent := func(pkgDir string) error {
				calls = append(calls, "install")
				return tt.installAgentErr
			}
			enroll := func(_ context.Context, _ *client.K8sClient, _ string, _ hostname.Name, _ string) error {
				calls = append(calls, "enroll")
				return nil
			}

			err := installAndEnroll(t.Context(), t.TempDir(), t.TempDir(), "host1", "region1",
				tt.usingBootstrapKubeconfig, nil, installAgent, enroll)

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

const (
	// enrollHostGuessedNamespace is the namespace a K8sClient built by startEnrollHostAPI
	// guesses: the first dot-separated segment of the httptest server's host (always 127),
	// the domain, and the tenant. See K8sClient.Namespace.
	enrollHostGuessedNamespace = "127-default-service"

	// enrollHostRealNamespace is what the oidc-proxy rewrites that guess to. It is
	// deliberately different, so a test fails if enrollHost uses the guess anywhere the
	// namespace read back off the created enrollment belongs.
	enrollHostRealNamespace = "tenant-real-ns"

	enrollHostRegion   = "region1"
	enrollHostName     = "host1"
	enrollHostKubeconf = "apiVersion: v1\nkind: Config\n"
)

// enrollHostAPI stands in for the oidc-proxy and the management cluster behind it: it accepts
// the ByoHostEnrollment create and serves the credential Secret a controller would produce.
type enrollHostAPI struct {
	// secret is served on the credential Secret read. Nil means the Secret never appears, so
	// the read keeps 404ing and the caller can only time out.
	secret *corev1.Secret

	// byohDir, when set, is checked for the agent's namespace file on the first credential
	// Secret read. This is the only place the ordering rule -- namespace file written
	// strictly before polling starts -- can actually be observed.
	byohDir string
}

// startEnrollHostAPI serves api over TLS and returns a real K8sClient pointed at it. The client
// is built with insecure=true so it accepts httptest's self-signed certificate.
func startEnrollHostAPI(t *testing.T, api enrollHostAPI) *client.K8sClient {
	t.Helper()

	wantEnrollmentPath := "/oidc-proxy/" + enrollHostGuessedNamespace + "/" + enrollHostRegion +
		"/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/" + enrollHostGuessedNamespace + "/byohostenrollments"
	wantSecretPath := "/oidc-proxy/" + enrollHostRealNamespace + "/" + enrollHostRegion +
		"/api/v1/namespaces/" + enrollHostRealNamespace + "/secrets/" + enrollHostName + infrav1beta1.CredentialSecretNameSuffix

	// The handler runs on the server's goroutine, so it reports failures with assert rather
	// than require: require's FailNow is only legal on the test's own goroutine.
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/byohostenrollments"):
			assert.Equal(t, wantEnrollmentPath, r.URL.Path)

			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var created unstructured.Unstructured
			if !assert.NoError(t, json.Unmarshal(body, &created)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// Standing in for the proxy's namespace rewrite: the created object comes back
			// in the caller's real tenant namespace, not the one it asked for.
			created.SetNamespace(enrollHostRealNamespace)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			assert.NoError(t, json.NewEncoder(w).Encode(&created))

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/secrets/"):
			assert.Equal(t, wantSecretPath, r.URL.Path)
			if api.byohDir != "" {
				_, err := os.Stat(filepath.Join(api.byohDir, "namespace"))
				assert.NoError(t, err, "namespace file must exist before the credential Secret is polled for")
			}
			if api.secret == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			assert.NoError(t, json.NewEncoder(w).Encode(api.secret))

		default:
			assert.Fail(t, "unexpected request", "%s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}

	server := httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "https://")
	return client.NewK8sClient(host, "default", "service", "test-token", enrollHostRegion, true)
}

func newCredentialSecret(hostName, kubeconfig string) *corev1.Secret {
	data := map[string][]byte{infrav1beta1.CredentialSecretHostNameKey: []byte(hostName)}
	if kubeconfig != "" {
		data[infrav1beta1.CredentialSecretKubeconfigKey] = []byte(kubeconfig)
	}
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: enrollHostRealNamespace,
			Name:      enrollHostName + infrav1beta1.CredentialSecretNameSuffix,
		},
		Data: data,
	}
}

// useTempBootstrapConfDir redirects the agent's systemd drop-in directory at a temp dir and
// returns the bootstrap kubeconfig path inside it.
func useTempBootstrapConfDir(t *testing.T) string {
	t.Helper()

	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	return bootstrapKubeconfigDestPath()
}

func TestEnrollHost_Success(t *testing.T) {
	kubeconfigPath := useTempBootstrapConfDir(t)
	byohDir := t.TempDir()

	k8sClient := startEnrollHostAPI(t, enrollHostAPI{
		secret:  newCredentialSecret(enrollHostName, enrollHostKubeconf),
		byohDir: byohDir,
	})

	err := enrollHost(t.Context(), k8sClient, byohDir, enrollHostName, enrollHostRegion)
	require.NoError(t, err)

	writtenNamespace, err := os.ReadFile(filepath.Join(byohDir, "namespace"))
	require.NoError(t, err)
	assert.Equal(t, enrollHostRealNamespace, string(writtenNamespace))

	writtenKubeconfig, err := os.ReadFile(kubeconfigPath)
	require.NoError(t, err)
	assert.Equal(t, enrollHostKubeconf, string(writtenKubeconfig))
}

// TestEnrollHost_NeverWritesSavedKubeconfig is the regression guard for the reason enrollHost
// stopped going through a kubeconfig file at all: the agent skips its whole bootstrap-token-to-
// certificate exchange when ~/.byoh/config exists, so onboarding must leave that path alone.
func TestEnrollHost_NeverWritesSavedKubeconfig(t *testing.T) {
	useTempBootstrapConfDir(t)
	byohDir := t.TempDir()

	// The path is fixed at package init from the real home directory, so the assertion is
	// that enrollHost does not change whether it exists -- not that it is absent.
	_, statErrBefore := os.Stat(service.KubeconfigFilePath)

	k8sClient := startEnrollHostAPI(t, enrollHostAPI{secret: newCredentialSecret(enrollHostName, enrollHostKubeconf)})

	err := enrollHost(t.Context(), k8sClient, byohDir, enrollHostName, enrollHostRegion)
	require.NoError(t, err)

	_, statErrAfter := os.Stat(service.KubeconfigFilePath)
	assert.Equal(t, os.IsNotExist(statErrBefore), os.IsNotExist(statErrAfter),
		"enrollHost must not create or remove %s", service.KubeconfigFilePath)
}

func TestEnrollHost_RejectsBadCredentialSecret(t *testing.T) {
	tests := []struct {
		name          string
		secret        *corev1.Secret
		wantErrPhrase string
	}{
		{
			name:          "secret is for a different host",
			secret:        newCredentialSecret("some-other-host", enrollHostKubeconf),
			wantErrPhrase: "some-other-host",
		},
		{
			name:          "secret has no kubeconfig key",
			secret:        newCredentialSecret(enrollHostName, ""),
			wantErrPhrase: infrav1beta1.CredentialSecretKubeconfigKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeconfigPath := useTempBootstrapConfDir(t)
			byohDir := t.TempDir()

			k8sClient := startEnrollHostAPI(t, enrollHostAPI{secret: tt.secret})

			err := enrollHost(t.Context(), k8sClient, byohDir, enrollHostName, enrollHostRegion)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPhrase)

			// The namespace file is written before the Secret is ever read, so it survives.
			_, err = os.ReadFile(filepath.Join(byohDir, "namespace"))
			assert.NoError(t, err)

			// The bootstrap kubeconfig must not be written once a check on the Secret fails:
			// its appearance is what unblocks the agent.
			_, err = os.Stat(kubeconfigPath)
			assert.True(t, os.IsNotExist(err))
		})
	}
}

func TestEnrollHost_PollTimesOutCleanly(t *testing.T) {
	origInterval, origTimeout := credentialPollInterval, credentialPollTimeout
	credentialPollInterval = 5 * time.Millisecond
	credentialPollTimeout = 30 * time.Millisecond
	t.Cleanup(func() {
		credentialPollInterval = origInterval
		credentialPollTimeout = origTimeout
	})

	kubeconfigPath := useTempBootstrapConfDir(t)
	byohDir := t.TempDir()

	// No credential Secret is ever served, so the poll can only time out.
	k8sClient := startEnrollHostAPI(t, enrollHostAPI{byohDir: byohDir})

	err := enrollHost(t.Context(), k8sClient, byohDir, enrollHostName, enrollHostRegion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	// The namespace file was written before the poll started and stays behind.
	_, err = os.ReadFile(filepath.Join(byohDir, "namespace"))
	assert.NoError(t, err)

	_, err = os.Stat(kubeconfigPath)
	assert.True(t, os.IsNotExist(err))
}
