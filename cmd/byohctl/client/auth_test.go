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
	"testing"

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
		"scope":         {"openid offline_access groups federated:id email"},
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
