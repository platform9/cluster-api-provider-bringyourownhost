// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/types"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

// passwordKey is the form field name and OAuth2 grant type for password auth.
const passwordKey = "password"

// Token requests are retried on transport errors and HTTP 5xx, e.g. a DU briefly overloaded by
// a large batch of hosts onboarding at once.
const (
	authMaxAttempts = 4
	authBackoffBase = time.Second
)

// authSleep waits between token attempts; tests replace it so they never actually sleep.
var authSleep = time.Sleep

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
	// No offline_access: byohctl only needs the id_token, and a refresh token makes dex create a
	// per-user offline session that parallel onboardings race on.
	formData := url.Values{
		"grant_type":    {passwordKey},
		"client_id":     {"kubernetes"},
		"client_secret": {c.clientToken},
		"username":      {username},
		passwordKey:     {password},
		"scope":         {"openid groups federated:id email"},
	}
	encodedForm := formData.Encode()

	var lastErr error
	for attempt := 1; attempt <= authMaxAttempts; attempt++ {
		body, retryable, err := c.requestToken(tokenEndpoint, encodedForm)
		if err == nil {
			var tokenResp types.TokenResponse
			if unmarshalErr := json.Unmarshal(body, &tokenResp); unmarshalErr != nil {
				return "", utils.LogErrorf("failed to parse authentication response: %v", unmarshalErr)
			}
			utils.LogSuccess("Successfully obtained authentication token")
			return tokenResp.IDToken, nil
		}
		lastErr = err
		if !retryable || attempt == authMaxAttempts {
			break
		}
		delay := authBackoff(attempt)
		utils.LogInfo("Token request attempt %d/%d failed (%v); retrying in %s",
			attempt, authMaxAttempts, err, delay.Round(time.Millisecond))
		authSleep(delay)
	}
	return "", utils.LogErrorf("%v", lastErr)
}

// requestToken makes one token request. The request is built here, on every call, because its
// body reader is consumed by the first attempt. retryable reports whether a failure is worth
// another attempt: transport errors and 5xx are, 4xx and anything else are not.
func (c *AuthClient) requestToken(endpoint, encodedForm string) (body []byte, retryable bool, err error) {
	//nolint:noctx // GetToken takes no context; changing its signature is out of scope
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(encodedForm))
	if err != nil {
		return nil, false, fmt.Errorf("failed to create authentication request: %w", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("failed to authenticate: %w", err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("failed to read authentication response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode >= http.StatusInternalServerError,
			fmt.Errorf("authentication failed with status %d: %s", resp.StatusCode, string(body))
	}
	return body, false, nil
}

// authBackoff returns the wait after the given failed attempt (1-based). The ceiling doubles
// per attempt (1s, 2s, 4s) and the actual wait is random between half the ceiling and the
// ceiling, so hosts that failed together don't all retry at the same instant.
func authBackoff(attempt int) time.Duration {
	d := authBackoffBase << (attempt - 1)
	return d/2 + rand.N(d/2+1) // #nosec G404 -- retry jitter, not security sensitive
}
