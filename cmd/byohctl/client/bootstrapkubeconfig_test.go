// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	testProjectID   = "0123456789abcdef"
	testIDNamespace = "127-01234567"
	testLegacyName  = "127-test-domain-test-tenant"

	idRoot     = "/oidc-proxy/" + testProjectID + "/region/"
	legacyRoot = "/oidc-proxy/" + testLegacyName + "/region/"

	bkcCollection = "/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/" + testIDNamespace + "/bootstrapkubeconfigs"
)

// proxyReply is the canned response of the fake oidc-proxy for one request path.
type proxyReply struct {
	status int
	body   string
	// drop closes the connection without answering, to simulate a transport error.
	drop bool
}

// recordedRequest is a request the fake oidc-proxy saw.
type recordedRequest struct {
	method string
	path   string
	body   string
}

// fakeProxy is an httptest oidc-proxy that answers from a table and records what it saw.
type fakeProxy struct {
	mu       sync.Mutex
	replies  map[string][]proxyReply
	requests []recordedRequest
}

// newFakeProxy starts the proxy and returns a client pointed at it. Replies are keyed by
// "METHOD path". A key with several replies answers them in order and repeats the last one.
// Unknown paths answer 404.
func newFakeProxy(t *testing.T, replies map[string][]proxyReply) (*K8sClient, *fakeProxy) {
	t.Helper()
	proxy := &fakeProxy{replies: replies}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		proxy.requests = append(proxy.requests, recordedRequest{method: r.Method, path: r.URL.Path, body: string(reqBody)})
		key := r.Method + " " + r.URL.Path
		queue := proxy.replies[key]
		if len(queue) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply := queue[0]
		if len(queue) > 1 {
			proxy.replies[key] = queue[1:]
		}
		if reply.drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(ts.Close)

	host := strings.TrimPrefix(ts.URL, "https://")
	client := NewK8sClient(host, "test-domain", "test-tenant", "test-token", "region", false)
	client.client = ts.Client()
	client.SetProjectID(testProjectID)
	return client, proxy
}

func (p *fakeProxy) seen() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

func one(status int, body string) []proxyReply {
	return []proxyReply{{status: status, body: body}}
}

func configMapBody(namespace string) string {
	if namespace == "" {
		return `{"metadata":{"name":"kube-root-ca.crt"}}`
	}
	return `{"metadata":{"name":"kube-root-ca.crt","namespace":"` + namespace + `"}}`
}

// resolvedClient returns a client whose tenant ref is already the ID-based one.
func resolvedClient(t *testing.T, replies map[string][]proxyReply) (*K8sClient, *fakeProxy) {
	t.Helper()
	replies["GET "+idRoot+"api/v1/namespaces/"+testIDNamespace+"/configmaps/kube-root-ca.crt"] = one(http.StatusOK, configMapBody(testIDNamespace))
	client, proxy := newFakeProxy(t, replies)
	err := client.ResolveTenantNamespace(context.Background())
	require.NoError(t, err)
	return client, proxy
}

func TestResolveTenantNamespace(t *testing.T) {
	const (
		idProbe     = "GET " + idRoot + "api/v1/namespaces/" + testIDNamespace + "/configmaps/kube-root-ca.crt"
		legacyProbe = "GET " + legacyRoot + "api/v1/namespaces/" + testLegacyName + "/configmaps/kube-root-ca.crt"
	)
	testCases := []struct {
		name          string
		replies       map[string][]proxyReply
		wantKey       string
		wantNamespace string
		wantErr       bool
	}{
		{
			name:          "ID path answers",
			replies:       map[string][]proxyReply{idProbe: one(http.StatusOK, configMapBody(testIDNamespace))},
			wantKey:       testProjectID,
			wantNamespace: testIDNamespace,
		},
		{
			name: "ID path forbidden falls back to legacy",
			replies: map[string][]proxyReply{
				idProbe:     one(http.StatusForbidden, ""),
				legacyProbe: one(http.StatusOK, configMapBody(testLegacyName)),
			},
			wantKey:       testLegacyName,
			wantNamespace: testLegacyName,
		},
		{
			name:          "namespace comes from the body, not the candidate",
			replies:       map[string][]proxyReply{idProbe: one(http.StatusOK, configMapBody(testLegacyName))},
			wantKey:       testProjectID,
			wantNamespace: testLegacyName,
		},
		{
			name:          "body without namespace uses the candidate",
			replies:       map[string][]proxyReply{idProbe: one(http.StatusOK, configMapBody(""))},
			wantKey:       testProjectID,
			wantNamespace: testIDNamespace,
		},
		{
			name:    "no candidate answers",
			replies: map[string][]proxyReply{},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newFakeProxy(t, tc.replies)

			err := client.ResolveTenantNamespace(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, client.namespace)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tenantRef{key: tc.wantKey, namespace: tc.wantNamespace}, client.resolvedRef())
			assert.Equal(t, tc.wantNamespace, client.getNamespace())
		})
	}
}

