// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	conditions "sigs.k8s.io/cluster-api/util/conditions/deprecated/v1beta1"

	infrav1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

func TestSyncInstallAnnotations(t *testing.T) {
	const attached = "default.cp-abc12"

	host := func(labels, annotations map[string]string) *infrav1.ByoHost {
		return &infrav1.ByoHost{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations}}
	}
	attachedLabels := map[string]string{infrav1.AttachedByoMachineLabel: attached}

	t.Run("records true conditions for the attached machine", func(t *testing.T) {
		h := host(attachedLabels, nil)
		conditions.MarkTrue(h, infrav1.K8sComponentsInstallationSucceeded)
		conditions.MarkTrue(h, infrav1.K8sNodeBootstrapSucceeded)

		syncInstallAnnotations(h)

		assert.Equal(t, attached, h.Annotations[infrav1.K8sInstalledForAnnotation])
		assert.Equal(t, attached, h.Annotations[infrav1.K8sBootstrappedForAnnotation])
	})

	t.Run("restores conditions dropped with status", func(t *testing.T) {
		// after clusterctl move: annotations kept, status empty
		h := host(attachedLabels, map[string]string{
			infrav1.K8sInstalledForAnnotation:    attached,
			infrav1.K8sBootstrappedForAnnotation: attached,
		})

		syncInstallAnnotations(h)

		assert.True(t, conditions.IsTrue(h, infrav1.K8sComponentsInstallationSucceeded))
		assert.True(t, conditions.IsTrue(h, infrav1.K8sNodeBootstrapSucceeded))
	})

	t.Run("restores bootstrap marked false while machineRef was unset", func(t *testing.T) {
		h := host(attachedLabels, map[string]string{infrav1.K8sBootstrappedForAnnotation: attached})
		conditions.MarkFalse(h, infrav1.K8sNodeBootstrapSucceeded, infrav1.WaitingForMachineRefReason, clusterv1.ConditionSeverityInfo, "")

		syncInstallAnnotations(h)

		assert.True(t, conditions.IsTrue(h, infrav1.K8sNodeBootstrapSucceeded))
	})

	t.Run("ignores a record for another machine", func(t *testing.T) {
		h := host(attachedLabels, map[string]string{
			infrav1.K8sInstalledForAnnotation:    "default.old-machine",
			infrav1.K8sBootstrappedForAnnotation: "default.old-machine",
		})

		syncInstallAnnotations(h)

		assert.False(t, conditions.IsTrue(h, infrav1.K8sComponentsInstallationSucceeded))
		assert.False(t, conditions.IsTrue(h, infrav1.K8sNodeBootstrapSucceeded))
	})

	t.Run("does nothing on a host that is not attached", func(t *testing.T) {
		h := host(nil, map[string]string{infrav1.K8sInstalledForAnnotation: attached})
		conditions.MarkTrue(h, infrav1.K8sNodeBootstrapSucceeded)

		syncInstallAnnotations(h)

		assert.False(t, conditions.IsTrue(h, infrav1.K8sComponentsInstallationSucceeded))
		assert.NotContains(t, h.Annotations, infrav1.K8sBootstrappedForAnnotation)
	})
}
