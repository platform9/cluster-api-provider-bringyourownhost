// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
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

// passwordKey is the form field name and OAuth2 grant type for password auth.
const passwordKey = "password"

type AuthClient struct {
	client      *http.Client
	fqdn        string
	clientToken string
}

// NewAuthClient creates a client for the DU's token endpoint. When insecure is true,
// TLS certificate verification is skipped -- see NewDUHTTPClient.
func NewAuthClient(fqdn, clientToken string, insecure bool) *AuthClient {
	return &AuthClient{
		client:      NewDUHTTPClient(DefaultTimeout, insecure),
		fqdn:        fqdn,
		clientToken: clientToken,
	}
}

func (c *AuthClient) GetToken(username, password string) (string, error) {
	start := time.Now()
	defer utils.TrackTime(start, "Token retrieval")

	utils.LogDebug("Getting authentication token for user %s", username)
	tokenEndpoint := fmt.Sprintf("https://%s/dex/token", c.fqdn)
	formData := url.Values{
		"grant_type":    {passwordKey},
		"client_id":     {"kubernetes"},
		"client_secret": {c.clientToken},
		"username":      {username},
		passwordKey:     {password},
		"scope":         {"openid offline_access groups federated:id email"},
	}

	//nolint:noctx // GetToken takes no context; changing its signature is out of scope
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

// defaultDomainID is Keystone's Default domain, where Dex logs users in.
const defaultDomainID = "default"

// GetProjectID returns the Keystone project ID of tenant in domain via a project-scoped login.
func (c *AuthClient) GetProjectID(username, password, domain, tenant string) (string, error) {
	var body types.KeystoneAuthRequest
	body.Auth.Identity.Methods = []string{passwordKey}
	body.Auth.Identity.Password.User.Name = username
	body.Auth.Identity.Password.User.Domain.ID = defaultDomainID
	body.Auth.Identity.Password.User.Password = password
	body.Auth.Scope.Project.Name = tenant
	// --domain defaults to "default" but the domain is named "Default", so scope by ID.
	if strings.EqualFold(domain, defaultDomainID) {
		body.Auth.Scope.Project.Domain.ID = defaultDomainID
	} else {
		body.Auth.Scope.Project.Domain.Name = domain
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to build keystone request: %w", err)
	}

	tokenEndpoint := fmt.Sprintf("https://%s/keystone/v3/auth/tokens?nocatalog", c.fqdn)
	//nolint:noctx // matches GetToken, which takes no context either
	req, err := http.NewRequest("POST", tokenEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create keystone request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach keystone: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read keystone response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("keystone login to project %s/%s failed with status %d: %s",
			domain, tenant, resp.StatusCode, string(respBody))
	}

	var tokenResp types.KeystoneTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse keystone response: %w", err)
	}
	if tokenResp.Token.Project.ID == "" {
		return "", fmt.Errorf("keystone response has no project ID for %s/%s", domain, tenant)
	}
	return tokenResp.Token.Project.ID, nil
}
