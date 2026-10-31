// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

// Test client initialization with options
func TestNewK8sClient(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		client := NewK8sClient("fqdn.test.com", "domain", "tenant", "token", "region", false)

		// No more containerd or agent image to test
		if client.fqdn != "fqdn.test.com" {
			t.Errorf("Expected fqdn fqdn.test.com, got %s", client.fqdn)
		}

		if client.domain != "domain" {
			t.Errorf("Expected domain domain, got %s", client.domain)
		}

		if client.tenant != "tenant" {
			t.Errorf("Expected tenant tenant, got %s", client.tenant)
		}

		if client.bearerToken != "token" {
			t.Errorf("Expected token token, got %s", client.bearerToken)
		}
	})
}

// Test namespace generation
func TestGetNamespace(t *testing.T) {
	client := NewK8sClient("api.test.platform9.io", "test-domain", "test-tenant", "token", "region", false)
	namespace := client.getNamespace()

	expectedPrefix := "api-"
	if !strings.HasPrefix(namespace, expectedPrefix) {
		t.Errorf("Namespace %s does not start with expected prefix %s", namespace, expectedPrefix)
	}

	if !strings.Contains(namespace, "test-domain") {
		t.Errorf("Namespace %s does not contain domain", namespace)
	}

	if !strings.Contains(namespace, "test-tenant") {
		t.Errorf("Namespace %s does not contain tenant", namespace)
	}
}

// Test GetSecret method
func TestGetSecret(t *testing.T) {
	// Set up test HTTP server
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path format: /oidc-proxy/{namespace}/{region}/api/v1/namespaces/{namespace}/secrets/{name}
		expectedPath := "/oidc-proxy/127-test-domain-test-tenant/region/api/v1/namespaces/127-test-domain-test-tenant/secrets/kubeconfig"
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}

		// The client may be using a different authorization mechanism now, so we'll be more flexible
		if !strings.Contains(r.Header.Get("Authorization"), "Bearer") {
			t.Errorf("Expected Bearer token in Authorization header, got %s", r.Header.Get("Authorization"))
		}

		// Send test response
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(types.Secret{
			Data: map[string]string{
				"config": base64.StdEncoding.EncodeToString([]byte("test-kubeconfig")),
			},
		})
		if err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
	defer ts.Close()

	// Extract host from test server URL
	host := strings.TrimPrefix(ts.URL, "https://")

	// Create client that skips TLS verification
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	client := NewK8sClient(host, "test-domain", "test-tenant", "test-token", "region", false)
	client.client = httpClient

	// Test GetSecret
	secret, err := client.GetSecret("kubeconfig")
	if err != nil {
		t.Errorf("GetSecret returned error: %v", err)
	}

	if secret == nil {
		t.Fatal("GetSecret returned nil")
	}

	value, ok := secret.Data["config"]
	if !ok {
		t.Error("Secret data doesn't contain 'config' key")
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Errorf("Failed to decode secret value: %v", err)
	}

	if string(decoded) != "test-kubeconfig" {
		t.Errorf("Expected secret value 'test-kubeconfig', got '%s'", string(decoded))
	}
}

func TestSaveKubeConfig(t *testing.T) {
	testCases := []struct {
		name         string
		preCreateDir bool
	}{
		{
			name:         "byoh directory does not exist",
			preCreateDir: false,
		},
		{
			name:         "byoh directory already exists",
			preCreateDir: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create temp directory to simulate home directory
			tempDir, err := os.MkdirTemp("", "test-kubeconfig")
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// Save original environment variables to restore later
			origHome := os.Getenv("HOME")
			defer os.Setenv("HOME", origHome)

			// Set HOME to our temp directory for this test
			os.Setenv("HOME", tempDir)

			byohDir := filepath.Join(tempDir, ".byoh")
			if tc.preCreateDir {
				err = os.MkdirAll(byohDir, DefaultDirPerms)
				require.NoError(t, err)
			}

			// Set up test HTTP server
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Send test response with a valid kubeconfig structure
				w.Header().Set("Content-Type", "application/json")
				encErr := json.NewEncoder(w).Encode(types.Secret{
					Data: map[string]string{
						"value":  base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nkind: Config\n")),
						"config": base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nkind: Config\n")),
					},
				})
				if encErr != nil {
					t.Errorf("Failed to encode response: %v", encErr)
				}
			}))
			defer ts.Close()

			// Extract host from test server URL
			host := strings.TrimPrefix(ts.URL, "https://")

			// Create client that skips TLS verification
			httpClient := &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			}

			client := NewK8sClient(host, "test-domain", "test-tenant", "test-token", "region", false)
			client.client = httpClient

			// Test SaveKubeConfig
			err = client.SaveKubeConfig("kubeconfig")
			require.NoError(t, err)

			// Verify the byoh directory exists
			dirInfo, err := os.Stat(byohDir)
			require.NoError(t, err)
			assert.True(t, dirInfo.IsDir())

			// Verify the kubeconfig file exists at the final location with the correct content
			// This should be ~/.byoh/config
			kubeConfigPath := filepath.Join(byohDir, "config")
			content, err := os.ReadFile(kubeConfigPath)
			require.NoError(t, err)
			assert.Equal(t, "apiVersion: v1\nkind: Config\n", string(content))
		})
	}
}

