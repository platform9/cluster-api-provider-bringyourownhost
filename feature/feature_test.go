// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package feature_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/feature"
	"k8s.io/component-base/featuregate"
)

func TestGatesIsReadOnlyViewOfMutableGates(t *testing.T) {
	assert.Same(t, feature.MutableGates, feature.Gates)
}

func TestRegisterNewGateReflectsDefaultThroughBothGatesAndMutableGates(t *testing.T) {
	const testFeature featuregate.Feature = "BYOHTestFeatureGate"

	err := feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		testFeature: {Default: false, PreRelease: featuregate.Alpha},
	})
	require.NoError(t, err)

	assert.False(t, feature.Gates.Enabled(testFeature))
	assert.False(t, feature.MutableGates.Enabled(testFeature))
}

func TestSetPropagatesThroughMutableGatesToGatesEnabled(t *testing.T) {
	const testFeature featuregate.Feature = "BYOHTestFeatureGateToggle"

	require.NoError(t, feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		testFeature: {Default: false, PreRelease: featuregate.Alpha},
	}))

	require.NoError(t, feature.MutableGates.Set("BYOHTestFeatureGateToggle=true"))
	assert.True(t, feature.Gates.Enabled(testFeature))
}

func TestReAddingKnownGateWithConflictingSpecReturnsErrorNotPanic(t *testing.T) {
	const testFeature featuregate.Feature = "BYOHTestFeatureGateConflict"

	require.NoError(t, feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		testFeature: {Default: false, PreRelease: featuregate.Alpha},
	}))

	err := feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		testFeature: {Default: true, PreRelease: featuregate.Beta},
	})
	assert.Error(t, err)
}
