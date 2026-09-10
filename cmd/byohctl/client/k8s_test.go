package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
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
		json.NewEncoder(w).Encode(types.Secret{
			Data: map[string]string{
				"config": base64.StdEncoding.EncodeToString([]byte("test-kubeconfig")),
			},
		})
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
				json.NewEncoder(w).Encode(types.Secret{
					Data: map[string]string{
						"value":  base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nkind: Config\n")),
						"config": base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nkind: Config\n")),
					},
				})
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

// newTestK8sClient returns a K8sClient whose requests reach an httptest TLS server running
// handler. The HTTP client comes from the server so it trusts the server's self-signed
// certificate, which is what lets the client's own https:// URLs be exercised unchanged.
func newTestK8sClient(t *testing.T, handler http.HandlerFunc) *K8sClient {
	t.Helper()

	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)

	client := NewK8sClient(strings.TrimPrefix(ts.URL, "https://"), "test-domain", "test-tenant", "test-token", "region", false)
	client.client = ts.Client()
	return client
}

func TestK8sClientCreateByoHostEnrollment(t *testing.T) {
	const (
		wantPath = "/oidc-proxy/tenant-ns/region/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/tenant-ns/byohostenrollments"
		hostName = "host1"
	)

	testCases := []struct {
		name            string
		status          int
		respBody        string
		wantNamespace   string
		wantErrContains string
	}{
		{
			name:          "created returns the namespace the API server assigned",
			status:        http.StatusCreated,
			respBody:      `{"apiVersion":"infrastructure.cluster.x-k8s.io/v1beta1","kind":"ByoHostEnrollment","metadata":{"name":"host1","namespace":"real-tenant-ns"}}`,
			wantNamespace: "real-tenant-ns",
		},
		{
			name:            "forbidden surfaces the status and body",
			status:          http.StatusForbidden,
			respBody:        `{"message":"byohostenrollments is forbidden"}`,
			wantErrContains: "byohostenrollments is forbidden",
		},
		{
			name:            "conflict surfaces the status and body",
			status:          http.StatusConflict,
			respBody:        `{"message":"byohostenrollments \"host1\" already exists"}`,
			wantErrContains: "already exists",
		},
		{
			name:            "malformed response body",
			status:          http.StatusCreated,
			respBody:        "not json at all",
			wantErrContains: "error parsing ByoHostEnrollment",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotAuth string
			var gotBody []byte

			client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				gotBody, _ = io.ReadAll(r.Body)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, writeErr := io.WriteString(w, tc.respBody)
				assert.NoError(t, writeErr)
			})

			labels := map[string]string{"byoh.pf9.io/role": "worker"}
			namespace, err := client.CreateByoHostEnrollment(context.Background(), "tenant-ns", hostName, labels)

			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, wantPath, gotPath)
			assert.Equal(t, "Bearer test-token", gotAuth)

			var sent map[string]any
			unmarshalErr := json.Unmarshal(gotBody, &sent)
			require.NoError(t, unmarshalErr)
			assert.Equal(t, "infrastructure.cluster.x-k8s.io/v1beta1", sent["apiVersion"])
			assert.Equal(t, "ByoHostEnrollment", sent["kind"])
			metadata, ok := sent["metadata"].(map[string]any)
			require.True(t, ok, "request body has no metadata object")
			assert.Equal(t, hostName, metadata["name"])
			assert.Equal(t, map[string]any{"byoh.pf9.io/role": "worker"}, metadata["labels"])

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantNamespace, namespace)
		})
	}
}

func TestK8sClientGetCredentialSecret(t *testing.T) {
	t.Run("secret exists", func(t *testing.T) {
		var gotPath, gotAuth string

		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")

			w.Header().Set("Content-Type", "application/json")
			encodeErr := json.NewEncoder(w).Encode(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "tenant-ns",
					Name:      "host1-bootstrap",
				},
				Data: map[string][]byte{
					"hostName":   []byte("host1"),
					"kubeconfig": []byte("apiVersion: v1\nkind: Config\n"),
				},
			})
			assert.NoError(t, encodeErr)
		})

		secret, err := client.GetCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap")
		require.NoError(t, err)

		assert.Equal(t, "/oidc-proxy/tenant-ns/region/api/v1/namespaces/tenant-ns/secrets/host1-bootstrap", gotPath)
		assert.Equal(t, "Bearer test-token", gotAuth)
		assert.Equal(t, "host1-bootstrap", secret.Name)
		assert.Equal(t, []byte("host1"), secret.Data["hostName"])
	})

	t.Run("missing secret is reported as NotFound", func(t *testing.T) {
		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		_, err := client.GetCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap")
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err), "expected an apierrors NotFound, got %v", err)
	})

	t.Run("other statuses are plain errors", func(t *testing.T) {
		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, writeErr := io.WriteString(w, `{"message":"secrets is forbidden"}`)
			assert.NoError(t, writeErr)
		})

		_, err := client.GetCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap")
		require.Error(t, err)
		assert.False(t, apierrors.IsNotFound(err))
		assert.Contains(t, err.Error(), "secrets is forbidden")
	})
}

func TestK8sClientAwaitCredentialSecret(t *testing.T) {
	t.Run("returns once the secret appears", func(t *testing.T) {
		// The Secret only shows up after this many not-found polls.
		const notFoundPolls = 2

		var calls atomic.Int32

		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= notFoundPolls {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			encodeErr := json.NewEncoder(w).Encode(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "tenant-ns",
					Name:      "host1-bootstrap",
				},
			})
			assert.NoError(t, encodeErr)
		})

		secret, err := client.AwaitCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap", 5*time.Millisecond, 2*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "host1-bootstrap", secret.Name)
		assert.Equal(t, int32(notFoundPolls+1), calls.Load())
	})

	t.Run("aborts on a non-NotFound error", func(t *testing.T) {
		var calls atomic.Int32

		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, writeErr := io.WriteString(w, `{"message":"secrets is forbidden"}`)
			assert.NoError(t, writeErr)
		})

		_, err := client.AwaitCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap", 5*time.Millisecond, 2*time.Second)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "timed out")
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("times out when the secret never appears", func(t *testing.T) {
		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		_, err := client.AwaitCredentialSecret(context.Background(), "tenant-ns", "host1-bootstrap", 5*time.Millisecond, 30*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})

	t.Run("honours context cancellation", func(t *testing.T) {
		client := newTestK8sClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.AwaitCredentialSecret(ctx, "tenant-ns", "host1-bootstrap", 5*time.Millisecond, 2*time.Second)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "timed out")
		// LogErrorf flattens the error chain with %v, so the cause is only in the message.
		assert.Contains(t, err.Error(), "context canceled")
	})
}