func TestCreateBootstrapKubeconfig(t *testing.T) {
	t.Run("posts the object without apiserver or CA", func(t *testing.T) {
		client, proxy := resolvedClient(t, map[string][]proxyReply{
			"POST " + idRoot + bkcCollection[1:]: one(http.StatusCreated, "{}"),
		})

		err := client.CreateBootstrapKubeconfig(context.Background(), "host-abcde", "host")
		require.NoError(t, err)

		requests := proxy.seen()
		post := requests[len(requests)-1]
		assert.Equal(t, "POST", post.method)
		assert.Equal(t, idRoot+bkcCollection[1:], post.path)
		var got infrastructurev1beta1.BootstrapKubeconfig
		require.NoError(t, json.Unmarshal([]byte(post.body), &got))
		want := infrastructurev1beta1.BootstrapKubeconfig{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				Kind:       "BootstrapKubeconfig",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "host-abcde",
				Namespace: testIDNamespace,
			},
			Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{HostName: "host"},
		}
		assert.Equal(t, want, got)
		assert.NotContains(t, post.body, "apiserver")
		assert.NotContains(t, post.body, "certificate-authority-data")
	})

	t.Run("error statuses", func(t *testing.T) {
		testCases := []struct {
			name       string
			status     int
			wantStatus int
		}{
			{name: "forbidden", status: http.StatusForbidden, wantStatus: 403},
			{name: "already exists", status: http.StatusConflict, wantStatus: 409},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				client, _ := resolvedClient(t, map[string][]proxyReply{
					"POST " + idRoot + bkcCollection[1:]: one(tc.status, "denied by test"),
				})

				err := client.CreateBootstrapKubeconfig(context.Background(), "host-abcde", "host")
				require.Error(t, err)
				var statusErr *StatusError
				require.ErrorAs(t, err, &statusErr)
				assert.Equal(t, &StatusError{Action: "create BootstrapKubeconfig host-abcde", StatusCode: tc.wantStatus, Body: "denied by test"}, statusErr)
				assert.Contains(t, err.Error(), "denied by test")
			})
		}
	})

	t.Run("before resolve", func(t *testing.T) {
		client, proxy := newFakeProxy(t, map[string][]proxyReply{})

		err := client.CreateBootstrapKubeconfig(context.Background(), "host-abcde", "host")
		require.Error(t, err)
		assert.Empty(t, proxy.seen())
	})
}

func TestWaitForCredentialSecretRef(t *testing.T) {
	getPath := idRoot + bkcCollection[1:] + "/host-abcde"
	getKey := "GET " + getPath
	unset := `{"status":{"tokenID":"abc"}}`
	set := `{"status":{"tokenID":"abc","credentialSecretRef":"host-abcde-cred"}}`

	t.Run("returns once the reference is set", func(t *testing.T) {
		client, proxy := resolvedClient(t, map[string][]proxyReply{
			getKey: {{status: 200, body: unset}, {status: 200, body: unset}, {status: 200, body: set}},
		})

		got, err := client.WaitForCredentialSecretRef(context.Background(), "host-abcde", time.Millisecond, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "host-abcde-cred", got)

		gets := proxy.seen()[1:]
		want := []recordedRequest{
			{method: "GET", path: getPath},
			{method: "GET", path: getPath},
			{method: "GET", path: getPath},
		}
		assert.Equal(t, want, gets)
	})

	t.Run("times out naming the object", func(t *testing.T) {
		client, _ := resolvedClient(t, map[string][]proxyReply{getKey: one(200, unset)})

		_, err := client.WaitForCredentialSecretRef(context.Background(), "host-abcde", time.Millisecond, 30*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host-abcde")
	})

	t.Run("transient failures are retried", func(t *testing.T) {
		testCases := []struct {
			name  string
			first proxyReply
		}{
			{name: "503 then success", first: proxyReply{status: 503, body: "unavailable"}},
			{name: "404 then success", first: proxyReply{status: 404, body: "not found"}},
			{name: "429 then success", first: proxyReply{status: 429, body: "slow down"}},
			{name: "transport error then success", first: proxyReply{drop: true}},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				client, _ := resolvedClient(t, map[string][]proxyReply{
					getKey: {tc.first, {status: 200, body: set}},
				})

				got, err := client.WaitForCredentialSecretRef(context.Background(), "host-abcde", time.Millisecond, 5*time.Second)
				require.NoError(t, err)
				assert.Equal(t, "host-abcde-cred", got)
			})
		}
	})

	t.Run("client errors fail at once", func(t *testing.T) {
		testCases := []struct {
			name   string
			status int
		}{
			{name: "400", status: 400},
			{name: "401", status: 401},
			{name: "403", status: 403},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				client, proxy := resolvedClient(t, map[string][]proxyReply{getKey: one(tc.status, "no")})

				_, err := client.WaitForCredentialSecretRef(context.Background(), "host-abcde", time.Millisecond, 5*time.Second)
				var statusErr *StatusError
				require.ErrorAs(t, err, &statusErr)
				assert.Equal(t, tc.status, statusErr.StatusCode)

				gets := proxy.seen()[1:]
				assert.Len(t, gets, 1, "a client error must not be retried")
			})
		}
	})

	t.Run("timeout reports the last error", func(t *testing.T) {
		client, _ := resolvedClient(t, map[string][]proxyReply{getKey: one(503, "still down")})

		_, err := client.WaitForCredentialSecretRef(context.Background(), "host-abcde", time.Millisecond, 50*time.Millisecond)
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "host-abcde")
		assert.Contains(t, err.Error(), "status 503")
		assert.Contains(t, err.Error(), "still down")
	})
}

