// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"

	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/util/conditions"
)

// AssertByoHostConditionsTrue polls the named ByoHost until each of conditionTypes
// reports True. Unlike waiting for the workload cluster to come up, this exercises
// BYOH's own condition-reporting path end to end (agent heartbeat -> AgentConnected,
// agent bootstrap status -> K8sNodeBootstrapSucceeded/K8sComponentsInstallationSucceeded),
// which a passing ApplyClusterTemplateAndWait does not by itself confirm.
func AssertByoHostConditionsTrue(ctx context.Context, clusterProxy framework.ClusterProxy, namespace, hostName, specName string, conditionTypes ...clusterv1.ConditionType) {
	key := k8stypes.NamespacedName{Name: hostName, Namespace: namespace}
	byoHost := &infrastructurev1beta1.ByoHost{}

	for _, conditionType := range conditionTypes {
		conditionType := conditionType
		Eventually(func() bool {
			if err := clusterProxy.GetClient().Get(ctx, key, byoHost); err != nil {
				return false
			}
			return conditions.IsTrue(byoHost, conditionType)
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected ByoHost %s/%s to report condition %s=True", namespace, hostName, conditionType)
	}
}
