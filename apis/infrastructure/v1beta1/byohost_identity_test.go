// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// dottedHostName returns a valid DNS-1123 subdomain of the given length, made
// of short labels so that no label exceeds 63 characters.
func dottedHostName(length int) string {
	var b strings.Builder
	for i := 0; i < length; i++ {
		if i%10 == 9 {
			b.WriteByte('.')
			continue
		}
		b.WriteByte('a')
	}

	return b.String()
}

func TestParseHostIdentity(t *testing.T) {
	testCases := []struct {
		name         string
		identity     string
		wantTenant   string
		wantHostName string
		wantSuffix   string
		wantErr      string
	}{
		{
			name:         "valid identity",
			identity:     "byoh:host:default:host1-x7k2p",
			wantTenant:   "default",
			wantHostName: "host1",
			wantSuffix:   "x7k2p",
		},
		{
			name:         "host name that ends like a suffix is split from the right",
			identity:     "byoh:host:default:web-abcde-xyzwq",
			wantTenant:   "default",
			wantHostName: "web-abcde",
			wantSuffix:   "xyzwq",
		},
		{
			name:         "tenant with hyphens",
			identity:     "byoh:host:a-b-c:host1-x7k2p",
			wantTenant:   "a-b-c",
			wantHostName: "host1",
			wantSuffix:   "x7k2p",
		},
		{
			name:         "host name with dots",
			identity:     "byoh:host:default:node1.example.com-x7k2p",
			wantTenant:   "default",
			wantHostName: "node1.example.com",
			wantSuffix:   "x7k2p",
		},
		{
			name:         "suffix of digits only",
			identity:     "byoh:host:default:host1-12345",
			wantTenant:   "default",
			wantHostName: "host1",
			wantSuffix:   "12345",
		},
		{
			name:     "identity without the prefix",
			identity: "someone@example.com",
			wantErr:  "does not start with",
		},
		{
			name:     "legacy form with only a host name",
			identity: "byoh:host:host1",
			wantErr:  "is not of the form",
		},
		{
			name:     "legacy form with a host name and a suffix",
			identity: "byoh:host:host1:x7k2p",
			wantErr:  `has a name "x7k2p" too short`,
		},
		{
			name:     "fifth segment",
			identity: "byoh:host:default:host1-x7k2p:extra",
			wantErr:  "is not of the form",
		},
		{
			name:     "empty tenant",
			identity: "byoh:host::host1-x7k2p",
			wantErr:  "invalid tenant",
		},
		{
			name:     "tenant with a dot",
			identity: "byoh:host:a.b:host1-x7k2p",
			wantErr:  "invalid tenant",
		},
		{
			name:     "uppercase tenant",
			identity: "byoh:host:Default:host1-x7k2p",
			wantErr:  "invalid tenant",
		},
		{
			name:     "empty name",
			identity: "byoh:host:default:",
			wantErr:  "empty name",
		},
		{
			name:     "name shorter than a hyphen and a suffix",
			identity: "byoh:host:default:abcde",
			wantErr:  "too short",
		},
		{
			name:     "no hyphen before the suffix",
			identity: "byoh:host:default:host1xabcde",
			wantErr:  "no hyphen before",
		},
		{
			name:     "suffix too short",
			identity: "byoh:host:default:host1-abcd",
			wantErr:  "no hyphen before",
		},
		{
			name:     "suffix too long",
			identity: "byoh:host:default:host1-abcdef",
			wantErr:  "no hyphen before",
		},
		{
			name:     "empty host name part",
			identity: "byoh:host:default:-abcde",
			wantErr:  "empty host name",
		},
		{
			name:     "uppercase letters in the host name",
			identity: "byoh:host:default:Host1-x7k2p",
			wantErr:  "invalid host name",
		},
		{
			name:     "uppercase letters in the suffix",
			identity: "byoh:host:default:host1-X7k2p",
			wantErr:  "invalid suffix",
		},
		{
			name:     "trailing dot in the host name",
			identity: "byoh:host:default:host1.-x7k2p",
			wantErr:  "invalid host name",
		},
		{
			name:     "symbol in the suffix",
			identity: "byoh:host:default:host1-x7k_p",
			wantErr:  "invalid suffix",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tenant, hostName, suffix, err := v1beta1.ParseHostIdentity(tc.identity)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Contains(t, err.Error(), tc.identity)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantTenant, tenant)
			assert.Equal(t, tc.wantHostName, hostName)
			assert.Equal(t, tc.wantSuffix, suffix)
		})
	}
}

