// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
)

func TestNewAuthClient(t *testing.T) {
	// Use HTTP server instead of HTTPS
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a POST request to /dex/token
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}

		if !strings.HasSuffix(r.URL.Path, "/dex/token") {
			t.Errorf("Unexpected path: %s", r.URL.Path)
		}

		// Parse form data
		err := r.ParseForm()
		if err != nil {
			t.Errorf("Failed to parse form: %v", err)
		}
		if r.FormValue("username") != "testuser" {
			t.Errorf("Unexpected username: expected testuser, got %s", r.FormValue("username"))
		}

		if r.FormValue(passwordKey) != "testpass" {
			t.Errorf("Unexpected password: expected testpass, got %s", r.FormValue(passwordKey))
		}

		// Return mock token response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err = w.Write([]byte(`{
            "id_token": "test-id-token"
        }`))
		if err != nil {
			t.Errorf("Failed to write response: %v", err)
		}
	}))
	defer server.Close()

	// Extract the server host
	serverHost := strings.TrimPrefix(server.URL, "http://")

	// Create our own custom HTTP client
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Create client with custom HTTP client
	authClient := &AuthClient{
		client:      httpClient,
		fqdn:        serverHost,
		clientToken: "test-client-token",
	}

	// Use HTTP explicitly for the test
	tokenEndpoint := fmt.Sprintf("http://%s/dex/token", authClient.fqdn)
	formData := url.Values{
		"grant_type":    {passwordKey},
		"client_id":     {"kubernetes"},
		"client_secret": {authClient.clientToken},
		"username":      {"testuser"},
		passwordKey:     {"testpass"},
		"scope":         {"openid groups federated:id email"},
	}

	//nolint:noctx // test request against a local httptest server
	req, _ := http.NewRequest("POST", tokenEndpoint, strings.NewReader(formData.Encode()))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := authClient.client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var tokenResp types.TokenResponse
	err = json.Unmarshal(body, &tokenResp)
	if err != nil {
		t.Fatalf("Failed to parse token response: %v", err)
	}

	// Verify token
	if tokenResp.IDToken != "test-id-token" {
		t.Errorf("Unexpected token: expected test-id-token, got %s", tokenResp.IDToken)
	}
}

const wantScope = "openid groups federated:id email"

// tokenServer is a TLS test server standing in for dex's /dex/token endpoint. Each request is
// recorded, and respond decides the reply for the 1-based request number.
type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	forms []url.Values
}

func newTokenServer(t *testing.T, respond func(n int, w http.ResponseWriter)) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form: %v", err)
		}
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		n := len(ts.forms)
		ts.mu.Unlock()
		respond(n, w)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) requests() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

func (ts *tokenServer) fqdn() string { return strings.TrimPrefix(ts.URL, "https://") }

func (ts *tokenServer) client() *AuthClient {
	return NewAuthClient(ts.fqdn(), "client-secret", true)
}

// recordSleeps replaces authSleep for the duration of the test and returns the requested waits.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var sleeps []time.Duration
	orig := authSleep
	authSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	t.Cleanup(func() { authSleep = orig })
	return &sleeps
}

func writeToken(w http.ResponseWriter, token string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id_token":"` + token + `"}`))
}

func writeStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestGetTokenSendsScopeWithoutOfflineAccess(t *testing.T) {
	recordSleeps(t)
	ts := newTokenServer(t, func(_ int, w http.ResponseWriter) { writeToken(w, "tok") })

	token, err := ts.client().GetToken("user", "pass")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if token != "tok" {
		t.Errorf("expected token tok, got %q", token)
	}
	if got := ts.forms[0].Get("scope"); got != wantScope {
		t.Errorf("scope = %q, want %q", got, wantScope)
	}
	if strings.Contains(ts.forms[0].Get("scope"), "offline_access") {
		t.Error("scope must not request offline_access")
	}
}

func TestGetTokenRetriesServerErrorThenSucceeds(t *testing.T) {
	sleeps := recordSleeps(t)
	ts := newTokenServer(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeStatus(w, http.StatusInternalServerError, `{"error":"server_error"}`)
			return
		}
		writeToken(w, "recovered-token")
	})

	token, err := ts.client().GetToken("user", "pass")
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if token != "recovered-token" {
		t.Errorf("expected recovered-token, got %q", token)
	}
	if got := ts.requests(); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	// The request body is consumed by the first attempt, so the retry must rebuild it.
	if retry, first := ts.forms[1].Encode(), ts.forms[0].Encode(); retry != first {
		t.Errorf("retry form = %q, want %q", retry, first)
	}
	if len(*sleeps) != 1 {
		t.Errorf("expected 1 backoff sleep, got %v", *sleeps)
	}
}

func TestGetTokenGivesUpAfterMaxAttemptsOnPersistentServerError(t *testing.T) {
	sleeps := recordSleeps(t)
	ts := newTokenServer(t, func(_ int, w http.ResponseWriter) {
		writeStatus(w, http.StatusInternalServerError, `{"error":"server_error"}`)
	})

	token, err := ts.client().GetToken("user", "pass")
	if err == nil {
		t.Fatalf("expected an error, got token %q", token)
	}
	want := `authentication failed with status 500: {"error":"server_error"}`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := ts.requests(); got != authMaxAttempts {
		t.Errorf("server saw %d requests, want %d", got, authMaxAttempts)
	}
	if len(*sleeps) != authMaxAttempts-1 {
		t.Errorf("expected %d backoff sleeps, got %v", authMaxAttempts-1, *sleeps)
	}
}

func TestGetTokenDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sleeps := recordSleeps(t)
			body := `{"error":"invalid_grant"}`
			ts := newTokenServer(t, func(_ int, w http.ResponseWriter) { writeStatus(w, status, body) })

			_, err := ts.client().GetToken("user", "wrong")
			if err == nil {
				t.Fatal("expected an error")
			}
			want := fmt.Sprintf("authentication failed with status %d: %s", status, body)
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
			if got := ts.requests(); got != 1 {
				t.Errorf("server saw %d requests, want exactly 1", got)
			}
			if len(*sleeps) != 0 {
				t.Errorf("expected no backoff, got %v", *sleeps)
			}
		})
	}
}

func TestGetTokenRetriesTransportErrors(t *testing.T) {
	sleeps := recordSleeps(t)
	ts := newTokenServer(t, func(_ int, w http.ResponseWriter) { writeToken(w, "unused") })
	client := ts.client()
	ts.Close() // connection refused from here on

	_, err := client.GetToken("user", "pass")
	if err == nil || !strings.Contains(err.Error(), "failed to authenticate") {
		t.Fatalf("expected a transport error, got %v", err)
	}
	if len(*sleeps) != authMaxAttempts-1 {
		t.Errorf("expected %d backoff sleeps (one per retry), got %v", authMaxAttempts-1, *sleeps)
	}
}
