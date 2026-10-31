// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	certTestProjectID = "0123456789abcdef"
	// certTestNamespace is the ID-based tenant namespace. The DU prefix of an httptest address is 127.
	certTestNamespace = "127-01234567"
	certTestRegion    = "region"
	certTestProxyRoot = "/oidc-proxy/" + certTestProjectID + "/" + certTestRegion + "/"
	certTestKubeconf  = "apiVersion: v1\nkind: Config\n"
)

// certTestServer is a fake Dex, Keystone and oidc-proxy. It records the requests it saw.
type certTestServer struct {
	mu       sync.Mutex
	requests []string
	// dexStatus is the status of POST /dex/token.
	dexStatus int
	// regions is the content of the region-config ConfigMap key "regions".
	regions string
	// credentialReady makes the created BootstrapKubeconfig report its credential Secret.
	credentialReady bool
	// createdName is the name in the last BootstrapKubeconfig POST body.
	createdName string
}

func (s *certTestServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *certTestServer) created() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createdName
}

func (s *certTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)

	nsPath := certTestProxyRoot + "api/v1/namespaces/" + certTestNamespace + "/"
	bkcPath := certTestProxyRoot + "apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/" + certTestNamespace + "/bootstrapkubeconfigs"
	switch {
	case r.URL.Path == "/dex/token":
		if s.dexStatus != 0 {
			w.WriteHeader(s.dexStatus)
			return
		}
		_, _ = w.Write([]byte(`{"id_token":"test-id-token"}`))
	case r.URL.Path == "/keystone/v3/auth/tokens":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":{"project":{"id":"` + certTestProjectID + `"}}}`))
	case r.URL.Path == nsPath+"configmaps/kube-root-ca.crt":
		_, _ = w.Write([]byte(`{"metadata":{"name":"kube-root-ca.crt","namespace":"` + certTestNamespace + `"}}`))
	case r.URL.Path == nsPath+"configmaps/region-config":
		data, _ := json.Marshal(map[string]any{"data": map[string]string{"regions": s.regions}})
		_, _ = w.Write(data)
	case r.Method == http.MethodPost && r.URL.Path == bkcPath:
		body, _ := io.ReadAll(r.Body)
		var object struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(body, &object)
		s.createdName = object.Metadata.Name
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, bkcPath+"/"):
		if s.credentialReady {
			_, _ = w.Write([]byte(`{"status":{"credentialSecretRef":"` + s.createdName + `-cred"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":{}}`))
	case r.URL.Path == nsPath+"secrets/"+s.createdName+"-cred":
		encoded := base64.StdEncoding.EncodeToString([]byte(certTestKubeconf))
		_, _ = w.Write([]byte(`{"data":{"kubeconfig":"` + encoded + `"}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newCertTestServer(t *testing.T) (*certTestServer, certOnboardParams) {
	t.Helper()
	srv := &certTestServer{regions: certTestRegion + "\nother", credentialReady: true}
	ts := httptest.NewTLSServer(http.HandlerFunc(srv.handle))
	t.Cleanup(ts.Close)
	params := certOnboardParams{
		fqdn:         strings.TrimPrefix(ts.URL, "https://"),
		username:     "user",
		password:     "pass",
		clientToken:  "client-token",
		domain:       "test-domain",
		tenant:       "test-tenant",
		region:       certTestRegion,
		hostName:     "my-host",
		insecure:     true,
		pollInterval: time.Millisecond,
		pollTimeout:  time.Second,
	}
	return srv, params
}

func TestRunCertOnboarding(t *testing.T) {
	t.Run("full flow", func(t *testing.T) {
		srv, params := newCertTestServer(t)

		kubeconfig, namespace, err := runCertOnboarding(context.Background(), &params)
		require.NoError(t, err)
		assert.Equal(t, []byte(certTestKubeconf), kubeconfig)
		assert.Equal(t, certTestNamespace, namespace)
		assert.True(t, strings.HasPrefix(srv.created(), "my-host-"), "created name %q", srv.created())
	})

	t.Run("region not available creates nothing", func(t *testing.T) {
		srv, params := newCertTestServer(t)
		srv.regions = "other"

		_, _, err := runCertOnboarding(context.Background(), &params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), certTestRegion)
		for _, request := range srv.seen() {
			assert.False(t, strings.HasPrefix(request, "POST "+certTestProxyRoot), "unexpected request %s", request)
		}
	})

	t.Run("credential never appears", func(t *testing.T) {
		srv, params := newCertTestServer(t)
		srv.credentialReady = false
		params.pollTimeout = 50 * time.Millisecond

		_, _, err := runCertOnboarding(context.Background(), &params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), srv.created())
		for _, request := range srv.seen() {
			assert.False(t, strings.HasPrefix(request, "DELETE "), "unexpected request %s", request)
		}
	})

	t.Run("login fails", func(t *testing.T) {
		srv, params := newCertTestServer(t)
		srv.dexStatus = http.StatusUnauthorized

		_, _, err := runCertOnboarding(context.Background(), &params)
		require.Error(t, err)
		assert.Equal(t, []string{"POST /dex/token"}, srv.seen())
	})
}

func TestBootstrapKubeconfigName(t *testing.T) {
	first := bootstrapKubeconfigName("my-host")
	second := bootstrapKubeconfigName("my-host")

	assert.True(t, strings.HasPrefix(first, "my-host-"), first)
	assert.Len(t, first, len("my-host-")+5)
	assert.NotEqual(t, first, second)
}

func TestSelectOnboardFlow(t *testing.T) {
	testCases := []struct {
		name                string
		tokenAuth           bool
		bootstrapKubeconfig string
		want                onboardFlow
	}{
		{
			name: "default is the certificate flow",
			want: flowCertificate,
		},
		{
			name:      "token auth flag",
			tokenAuth: true,
			want:      flowTokenAuth,
		},
		{
			name:                "bootstrap kubeconfig path",
			bootstrapKubeconfig: "/tmp/bootstrap.yaml",
			want:                flowBootstrapKubeconfig,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectOnboardFlow(tc.tokenAuth, tc.bootstrapKubeconfig)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOnboardTokenAuthFlag(t *testing.T) {
	testCases := []struct {
		name          string
		args          []string
		wantTokenAuth bool
		wantErr       bool
	}{
		{
			name:          "absent",
			args:          []string{"-u", "fqdn", "-e", "user", "-c", "token", "-r", "region"},
			wantTokenAuth: false,
			wantErr:       false,
		},
		{
			name:          "present",
			args:          []string{"--token-auth", "-u", "fqdn", "-e", "user", "-c", "token", "-r", "region"},
			wantTokenAuth: true,
			wantErr:       false,
		},
		{
			name:          "with bootstrap kubeconfig",
			args:          []string{"--token-auth", "--bootstrap-kubeconfig", "x", "-r", "region"},
			wantTokenAuth: true,
			wantErr:       true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetOnboardGlobals()
			testCmd := createTestCommand()
			testCmd.SetArgs(tc.args)
			var output bytes.Buffer
			testCmd.SetOut(&output)
			testCmd.SetErr(&output)

			err := testCmd.Execute()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, output.String(), "none of the others can be")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTokenAuth, tokenAuth)
		})
	}
}

func TestOnboardRemovedFlagsAreAbsent(t *testing.T) {
	testCmd := createTestCommand()

	assert.Nil(t, testCmd.Flags().Lookup("apiserver"))
	assert.Nil(t, testCmd.Flags().Lookup("certificate-authority"))
	assert.Nil(t, testCmd.Flags().Lookup("ca"))
}

func TestCheckCertFlowFlags(t *testing.T) {
	testCases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "namespace not set",
			args:    []string{"-r", "region"},
			wantErr: false,
		},
		{
			name:    "namespace set",
			args:    []string{"--namespace", "foo", "-r", "region"},
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resetOnboardGlobals()
			testCmd := createTestCommand()
			testCmd.SetArgs(tc.args)
			testCmd.SetOut(&bytes.Buffer{})
			testCmd.SetErr(&bytes.Buffer{})
			// The command has no region requirement of its own to satisfy here, so ignore its result.
			_ = testCmd.Execute()

			err := checkCertFlowFlags(testCmd)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "the namespace comes from the login")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestWriteBootstrapCredentialBytes(t *testing.T) {
	const kubeconfigContent = `apiVersion: v1
kind: Config
clusters:
- name: du
  cluster:
    server: https://du.example.com
    certificate-authority-data: dGVzdA==
current-context: ""
`
	testCases := []struct {
		name         string
		insecure     bool
		wantInsecure bool
	}{
		{
			name:         "secure",
			insecure:     false,
			wantInsecure: false,
		},
		{
			name:         "insecure clears the CA and skips verification",
			insecure:     true,
			wantInsecure: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			byohDir := t.TempDir()
			confDir := t.TempDir()
			origInsecure := insecure
			insecure = tc.insecure
			t.Cleanup(func() {
				insecure = origInsecure
			})

			err := writeBootstrapCredentialBytes(byohDir, confDir, []byte(kubeconfigContent), "test-tenant-ns")
			require.NoError(t, err)

			path := filepath.Join(confDir, "bootstrap-kubeconfig.yaml")
			written, err := os.ReadFile(path)
			require.NoError(t, err)
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			if tc.wantInsecure {
				assert.Contains(t, string(written), "insecure-skip-tls-verify: true")
				assert.NotContains(t, string(written), "certificate-authority-data")
			} else {
				assert.Equal(t, kubeconfigContent, string(written))
			}

			namespace, err := os.ReadFile(filepath.Join(byohDir, "namespace"))
			require.NoError(t, err)
			assert.Equal(t, "test-tenant-ns", string(namespace))

			_, err = os.Stat(filepath.Join(byohDir, "config"))
			assert.True(t, os.IsNotExist(err), "config file must not be written")
		})
	}
}
