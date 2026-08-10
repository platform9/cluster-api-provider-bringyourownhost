// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package feature_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/feature"
	"k8s.io/component-base/featuregate"
)

func TestFeature(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Feature Suite")
}

var _ = Describe("Feature gates", func() {
	It("exposes Gates as the read-only view of MutableGates", func() {
		Expect(feature.Gates).To(BeIdenticalTo(feature.MutableGates))
	})

	It("registers a new gate and reflects its default through both Gates and MutableGates", func() {
		const testFeature featuregate.Feature = "BYOHTestFeatureGate"

		err := feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
			testFeature: {Default: false, PreRelease: featuregate.Alpha},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(feature.Gates.Enabled(testFeature)).To(BeFalse())
		Expect(feature.MutableGates.Enabled(testFeature)).To(BeFalse())
	})

	It("propagates Set() through MutableGates to Gates.Enabled()", func() {
		const testFeature featuregate.Feature = "BYOHTestFeatureGateToggle"

		Expect(feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
			testFeature: {Default: false, PreRelease: featuregate.Alpha},
		})).NotTo(HaveOccurred())

		Expect(feature.MutableGates.Set("BYOHTestFeatureGateToggle=true")).NotTo(HaveOccurred())
		Expect(feature.Gates.Enabled(testFeature)).To(BeTrue())
	})

	It("returns an error, not a panic, when re-adding a known gate with a conflicting spec", func() {
		const testFeature featuregate.Feature = "BYOHTestFeatureGateConflict"

		Expect(feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
			testFeature: {Default: false, PreRelease: featuregate.Alpha},
		})).NotTo(HaveOccurred())

		err := feature.MutableGates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
			testFeature: {Default: true, PreRelease: featuregate.Beta},
		})
		Expect(err).To(HaveOccurred())
	})
})
