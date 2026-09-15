// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostIdentity(t *testing.T) {
	identity := HostIdentity("coke-worker-1", "x7k2p")

	assert.Equal(t, "byoh:host:coke-worker-1:x7k2p", identity)
}

func TestHostNameFromIdentity(t *testing.T) {
	testCases := []struct {
		name     string
		identity string
		want     string
		wantErr  string
	}{
		{
			name:     "valid identity",
			identity: "byoh:host:coke-worker-1:x7k2p",
			want:     "coke-worker-1",
		},
		{
			name:     "identity without the prefix is rejected",
			identity: "someone@example.com",
			wantErr:  "does not start with",
		},
		{
			name:     "identity without suffix is rejected",
			identity: "byoh:host:coke-worker-1",
			wantErr:  "is not of the form",
		},
		{
			name:     "identity with extra segments is rejected",
			identity: "byoh:host:coke-worker-1:x7k2p:extra",
			wantErr:  "is not of the form",
		},
		{
			name:     "empty host name is rejected",
			identity: "byoh:host::x7k2p",
			wantErr:  "empty host name",
		},
		{
			name:     "empty suffix is rejected",
			identity: "byoh:host:coke-worker-1:",
			wantErr:  "empty suffix",
		},
		{
			name:     "hostname has another host as substring (worker-1 vs worker-12)",
			identity: "byoh:host:coke-worker-12:x7k2p",
			want:     "coke-worker-12",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HostNameFromIdentity(tc.identity)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