func TestGetCredentialKubeconfig(t *testing.T) {
	secretPath := idRoot + "api/v1/namespaces/" + testIDNamespace + "/secrets/host-abcde-cred"

	t.Run("decodes the kubeconfig key", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("kubeconfig-bytes"))
		client, proxy := resolvedClient(t, map[string][]proxyReply{
			"GET " + secretPath: one(200, `{"data":{"kubeconfig":"`+encoded+`"}}`),
		})

		got, err := client.GetCredentialKubeconfig(context.Background(), "host-abcde-cred")
		require.NoError(t, err)
		assert.Equal(t, []byte("kubeconfig-bytes"), got)

		requests := proxy.seen()
		assert.Equal(t, recordedRequest{method: "GET", path: secretPath}, requests[len(requests)-1])
		assert.Len(t, requests, 2, "one probe and one named GET, no list")
	})

	t.Run("secret without the key", func(t *testing.T) {
		client, _ := resolvedClient(t, map[string][]proxyReply{
			"GET " + secretPath: one(200, `{"data":{"other":"eA=="}}`),
		})

		_, err := client.GetCredentialKubeconfig(context.Background(), "host-abcde-cred")
		require.Error(t, err)
	})

	t.Run("before resolve", func(t *testing.T) {
		client, proxy := newFakeProxy(t, map[string][]proxyReply{})

		_, err := client.GetCredentialKubeconfig(context.Background(), "host-abcde-cred")
		require.Error(t, err)
		assert.Empty(t, proxy.seen())
	})
}

func TestCheckRegionAvailabilityViaProxy(t *testing.T) {
	regionPath := "GET " + idRoot + "api/v1/namespaces/" + testIDNamespace + "/configmaps/region-config"
	testCases := []struct {
		name      string
		body      string
		status    int
		region    string
		wantFound bool
		wantKnown []string
		wantErr   bool
	}{
		{
			name:      "region found",
			status:    200,
			body:      `{"data":{"regions":"region\nother"}}`,
			region:    "region",
			wantFound: true,
		},
		{
			name:      "region not found lists the known ones",
			status:    200,
			body:      `{"data":{"regions":"one\ntwo"}}`,
			region:    "region",
			wantKnown: []string{"one", "two"},
		},
		{
			name:    "no regions key",
			status:  200,
			body:    `{"data":{}}`,
			region:  "region",
			wantErr: true,
		},
		{
			name:    "configmap unreadable",
			status:  403,
			region:  "region",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := resolvedClient(t, map[string][]proxyReply{regionPath: one(tc.status, tc.body)})

			found, known, err := client.CheckRegionAvailabilityViaProxy(context.Background(), tc.region)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantFound, found)
			assert.Equal(t, tc.wantKnown, known)
		})
	}
}

func TestParseRegions(t *testing.T) {
	testCases := []struct {
		name       string
		regionsStr string
		region     string
		wantFound  bool
		wantKnown  []string
	}{
		{
			name:       "found with surrounding spaces",
			regionsStr: "one\n region \n",
			region:     "region",
			wantFound:  true,
		},
		{
			name:       "not found",
			regionsStr: "one\ntwo",
			region:     "region",
			wantKnown:  []string{"one", "two"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			found, known := matchRegion(tc.regionsStr, tc.region)
			assert.Equal(t, tc.wantFound, found)
			assert.Equal(t, tc.wantKnown, known)
		})
	}
}
