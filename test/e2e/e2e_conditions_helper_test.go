// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"

	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	conditions "sigs.k8s.io/cluster-api/util/conditions/deprecated/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// AssertInfrastructureProvisioned asserts that Cluster API saw the cluster's and every
// machine's infrastructure as provisioned. Under the v1beta2 provider contract it reads
// that only from ByoCluster/ByoMachine status.initialization.provisioned, so this catches
// BYOH declaring the contract without reporting the field Cluster API now relies on.
func AssertInfrastructureProvisioned(ctx context.Context, clusterProxy framework.ClusterProxy, cluster *clusterv1.Cluster) {
	c := clusterProxy.GetClient()
	Expect(c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
	Expect(ptr.Deref(cluster.Status.Initialization.InfrastructureProvisioned, false)).To(BeTrue(),
		"expected Cluster %s/%s to report status.initialization.infrastructureProvisioned=true", cluster.Namespace, cluster.Name)

	machines := &clusterv1.MachineList{}
	Expect(c.List(ctx, machines, client.InNamespace(cluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cluster.Name})).To(Succeed())
	Expect(machines.Items).NotTo(BeEmpty())
	for i := range machines.Items {
		m := &machines.Items[i]
		Expect(ptr.Deref(m.Status.Initialization.InfrastructureProvisioned, false)).To(BeTrue(),
			"expected Machine %s/%s to report status.initialization.infrastructureProvisioned=true", m.Namespace, m.Name)
	}
}
