// cmd/byohctl/client/auth.go
package client

import (
    "encoding/json"
    "fmt"
    "io/ioutil"
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

func NewAuthClient(fqdn, clientToken string) *AuthClient {
    return &AuthClient{
        client:      &http.Client{Timeout: 30 * time.Second},
        fqdn:        fqdn,
        clientToken: clientToken,
    }
}

func (c *AuthClient) GetToken(username, password string) (string, error) {
    start := time.Now()
    defer utils.TrackTime(start, "Token retrieval")

    utils.LogInfo("Getting authentication token for user %s", username)
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
        return "", fmt.Errorf("failed to create authentication request: %v", err)
    }

    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    resp, err := c.client.Do(req)
    if err != nil {
        return "", fmt.Errorf("failed to authenticate: %v", err)
    }
    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        return "", fmt.Errorf("failed to read authentication response: %v", err)
    }

    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(body))
    }

    var tokenResp types.TokenResponse
    if err := json.Unmarshal(body, &tokenResp); err != nil {
        return "", fmt.Errorf("failed to parse authentication response: %v", err)
    }

    utils.LogSuccess("Successfully obtained authentication token")
    return tokenResp.IDToken, nil
}