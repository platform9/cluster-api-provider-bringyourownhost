package client

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

type AuthClient struct {
	client      *http.Client
	fqdn        string
	clientToken string
}

// NewAuthClient creates an AuthClient. When rootCAs is non-nil the HTTP client trusts that
// pool in addition to (in place of, for this client) the default system roots; pass nil to
// use the default system trust store.
func NewAuthClient(fqdn, clientToken string, rootCAs *x509.CertPool) *AuthClient {
	return &AuthClient{
		client:      newHTTPClient(30*time.Second, rootCAs),
		fqdn:        fqdn,
		clientToken: clientToken,
	}
}

// newHTTPClient builds an *http.Client with the given timeout, applying a custom RootCAs
// pool when one is provided.
func newHTTPClient(timeout time.Duration, rootCAs *x509.CertPool) *http.Client {
	c := &http.Client{Timeout: timeout}
	if rootCAs != nil {
		c.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: rootCAs},
		}
	}
	return c
}

func (c *AuthClient) GetToken(username, password string) (string, error) {
	start := time.Now()
	defer utils.TrackTime(start, "Token retrieval")

	utils.LogDebug("Getting authentication token for user %s", username)
	tokenEndpoint := fmt.Sprintf("https://%s/dex/token", c.fqdn)
	formData := url.Values{
		"grant_type":    {"password"},
		"client_id":     {"kubernetes"},
		"client_secret": {c.clientToken},
		"username":      {username},
		"password":      {password},
		"scope":         {"openid offline_access groups federated:id email"},
	}

	req, err := http.NewRequest("POST", tokenEndpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", utils.LogErrorf("failed to create authentication request: %v", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", utils.LogErrorf("failed to authenticate: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", utils.LogErrorf("failed to read authentication response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", utils.LogErrorf("authentication failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp types.TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", utils.LogErrorf("failed to parse authentication response: %v", err)
	}

	utils.LogSuccess("Successfully obtained authentication token")
	return tokenResp.IDToken, nil
}
