// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	v1beta2conditions "sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestByoClusterController_ReconcilesLegacyStatus(t *testing.T) {
	c := startIsolatedEnv(t)
	ns := newTestNamespace(t, c, "byocluster-legacy-status")
	cluster := builder.Cluster(ns, "legacy-cluster").Build()
	require.NoError(t, c.Create(t.Context(), cluster))
	infraCluster := builder.ByoCluster(ns, "legacy-cluster").WithOwnerCluster(cluster).Build()
	require.NoError(t, c.Create(t.Context(), infraCluster))

	storeLegacyStatus(t, c, infraCluster, "byoclusters.infrastructure.cluster.x-k8s.io", map[string]interface{}{
		"ready": true,
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "True", "lastTransitionTime": "2025-06-01T00:00:00Z"},
		},
	})

	r := controllers.ByoClusterReconciler{Client: c}
	_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(infraCluster)})
	require.NoError(t, err)

	updated := &infrastructurev1beta1.ByoCluster{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(infraCluster), updated))
	require.NotNil(t, updated.Status.Initialization)
	assert.Equal(t, ptr.To(true), updated.Status.Initialization.Provisioned)
	assert.True(t, updated.Status.Ready) //nolint:staticcheck // deprecated field is still written
	ready := v1beta2conditions.Get(updated, clusterv1.ReadyCondition)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, clusterv1.ProvisionedReason, ready.Reason)
	assertConditionsInV1Beta2Shape(t, c, updated)
}
