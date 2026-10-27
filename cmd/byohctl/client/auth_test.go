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

func TestGetProjectID(t *testing.T) {
	testCases := []struct {
		name          string
		domain        string
		status        int
		response      string
		wantDomainKey string
		wantDomainVal string
		wantID        string
		wantErr       bool
	}{
		{
			name:          "default domain is scoped by ID",
			domain:        "default",
			status:        http.StatusCreated,
			response:      `{"token": {"project": {"id": "0123456789abcdef"}}}`,
			wantDomainKey: "id",
			wantDomainVal: "default",
			wantID:        "0123456789abcdef",
		},
		{
			name:          "other domain is scoped by name",
			domain:        "Team_A",
			status:        http.StatusCreated,
			response:      `{"token": {"project": {"id": "fedcba9876543210"}}}`,
			wantDomainKey: "name",
			wantDomainVal: "Team_A",
			wantID:        "fedcba9876543210",
		},
		{
			name:          "keystone rejects the login",
			domain:        "default",
			status:        http.StatusUnauthorized,
			response:      `{"error": {"code": 401}}`,
			wantDomainKey: "id",
			wantDomainVal: "default",
			wantErr:       true,
		},
		{
			name:          "response has no project",
			domain:        "default",
			status:        http.StatusCreated,
			response:      `{"token": {}}`,
			wantDomainKey: "id",
			wantDomainVal: "default",
			wantErr:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/keystone/v3/auth/tokens" {
					t.Errorf("Unexpected path: %s", r.URL.Path)
				}
				var body struct {
					Auth struct {
						Identity struct {
							Password struct {
								User struct {
									Name     string            `json:"name"`
									Domain   map[string]string `json:"domain"`
									Password string            `json:"password"`
								} `json:"user"`
							} `json:"password"`
						} `json:"identity"`
						Scope struct {
							Project struct {
								Name   string            `json:"name"`
								Domain map[string]string `json:"domain"`
							} `json:"project"`
						} `json:"scope"`
					} `json:"auth"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("Failed to decode request: %v", err)
				}
				user := body.Auth.Identity.Password.User
				if user.Name != "testuser" || user.Password != "testpass" || user.Domain["id"] != "default" {
					t.Errorf("Unexpected user in request: %+v", user)
				}
				project := body.Auth.Scope.Project
				if project.Name != "service" || project.Domain[tc.wantDomainKey] != tc.wantDomainVal {
					t.Errorf("Unexpected project scope in request: %+v", project)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer ts.Close()

			authClient := NewAuthClient(strings.TrimPrefix(ts.URL, "https://"), "test-client-token", true)
			projectID, err := authClient.GetProjectID("testuser", "testpass", tc.domain, "service")
			if tc.wantErr {
				if err == nil {
					t.Errorf("Expected an error, got project ID %q", projectID)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetProjectID returned error: %v", err)
			}
			if projectID != tc.wantID {
				t.Errorf("Expected project ID %s, got %s", tc.wantID, projectID)
			}
		})
	}
}