func TestFormatHostIdentity(t *testing.T) {
	testCases := []struct {
		name     string
		tenant   string
		hostName string
		suffix   string
		want     string
		wantName string
	}{
		{
			name:     "simple host",
			tenant:   "default",
			hostName: "host1",
			suffix:   "x7k2p",
			want:     "byoh:host:default:host1-x7k2p",
			wantName: "host1-x7k2p",
		},
		{
			name:     "host name that ends like a suffix",
			tenant:   "a-b-c",
			hostName: "web-abcde",
			suffix:   "xyzwq",
			want:     "byoh:host:a-b-c:web-abcde-xyzwq",
			wantName: "web-abcde-xyzwq",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := v1beta1.FormatHostIdentity(tc.tenant, tc.hostName, tc.suffix)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantName, v1beta1.ByoHostName(tc.hostName, tc.suffix))

			tenant, hostName, suffix, err := v1beta1.ParseHostIdentity(got)
			require.NoError(t, err)
			assert.Equal(t, tc.tenant, tenant)
			assert.Equal(t, tc.hostName, hostName)
			assert.Equal(t, tc.suffix, suffix)
		})
	}
}

func TestParseHostIdentity_hostNameLength(t *testing.T) {
	const suffix = "x7k2p"

	testCases := []struct {
		name         string
		hostNameLen  int
		wantNameLen  int
		wantValid    bool
		wantParseErr string
	}{
		{
			name:        "name of 253 characters is valid",
			hostNameLen: 247,
			wantNameLen: 253,
			wantValid:   true,
		},
		{
			name:         "name of 254 characters is not valid",
			hostNameLen:  248,
			wantNameLen:  254,
			wantValid:    false,
			wantParseErr: "invalid name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hostName := dottedHostName(tc.hostNameLen)
			name := v1beta1.ByoHostName(hostName, suffix)
			require.Len(t, name, tc.wantNameLen)

			problems := validation.IsDNS1123Subdomain(name)
			if tc.wantValid {
				assert.Empty(t, problems)
			} else {
				assert.NotEmpty(t, problems)
			}

			identity := v1beta1.FormatHostIdentity("default", hostName, suffix)
			_, gotHostName, gotSuffix, err := v1beta1.ParseHostIdentity(identity)
			if !tc.wantValid {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantParseErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, hostName, gotHostName)
			assert.Equal(t, suffix, gotSuffix)
		})
	}
}

func TestParseHostIdentity_allProblems(t *testing.T) {
	testCases := []struct {
		name         string
		identity     string
		wantProblems []string
		wantErr      bool
	}{
		{
			name:         "invalid tenant, host name and suffix",
			identity:     "byoh:host:Default:Host1-X7k2p",
			wantProblems: []string{"invalid tenant", "invalid suffix", "invalid host name", "invalid name"},
			wantErr:      true,
		},
		{
			name:         "invalid tenant and short name",
			identity:     "byoh:host:a.b:abc",
			wantProblems: []string{"invalid tenant", "too short"},
			wantErr:      true,
		},
		{
			name:         "malformed identity stops at the first structural problem",
			identity:     "byoh:host:host1",
			wantProblems: []string{"is not of the form"},
			wantErr:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := v1beta1.ParseHostIdentity(tc.identity)
			require.Error(t, err)

			var identityErr *v1beta1.HostIdentityError
			require.ErrorAs(t, err, &identityErr)
			assert.Equal(t, tc.identity, identityErr.Identity)
			require.Len(t, identityErr.Problems, len(tc.wantProblems))

			for i, want := range tc.wantProblems {
				assert.Contains(t, identityErr.Problems[i].Error(), want)
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
