// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSharedSuiteData(t *testing.T) {
	testCases := []struct {
		name string
		data sharedSuiteData
	}{
		{
			name: "typical values",
			data: sharedSuiteData{
				artifactFolder:        "/tmp/artifacts",
				configPath:            "/tmp/e2e-config.yaml",
				clusterctlConfigPath:  "/tmp/artifacts/repository/clusterctl-config.yaml",
				kubeconfigPath:        "/tmp/kind-bootstrap.kubeconfig",
				clusterConName:        "test-ab12cd",
				pathToHostAgentBinary: "/tmp/agent-binary/byoh-hostagent",
			},
		},
		{
			name: "empty fields round-trip as empty strings",
			data: sharedSuiteData{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := formatSharedSuiteData(&tc.data)

			decoded, err := parseSharedSuiteData(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.data, decoded)
		})
	}
}

func TestParseSharedSuiteDataWrongFieldCount(t *testing.T) {
	_, err := parseSharedSuiteData([]byte("only,four,comma,fields"))
	require.Error(t, err)
}
