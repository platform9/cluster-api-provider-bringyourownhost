// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// generateSafeLabelValue is unexported, so these tests live in the package
// itself rather than in the external test package.
package controllers //nolint: testpackage // exercises an unexported helper

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// expectedHashSuffix reproduces the hash half of generateSafeLabelValue's
// fallback so the test can assert against a value derived independently of
// the function under test, rather than restating its own output.
func expectedHashSuffix(t *testing.T, originalValue string) string {
	t.Helper()

	hasher := sha256.New()
	_, err := hasher.Write([]byte(originalValue))
	require.NoError(t, err)
	hashBytes := hasher.Sum(nil)

	return hex.EncodeToString(hashBytes)[:infrav1.LabelHashLength]
}

func TestGenerateSafeLabelValue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		namespace string
		hostname  string
	}{
		{
			name:      "short value is returned unchanged",
			namespace: "ns",
			hostname:  "test",
		},
		{
			name:      "value exactly at the length limit is returned unchanged",
			namespace: strings.Repeat("a", 30),
			hostname:  strings.Repeat("b", infrav1.MaxK8sLabelValueLength-30-1),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := tc.namespace + "." + tc.hostname
			require.LessOrEqual(t, len(want), infrav1.MaxK8sLabelValueLength)

			got := generateSafeLabelValue(tc.namespace, tc.hostname)
			assert.Equal(t, want, got)
		})
	}
}

func TestGenerateSafeLabelValue_OverLimit(t *testing.T) {
	t.Parallel()

	namespace := strings.Repeat("a", 30)
	hostname := strings.Repeat("b", infrav1.MaxK8sLabelValueLength-30) // one char past the limit
	originalValue := namespace + "." + hostname
	require.Greater(t, len(originalValue), infrav1.MaxK8sLabelValueLength)

	got := generateSafeLabelValue(namespace, hostname)

	require.Len(t, got, infrav1.MaxK8sLabelValueLength)

	wantPrefix := originalValue[:infrav1.MaxLabelPrefixLength]
	wantHash := expectedHashSuffix(t, originalValue)
	want := wantPrefix + infrav1.LabelSeparator + wantHash
	assert.Equal(t, want, got)
}

func TestGenerateSafeLabelValue_SharedPrefixDifferentHash(t *testing.T) {
	t.Parallel()

	// Both namespaces share the same first MaxLabelPrefixLength characters,
	// then diverge, so the results must share a truncated prefix but carry
	// different hash suffixes.
	commonPrefix := strings.Repeat("n", infrav1.MaxLabelPrefixLength)
	namespaceA := commonPrefix + "-suffix-a"
	namespaceB := commonPrefix + "-suffix-b"
	hostname := strings.Repeat("h", 20)

	gotA := generateSafeLabelValue(namespaceA, hostname)
	gotB := generateSafeLabelValue(namespaceB, hostname)

	require.Len(t, gotA, infrav1.MaxK8sLabelValueLength)
	require.Len(t, gotB, infrav1.MaxK8sLabelValueLength)

	prefixA := gotA[:infrav1.MaxLabelPrefixLength]
	prefixB := gotB[:infrav1.MaxLabelPrefixLength]
	assert.Equal(t, prefixA, prefixB, "both results should truncate to the same shared prefix")
	assert.NotEqual(t, gotA, gotB, "different inputs sharing a prefix must produce different hash suffixes")
}

func TestGenerateSafeLabelValue_Deterministic(t *testing.T) {
	t.Parallel()

	namespace := strings.Repeat("a", 40)
	hostname := strings.Repeat("b", 40)

	first := generateSafeLabelValue(namespace, hostname)
	second := generateSafeLabelValue(namespace, hostname)

	assert.Equal(t, first, second)
}
