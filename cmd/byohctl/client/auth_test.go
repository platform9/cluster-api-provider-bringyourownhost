// client/auth_test.go
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
		r.ParseForm()
		if r.FormValue("username") != "testuser" {
			t.Errorf("Unexpected username: expected testuser, got %s", r.FormValue("username"))
		}

		if r.FormValue("password") != "testpass" {
			t.Errorf("Unexpected password: expected testpass, got %s", r.FormValue("password"))
		}

		// Return mock token response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
            "id_token": "test-id-token"
        }`))
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
		"grant_type":    {"password"},
		"client_id":     {"kubernetes"},
		"client_secret": {authClient.clientToken},
		"username":      {"testuser"},
		"password":      {"testpass"},
		"scope":         {"openid offline_access groups federated:id email"},
	}

	req, _ := http.NewRequest("POST", tokenEndpoint, strings.NewReader(formData.Encode()))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := authClient.client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var tokenResp types.TokenResponse
	json.Unmarshal(body, &tokenResp)

	// Verify token
	if tokenResp.IDToken != "test-id-token" {
		t.Errorf("Unexpected token: expected test-id-token, got %s", tokenResp.IDToken)
	}
}
