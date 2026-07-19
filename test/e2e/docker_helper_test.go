// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlPlaneEndpointIP(t *testing.T) {
	testCases := []struct {
		name         string
		subnet       string
		processIndex int
		want         string
	}{
		{
			name:         "process 1 gets the base offset",
			subnet:       "172.18.0.0/16",
			processIndex: 1,
			want:         "172.18.0.151",
		},
		{
			name:         "distinct concurrent processes get distinct IPs",
			subnet:       "172.18.0.0/16",
			processIndex: 2,
			want:         "172.18.0.152",
		},
		{
			name:         "process index offsets the last octet regardless of subnet size",
			subnet:       "10.0.0.0/24",
			processIndex: 7,
			want:         "10.0.0.157",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controlPlaneEndpointIP(tc.subnet, tc.processIndex)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestControlPlaneEndpointIPInvalidSubnet(t *testing.T) {
	_, err := controlPlaneEndpointIP("not-a-subnet", 1)
	require.Error(t, err)
}
