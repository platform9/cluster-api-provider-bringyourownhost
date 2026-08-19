// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

func TestNewDUHTTPClientSecureByDefault(t *testing.T) {
	c := NewDUHTTPClient(7*time.Second, false)

	if c.Timeout != 7*time.Second {
		t.Errorf("expected timeout 7s, got %v", c.Timeout)
	}
	// A nil Transport means http.DefaultTransport, i.e. exactly the behavior byohctl
	// had before --insecure existed. Anything else risks changing proxy or TLS defaults
	// for users who never asked for --insecure.
	if c.Transport != nil {
		t.Errorf("expected nil Transport when insecure=false, got %T", c.Transport)
	}
}

func TestNewDUHTTPClientInsecureSkipsVerification(t *testing.T) {
	c := NewDUHTTPClient(DefaultTimeout, true)

	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be set")
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true")
	}
}

// TestNewDUHTTPClientAgainstSelfSignedServer is the test that actually matters: httptest's
// TLS server presents a self-signed certificate, the same situation as an on-prem DU.
func TestNewDUHTTPClientAgainstSelfSignedServer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	get := func(t *testing.T, insecure bool) (*http.Response, error) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, http.NoBody)
		if err != nil {
			t.Fatalf("failed to build request: %v", err)
		}
		return NewDUHTTPClient(DefaultTimeout, insecure).Do(req)
	}

	t.Run("fails without insecure", func(t *testing.T) {
		resp, err := get(t, false)
		if err == nil {
			resp.Body.Close()
			t.Fatal("expected TLS verification to fail against a self-signed certificate")
		}
		if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("expected a certificate error, got %v", err)
		}
	})

	t.Run("succeeds with insecure", func(t *testing.T) {
		resp, err := get(t, true)
		if err != nil {
			t.Fatalf("expected request to succeed with insecure=true, got %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
	})
}

// TestConstructorsHonourInsecure exercises the exported constructors rather than
// NewDUHTTPClient directly. The helper being correct is worthless if a constructor forgets to
// pass the flag through, and both of these talk to the DU over the certificate in question.
func TestConstructorsHonourInsecure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Enough of a response for each client to get past TLS and parse something.
		_, _ = w.Write([]byte(`{"id_token":"test-token","data":{"config":""}}`))
	}))
	defer server.Close()

	// Both clients build their URLs as https://{fqdn}/..., so the fqdn is the bare host:port.
	fqdn := strings.TrimPrefix(server.URL, "https://")

	t.Run("NewAuthClient rejects self-signed by default", func(t *testing.T) {
		_, err := NewAuthClient(fqdn, "client-token", false).GetToken("user", "pass")
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("expected a certificate error, got %v", err)
		}
	})

	t.Run("NewAuthClient accepts self-signed with insecure", func(t *testing.T) {
		token, err := NewAuthClient(fqdn, "client-token", true).GetToken("user", "pass")
		if err != nil {
			t.Fatalf("expected TLS to be tolerated, got %v", err)
		}
		if token != "test-token" {
			t.Errorf("expected the token from the server, got %q", token)
		}
	})

	t.Run("NewK8sClient rejects self-signed by default", func(t *testing.T) {
		_, err := NewK8sClient(fqdn, "domain", "tenant", "token", "region", false).GetSecret("some-secret")
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("expected a certificate error, got %v", err)
		}
	})

	t.Run("NewK8sClient accepts self-signed with insecure", func(t *testing.T) {
		if _, err := NewK8sClient(fqdn, "domain", "tenant", "token", "region", true).GetSecret("some-secret"); err != nil {
			t.Fatalf("expected TLS to be tolerated, got %v", err)
		}
	})
}

const kubeconfigWithCA = `apiVersion: v1
kind: Config
clusters:
- name: du
  cluster:
    server: https://du.example.com:443
    certificate-authority-data: dGVzdC1jYS1kYXRh
contexts:
- name: du-context
  context:
    cluster: du
    user: byoh
    namespace: some-namespace
current-context: du-context
users:
- name: byoh
  user:
    token: bootstrap-token
`

func TestMakeKubeconfigInsecure(t *testing.T) {
	out, err := MakeKubeconfigInsecure([]byte(kubeconfigWithCA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("result is not a valid kubeconfig: %v", err)
	}

	cluster, ok := cfg.Clusters["du"]
	if !ok {
		t.Fatal("cluster 'du' missing from result")
	}
	if !cluster.InsecureSkipTLSVerify {
		t.Error("expected insecure-skip-tls-verify to be set")
	}
	// clientcmd rejects a kubeconfig that sets both, so the CA must be cleared rather
	// than merely overridden.
	if len(cluster.CertificateAuthorityData) != 0 || cluster.CertificateAuthority != "" {
		t.Error("expected certificate authority to be cleared")
	}
	if cluster.Server != "https://du.example.com:443" {
		t.Errorf("server was altered: %s", cluster.Server)
	}

	// Everything the agent needs besides the cluster stanza must survive untouched.
	if cfg.CurrentContext != "du-context" {
		t.Errorf("current-context was altered: %s", cfg.CurrentContext)
	}
	if ctx := cfg.Contexts["du-context"]; ctx == nil || ctx.Namespace != "some-namespace" {
		t.Error("context namespace was not preserved")
	}
	if user := cfg.AuthInfos["byoh"]; user == nil || user.Token != "bootstrap-token" {
		t.Error("user token was not preserved")
	}
}

// TestMakeKubeconfigInsecureIsLoadableAsRESTConfig guards the failure mode that would
// otherwise only show up at runtime: client-go errors out if a cluster sets both a CA and
// the insecure flag.
func TestMakeKubeconfigInsecureIsLoadableAsRESTConfig(t *testing.T) {
	out, err := MakeKubeconfigInsecure([]byte(kubeconfigWithCA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config")
	if writeErr := os.WriteFile(path, out, 0o600); writeErr != nil {
		t.Fatalf("failed to write kubeconfig: %v", writeErr)
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatalf("stamped kubeconfig did not load as a rest.Config: %v", err)
	}
	if !restConfig.Insecure {
		t.Error("expected rest.Config.Insecure to be true")
	}
	if len(restConfig.CAData) != 0 || restConfig.CAFile != "" {
		t.Error("expected rest.Config to carry no CA, since Insecure and CA are mutually exclusive")
	}
}

func TestMakeKubeconfigInsecureAllClusters(t *testing.T) {
	multi := `apiVersion: v1
kind: Config
clusters:
- name: first
  cluster:
    server: https://one.example.com
    certificate-authority-data: dGVzdA==
- name: second
  cluster:
    server: https://two.example.com
    certificate-authority: /etc/ssl/du.crt
current-context: ""
`
	out, err := MakeKubeconfigInsecure([]byte(multi))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("result is not a valid kubeconfig: %v", err)
	}
	for name, cluster := range cfg.Clusters {
		if !cluster.InsecureSkipTLSVerify {
			t.Errorf("cluster %s: expected insecure-skip-tls-verify", name)
		}
		if len(cluster.CertificateAuthorityData) != 0 || cluster.CertificateAuthority != "" {
			t.Errorf("cluster %s: expected certificate authority to be cleared", name)
		}
	}
}

func TestMakeKubeconfigInsecureRejectsGarbage(t *testing.T) {
	if _, err := MakeKubeconfigInsecure([]byte("not a kubeconfig at all: [")); err == nil {
		t.Error("expected an error for malformed input")
	}
}
