// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newMapFuncTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, infrastructurev1beta1.AddToScheme(scheme))
	require.NoError(t, clusterv1.AddToScheme(scheme))
	return scheme
}

func TestClusterToByoMachines(t *testing.T) {
	const namespace = "default"
	scheme := newMapFuncTestScheme(t)

	matching := &infrastructurev1beta1.ByoMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "byomachine-in-cluster",
			Namespace: namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "cluster-a"},
		},
	}
	other := &infrastructurev1beta1.ByoMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "byomachine-in-other-cluster",
			Namespace: namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "cluster-b"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(matching, other).Build()
	r := &controllers.ByoMachineReconciler{Client: fakeClient}

	mapFunc := r.ClusterToByoMachines(logr.Discard())

	t.Run("returns requests only for ByoMachines labeled with the cluster", func(t *testing.T) {
		cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: namespace}}
		requests := mapFunc(cluster)
		require.Len(t, requests, 1)
		assert.Equal(t, "byomachine-in-cluster", requests[0].Name)
		assert.Equal(t, namespace, requests[0].Namespace)
	})

	t.Run("returns nothing for a cluster with no matching ByoMachines", func(t *testing.T) {
		cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-with-no-machines", Namespace: namespace}}
		assert.Empty(t, mapFunc(cluster))
	})

	t.Run("skips clusters with a deletion timestamp", func(t *testing.T) {
		now := metav1.Now()
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "cluster-a",
				Namespace:         namespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{"keep-for-test"},
			},
		}
		assert.Empty(t, mapFunc(cluster))
	})

	t.Run("returns nil for a non-Cluster object", func(t *testing.T) {
		assert.Nil(t, mapFunc(&corev1.Pod{}))
	})
}

func TestByoHostToByoMachineMapFunc(t *testing.T) {
	byoMachineGVK := infrastructurev1beta1.GroupVersion.WithKind("ByoMachine")
	mapFunc := controllers.ByoHostToByoMachineMapFunc(byoMachineGVK)

	t.Run("maps to the referenced ByoMachine when the GroupKind matches", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{
			Status: infrastructurev1beta1.ByoHostStatus{
				MachineRef: &corev1.ObjectReference{
					APIVersion: byoMachineGVK.GroupVersion().String(),
					Kind:       byoMachineGVK.Kind,
					Namespace:  "default",
					Name:       "referenced-byomachine",
				},
			},
		}
		requests := mapFunc(host)
		require.Len(t, requests, 1)
		assert.Equal(t, client.ObjectKey{Namespace: "default", Name: "referenced-byomachine"}, requests[0].NamespacedName)
	})

	t.Run("returns nil when MachineRef is not set", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{}
		assert.Nil(t, mapFunc(host))
	})

	t.Run("returns nil when MachineRef points at a different GroupKind", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{
			Status: infrastructurev1beta1.ByoHostStatus{
				MachineRef: &corev1.ObjectReference{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Machine",
					Namespace:  "default",
					Name:       "some-machine",
				},
			},
		}
		assert.Nil(t, mapFunc(host))
	})

	t.Run("returns nil for a non-ByoHost object", func(t *testing.T) {
		assert.Nil(t, mapFunc(&corev1.Pod{}))
	})
}