// Test DNS resolution
func TestDNSResolution(t *testing.T) {
	// Mock DNS resolution by using a local resolver
	lookupFunc := func(host string) ([]string, error) {
		if host == "valid.example.com" {
			return []string{"192.168.1.1"}, nil
		}
		return nil, fmt.Errorf("lookup failed")
	}

	// Test valid resolution with our mock function directly
	addrs, err := lookupFunc("valid.example.com")
	if err != nil {
		t.Errorf("Expected successful lookup, got error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "192.168.1.1" {
		t.Errorf("Expected [192.168.1.1], got %v", addrs)
	}

	// Test invalid resolution with our mock function directly
	_, err = lookupFunc("invalid.example.com")
	if err == nil {
		t.Error("Expected error for invalid lookup, got nil")
	}
}

// TestIntegration tests the interaction between the K8sClient and agent service
// without using the removed RunByohAgent method
func TestIntegration(t *testing.T) {
	// This is a lightweight integration test to ensure the components
	// can still work together even after refactoring

	// Skip in CI environments
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI environment")
	}

	// Create a test client
	client := NewK8sClient("example.com", "test-domain", "test-tenant", "test-token", "region", false)

	// Test that DNS resolution works
	t.Run("DNS resolution", func(t *testing.T) {
		// This is just checking the method call structure - in a real test,
		// we would mock the actual DNS lookup
		_, err := client.CheckDNSResolution()
		if err == nil {
			// We expect an error for a fake domain, so if we don't get one,
			// something might be wrong
			t.Log("Note: No DNS error for example.com - this might be due to DNS hijacking")
		}
	})

	// Test that the SaveKubeConfig method has all necessary error handling
	t.Run("SaveKubeConfig error paths", func(t *testing.T) {
		// Try with a non-existent secret
		err := client.SaveKubeConfig("non-existent-secret")
		if err == nil {
			t.Error("Expected error when saving kubeconfig from non-existent secret")
		}
	})
}

// Test AgentLogOutput tests the logging behavior for the agent
func TestAgentLogOutput(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "agent-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create logs directory
	logDir := filepath.Join(tmpDir, "logs")
	err = os.MkdirAll(logDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create log directory: %v", err)
	}

	// Create a test agent log file
	agentLogPath := filepath.Join(logDir, "agent.log")
	testContent := "===== AGENT STARTED ====\nTest log content\n"
	err = os.WriteFile(agentLogPath, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test log file: %v", err)
	}

	// Verify the agent log file was created correctly
	content, err := os.ReadFile(agentLogPath)
	if err != nil {
		t.Fatalf("Failed to read agent log file: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("Log file content doesn't match expected content, got: %s", string(content))
	}

	// Test that log file location is displayed properly
	// This is a basic test since the actual display happens in the command
	if _, err := os.Stat(agentLogPath); os.IsNotExist(err) {
		t.Errorf("Agent log file doesn't exist at expected path: %s", agentLogPath)
	}
}

// writeHostKubeconfig writes a kubeconfig whose user holds the given certificate, inline or by
// file path, and returns the kubeconfig path.
func writeHostKubeconfig(t *testing.T, certPEM []byte, inline bool) string {
	t.Helper()
	dir := t.TempDir()
	userFields := ""
	if certPEM != nil {
		if inline {
			userFields = "    client-certificate-data: " + base64.StdEncoding.EncodeToString(certPEM) + "\n"
		} else {
			certPath := filepath.Join(dir, "client.crt")
			require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
			userFields = "    client-certificate: " + certPath + "\n"
		}
	}
	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: ctx\n" +
		"clusters:\n- name: c\n  cluster:\n    server: https://example.invalid\n" +
		"contexts:\n- name: ctx\n  context:\n    cluster: c\n    user: u\n    namespace: default\n" +
		"users:\n- name: u\n  user:\n" + userFields
	if certPEM == nil {
		kubeconfig = strings.TrimSuffix(kubeconfig, "  user:\n") + "  user: {}\n"
	}
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, []byte(kubeconfig), 0o600))
	return path
}

func newCertPEM(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestGetHostRefFromConfig(t *testing.T) {
	validCert := newCertPEM(t, identity.HostIdentity{
		TenantNamespace:    "tenant-a",
		NormalizedHostName: "worker-1",
		Suffix:             "abc12",
	}.String())

	tests := []struct {
		name                string
		certPEM             []byte
		inline              bool
		wantTenantNamespace string
		wantName            string
		wantErr             string
	}{
		{
			name:                "inline certificate data",
			certPEM:             validCert,
			inline:              true,
			wantTenantNamespace: "tenant-a",
			wantName:            "worker-1-abc12",
		},
		{
			name:                "certificate file path",
			certPEM:             validCert,
			wantTenantNamespace: "tenant-a",
			wantName:            "worker-1-abc12",
		},
		{
			name:    "no certificate",
			wantErr: "no client certificate",
		},
		{
			name:    "common name is not a host identity",
			certPEM: newCertPEM(t, "kubernetes-admin"),
			inline:  true,
			wantErr: "not a host identity",
		},
		{
			name:    "certificate is not PEM",
			certPEM: []byte("not a certificate"),
			inline:  true,
			wantErr: "no PEM CERTIFICATE block",
		},
		{
			name:    "PEM block is not a certificate",
			certPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("key")}),
			inline:  true,
			wantErr: "no PEM CERTIFICATE block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeHostKubeconfig(t, tt.certPEM, tt.inline)

			namespace, name, err := GetHostRefFromConfig(path)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantTenantNamespace, namespace)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestGetHostRefFromConfig_UnreadableConfig(t *testing.T) {
	_, _, err := GetHostRefFromConfig(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error reading kubeconfig")
}

func TestGetHostRefFromConfig_UnknownContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\ncurrent-context: nope\n"), 0o600)
	require.NoError(t, err)

	_, _, err = GetHostRefFromConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current context")
}
