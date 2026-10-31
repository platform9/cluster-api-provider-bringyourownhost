// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
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

func TestParse(t *testing.T) {
	testCases := []struct {
		name     string
		identity string
		want     identity.HostIdentity
		wantErr  bool
		wantMsg  string
	}{
		{
			name:     "valid identity",
			identity: "byoh:host:default:host1-x7k2p",
			want: identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: "host1",
				Suffix:             "x7k2p",
			},
			wantErr: false,
		},
		{
			name:     "host name that ends like a suffix",
			identity: "byoh:host:default:web-abcde-xyzwq",
			want: identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: "web-abcde",
				Suffix:             "xyzwq",
			},
			wantErr: false,
		},
		{
			name:     "tenant namespace with hyphens",
			identity: "byoh:host:a-b-c:host1-x7k2p",
			want: identity.HostIdentity{
				TenantNamespace:    "a-b-c",
				NormalizedHostName: "host1",
				Suffix:             "x7k2p",
			},
			wantErr: false,
		},
		{
			name:     "host name with dots",
			identity: "byoh:host:default:node1.example.com-x7k2p",
			want: identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: "node1.example.com",
				Suffix:             "x7k2p",
			},
			wantErr: false,
		},
		{
			name:     "suffix of digits only",
			identity: "byoh:host:default:host1-12345",
			want: identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: "host1",
				Suffix:             "12345",
			},
			wantErr: false,
		},
		{
			name:     "identity without the prefix",
			identity: "someone@example.com",
			wantErr:  true,
			wantMsg:  "does not start with",
		},
		{
			name:     "legacy form with only a host name",
			identity: "byoh:host:host1",
			wantErr:  true,
			wantMsg:  "is not of the form",
		},
		{
			name:     "legacy form with a host name and a suffix",
			identity: "byoh:host:host1:x7k2p",
			wantErr:  true,
			wantMsg:  `has a name "x7k2p" too short`,
		},
		{
			name:     "fifth segment",
			identity: "byoh:host:default:host1-x7k2p:extra",
			wantErr:  true,
			wantMsg:  "is not of the form",
		},
		{
			name:     "empty tenant namespace",
			identity: "byoh:host::host1-x7k2p",
			wantErr:  true,
			wantMsg:  "invalid tenant",
		},
		{
			name:     "tenant namespace with a dot",
			identity: "byoh:host:a.b:host1-x7k2p",
			wantErr:  true,
			wantMsg:  "invalid tenant",
		},
		{
			name:     "uppercase tenant namespace",
			identity: "byoh:host:Default:host1-x7k2p",
			wantErr:  true,
			wantMsg:  "invalid tenant",
		},
		{
			name:     "empty name",
			identity: "byoh:host:default:",
			wantErr:  true,
			wantMsg:  "empty name",
		},
		{
			name:     "name shorter than a hyphen and a suffix",
			identity: "byoh:host:default:abcde",
			wantErr:  true,
			wantMsg:  "too short",
		},
		{
			name:     "no hyphen before the suffix",
			identity: "byoh:host:default:host1xabcde",
			wantErr:  true,
			wantMsg:  "no hyphen before",
		},
		{
			name:     "suffix too short",
			identity: "byoh:host:default:host1-abcd",
			wantErr:  true,
			wantMsg:  "no hyphen before",
		},
		{
			name:     "suffix too long",
			identity: "byoh:host:default:host1-abcdef",
			wantErr:  true,
			wantMsg:  "no hyphen before",
		},
		{
			name:     "empty host name part",
			identity: "byoh:host:default:-abcde",
			wantErr:  true,
			wantMsg:  "empty host name",
		},
		{
			name:     "uppercase letters in the host name",
			identity: "byoh:host:default:Host1-x7k2p",
			wantErr:  true,
			wantMsg:  "invalid host name",
		},
		{
			name:     "uppercase letters in the suffix",
			identity: "byoh:host:default:host1-X7k2p",
			wantErr:  true,
			wantMsg:  "invalid suffix",
		},
		{
			name:     "trailing dot in the host name",
			identity: "byoh:host:default:host1.-x7k2p",
			wantErr:  true,
			wantMsg:  "invalid host name",
		},
		{
			name:     "symbol in the suffix",
			identity: "byoh:host:default:host1-x7k_p",
			wantErr:  true,
			wantMsg:  "invalid suffix",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.Parse(tc.identity)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantMsg)
				assert.Contains(t, err.Error(), tc.identity)
				assert.Equal(t, identity.HostIdentity{}, got)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParse_hostNameLength(t *testing.T) {
	const suffix = "x7k2p"

	testCases := []struct {
		name         string
		hostNameLen  int
		wantNameLen  int
		wantValid    bool
		wantParseErr string
	}{
		{
			name:        "object name at the 253 character limit",
			hostNameLen: 247,
			wantNameLen: 253,
			wantValid:   true,
		},
		{
			name:         "object name one character over the limit",
			hostNameLen:  248,
			wantNameLen:  254,
			wantValid:    false,
			wantParseErr: "invalid name",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			want := identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: dottedHostName(tc.hostNameLen),
				Suffix:             suffix,
			}
			name := want.ByoHostName()
			require.Len(t, name, tc.wantNameLen)

			problems := validation.IsDNS1123Subdomain(name)
			if tc.wantValid {
				assert.Empty(t, problems)
			} else {
				assert.NotEmpty(t, problems)
			}

			got, err := identity.Parse(want.String())
			if !tc.wantValid {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantParseErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestParse_allProblems(t *testing.T) {
	testCases := []struct {
		name         string
		identity     string
		wantProblems []string
		wantErr      bool
	}{
		{
			name:         "invalid tenant namespace, host name and suffix",
			identity:     "byoh:host:Default:Host1-X7k2p",
			wantProblems: []string{"invalid tenant", "invalid suffix", "invalid host name", "invalid name"},
			wantErr:      true,
		},
		{
			name:         "invalid tenant namespace and short name",
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
			_, err := identity.Parse(tc.identity)
			require.Error(t, err)

			var identityErr *identity.HostIdentityError
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

func TestHostIdentity_String(t *testing.T) {
	testCases := []struct {
		name         string
		hostIdentity identity.HostIdentity
		want         string
		wantByoName  string
	}{
		{
			name: "simple host",
			hostIdentity: identity.HostIdentity{
				TenantNamespace:    "default",
				NormalizedHostName: "host1",
				Suffix:             "x7k2p",
			},
			want:        "byoh:host:default:host1-x7k2p",
			wantByoName: "host1-x7k2p",
		},
		{
			name: "host name that ends like a suffix",
			hostIdentity: identity.HostIdentity{
				TenantNamespace:    "a-b-c",
				NormalizedHostName: "web-abcde",
				Suffix:             "xyzwq",
			},
			want:        "byoh:host:a-b-c:web-abcde-xyzwq",
			wantByoName: "web-abcde-xyzwq",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.hostIdentity.String()
			assert.Equal(t, tc.want, got)

			byoHostName := tc.hostIdentity.ByoHostName()
			assert.Equal(t, tc.wantByoName, byoHostName)

			parsed, err := identity.Parse(got)
			require.NoError(t, err)
			assert.Equal(t, tc.hostIdentity, parsed)
		})
	}
}

func TestHasPrefix(t *testing.T) {
	testCases := []struct {
		name string
		s    string
		want bool
	}{
		{name: "valid host identity", s: "byoh:host:default:host1-x7k2p", want: true},
		{name: "legacy form with only a host name", s: "byoh:host:host1", want: true},
		{name: "prefix alone", s: identity.Prefix, want: true},
		{name: "email-like user", s: "someone@example.com", want: false},
		{name: "service account", s: "system:serviceaccount:default:byoh", want: false},
		{name: "prefix without the trailing colon", s: "byoh:host", want: false},
		{name: "prefix in the middle", s: "x-byoh:host:default:host1-x7k2p", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := identity.HasPrefix(tc.s)
			assert.Equal(t, tc.want, got)
		})
	}
}
