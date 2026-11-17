// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

func TestByoMachineReconciler_setNodeProviderID(t *testing.T) {
	const (
		// The fake client looks Nodes up by the namespace in the key, but a
		// Node is cluster-scoped and stored with none, so the host must have
		// no namespace for the lookup to succeed here.
		namespace = ""
		hostName  = "byohost-ewjv98-qp4fx"
		// otherNodeName is the normalized hostname without the ByoHost suffix.
		otherNodeName = "byohost-ewjv98"
		hostIDRegex   = ProviderIDPrefix + hostName + "/.+"
	)

	newHost := func(name string) *infrav1.ByoHost {
		return &infrav1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}
	}
	newNode := func(name, providerID string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.NodeSpec{ProviderID: providerID},
		}
	}

	// newRemoteClient returns a workload cluster client holding the given Nodes.
	newRemoteClient := func(nodes ...*corev1.Node) client.Client {
		builder := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme)
		for _, node := range nodes {
			builder = builder.WithObjects(node)
		}
		return builder.Build()
	}
	// storedProviderID returns the ProviderID of the named Node as stored in
	// the workload cluster.
	storedProviderID := func(t *testing.T, remoteClient client.Client, name string) string {
		t.Helper()
		stored := &corev1.Node{}
		err := remoteClient.Get(context.Background(), client.ObjectKey{Name: name}, stored)
		require.NoError(t, err)
		return stored.Spec.ProviderID
	}

	t.Run("sets a new provider id on the Node named after the host", func(t *testing.T) {
		remoteClient := newRemoteClient(newNode(hostName, ""))
		reconciler := &ByoMachineReconciler{}

		got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

		require.NoError(t, err)
		stored := storedProviderID(t, remoteClient, hostName)
		assert.Regexp(t, hostIDRegex, stored)
		assert.Equal(t, stored, got)
	})

	t.Run("Node already has a provider id", func(t *testing.T) {
		t.Run("bound to this host returns the existing provider id", func(t *testing.T) {
			existingID := ProviderIDPrefix + hostName + "/abcde"
			remoteClient := newRemoteClient(newNode(hostName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

			require.NoError(t, err)
			assert.Equal(t, existingID, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, hostName))
		})

		t.Run("bound to another host returns an error and is not modified", func(t *testing.T) {
			existingID := ProviderIDPrefix + "another-host/abcde"
			remoteClient := newRemoteClient(newNode(hostName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

			require.EqualError(t, err, "invalid format for node.Spec.ProviderID")
			assert.Empty(t, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, hostName))
		})
	})

	t.Run("Node is not found", func(t *testing.T) {
		testCases := []struct {
			name  string
			nodes []*corev1.Node
		}{
			{
				name:  "node missing in the workload cluster",
				nodes: nil,
			},
			{
				name:  "only a node named after the hostname without the suffix exists",
				nodes: []*corev1.Node{newNode(otherNodeName, "")},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				remoteClient := newRemoteClient(tc.nodes...)
				reconciler := &ByoMachineReconciler{}

				got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
				assert.Empty(t, got)
			})
		}
	})

	t.Run("two ByoHosts reporting the same hostname never claim the same Node", func(t *testing.T) {
		const (
			firstHostName  = "worker-1-aaaaa"
			secondHostName = "worker-1-bbbbb"
		)
		remoteClient := newRemoteClient(newNode(firstHostName, ""), newNode(secondHostName, ""))
		reconciler := &ByoMachineReconciler{}

		firstID, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(firstHostName))
		require.NoError(t, err)
		secondID, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(secondHostName))
		require.NoError(t, err)

		assert.Regexp(t, ProviderIDPrefix+firstHostName+"/.+", firstID)
		assert.Regexp(t, ProviderIDPrefix+secondHostName+"/.+", secondID)
		assert.Equal(t, firstID, storedProviderID(t, remoteClient, firstHostName))
		assert.Equal(t, secondID, storedProviderID(t, remoteClient, secondHostName))
	})

	t.Run("stale Node of an old ByoHost does not block a re-onboarded host", func(t *testing.T) {
		const (
			oldHostName = "worker-1-old01"
			newHostName = "worker-1-new02"
		)
		staleID := ProviderIDPrefix + oldHostName + "/abcde"
		remoteClient := newRemoteClient(newNode(oldHostName, staleID))
		reconciler := &ByoMachineReconciler{}

		// The new host registered no Node yet, so it must not take the stale one.
		got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(newHostName))
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
		assert.Empty(t, got)
		assert.Equal(t, staleID, storedProviderID(t, remoteClient, oldHostName))

		// Once the new host registers its own Node, it gets its own provider id.
		err = remoteClient.Create(context.Background(), newNode(newHostName, ""))
		require.NoError(t, err)

		got, err = reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(newHostName))
		require.NoError(t, err)
		assert.Regexp(t, ProviderIDPrefix+newHostName+"/.+", got)
		assert.Equal(t, got, storedProviderID(t, remoteClient, newHostName))
		assert.Equal(t, staleID, storedProviderID(t, remoteClient, oldHostName))
	})
}

// TestByoMachineReconciler_markHostForCleanup covers isRolloutReplacement's
// decision on whether a ByoMachine delete should strip the host's
// kaapi.pf9.io/cluster-name(-cp) reservation label (a real release) or keep
// it (a rollout replacing the machine) -- see KAAP-2639.
func TestByoMachineReconciler_markHostForCleanup(t *testing.T) {
	const (
		namespace   = "default"
		clusterName = "test-cluster"
	)

	testScheme := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(testScheme))
	require.NoError(t, clusterv1.AddToScheme(testScheme))

	t0 := time.Now().Add(-time.Hour)

	newHost := func(name string, cpLabel bool) *infrav1.ByoHost {
		hostLabels := map[string]string{infrav1.ClusterLabel: clusterName}
		if cpLabel {
			hostLabels[infrav1.ClusterLabelCP] = clusterName
		}
		return &infrav1.ByoHost{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: hostLabels}}
	}
	newCluster := func(deleting bool) *clusterv1.Cluster {
		c := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace}}
		if deleting {
			now := metav1.Now()
			c.DeletionTimestamp = &now
			c.Finalizers = []string{"keep-for-test"}
		}
		return c
	}
	newByoMachine := func(name string, byoMachineAnnotations map[string]string) *infrav1.ByoMachine {
		return &infrav1.ByoMachine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: byoMachineAnnotations}}
	}
	newMachine := func(name string, created time.Time, machineLabels map[string]string) *clusterv1.Machine {
		return &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         namespace,
				CreationTimestamp: metav1.NewTime(created),
				Labels:            machineLabels,
			},
		}
	}
	markDeleting := func(m *clusterv1.Machine) *clusterv1.Machine {
		now := metav1.Now()
		m.DeletionTimestamp = &now
		m.Finalizers = []string{"keep-for-test"}
		return m
	}

	// run calls markHostForCleanup for the given host/cluster/byoMachine/machine
	// (plus any extra sibling Machines) and returns the ByoHost as persisted.
	run := func(t *testing.T, host *infrav1.ByoHost, cluster *clusterv1.Cluster, byoMachine *infrav1.ByoMachine, machine *clusterv1.Machine, siblings ...*clusterv1.Machine) *infrav1.ByoHost {
		t.Helper()
		objs := []client.Object{host, cluster, byoMachine, machine}
		for _, sibling := range siblings {
			objs = append(objs, sibling)
		}
		fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).Build()
		r := &ByoMachineReconciler{Client: fakeClient}
		scope, err := newByoMachineScope(byoMachineScopeParams{
			Client:     fakeClient,
			Cluster:    cluster,
			Machine:    machine,
			ByoCluster: &infrav1.ByoCluster{},
			ByoMachine: byoMachine,
			ByoHost:    host,
		})
		require.NoError(t, err)

		require.NoError(t, r.markHostForCleanup(context.Background(), scope))

		updated := &infrav1.ByoHost{}
		require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: host.Name, Namespace: namespace}, updated))
		return updated
	}

	t.Run("real release: no rollout signal present strips both cluster labels", func(t *testing.T) {
		host := newHost("host-1", true)
		machine := newMachine("m-1", t0, map[string]string{clusterv1.MachineControlPlaneLabel: "", clusterv1.ClusterNameLabel: clusterName})

		updated := run(t, host, newCluster(false), newByoMachine("bm-1", nil), machine)

		assert.NotContains(t, updated.Labels, infrav1.ClusterLabel)
		assert.NotContains(t, updated.Labels, infrav1.ClusterLabelCP)
	})

	t.Run("rollout replacement: a newer control-plane sibling exists, label is preserved", func(t *testing.T) {
		host := newHost("host-2", true)
		machine := newMachine("m-2", t0, map[string]string{clusterv1.MachineControlPlaneLabel: "", clusterv1.ClusterNameLabel: clusterName})
		newerSibling := newMachine("m-2-new", t0.Add(time.Minute), map[string]string{clusterv1.MachineControlPlaneLabel: "", clusterv1.ClusterNameLabel: clusterName})

		updated := run(t, host, newCluster(false), newByoMachine("bm-2", nil), machine, newerSibling)

		assert.Equal(t, clusterName, updated.Labels[infrav1.ClusterLabel])
		assert.Equal(t, clusterName, updated.Labels[infrav1.ClusterLabelCP])
	})

	t.Run("rollout replacement: a newer worker sibling in the same MachineDeployment exists, label is preserved", func(t *testing.T) {
		host := newHost("host-3", false)
		machine := newMachine("m-3", t0, map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})
		newerSibling := newMachine("m-3-new", t0.Add(time.Minute), map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})

		updated := run(t, host, newCluster(false), newByoMachine("bm-3", nil), machine, newerSibling)

		assert.Equal(t, clusterName, updated.Labels[infrav1.ClusterLabel])
	})

	t.Run("scale-down: only an older sibling exists, label is stripped", func(t *testing.T) {
		host := newHost("host-4", false)
		machine := newMachine("m-4", t0, map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})
		olderSibling := newMachine("m-4-old", t0.Add(-time.Minute), map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})

		updated := run(t, host, newCluster(false), newByoMachine("bm-4", nil), machine, olderSibling)

		assert.NotContains(t, updated.Labels, infrav1.ClusterLabel)
	})

	t.Run("scale-down: the only newer sibling is itself being deleted, label is stripped", func(t *testing.T) {
		host := newHost("host-5", false)
		machine := newMachine("m-5", t0, map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})
		deletingNewerSibling := markDeleting(newMachine("m-5-new", t0.Add(time.Minute), map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"}))

		updated := run(t, host, newCluster(false), newByoMachine("bm-5", nil), machine, deletingNewerSibling)

		assert.NotContains(t, updated.Labels, infrav1.ClusterLabel)
	})

	t.Run("MachineHealthCheck remediation in progress preserves the label even with no newer sibling", func(t *testing.T) {
		host := newHost("host-6", false)
		machine := newMachine("m-6", t0, map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})
		machine.Status.Conditions = []metav1.Condition{
			{Type: clusterv1.MachineHealthCheckSucceededCondition, Status: metav1.ConditionFalse, Reason: "Unhealthy", LastTransitionTime: metav1.Now()},
			{Type: clusterv1.MachineOwnerRemediatedCondition, Status: metav1.ConditionFalse, Reason: "RemediationInProgress", LastTransitionTime: metav1.Now()},
		}

		updated := run(t, host, newCluster(false), newByoMachine("bm-6", nil), machine)

		assert.Equal(t, clusterName, updated.Labels[infrav1.ClusterLabel])
	})

	t.Run("explicit upgrade-in-progress annotation preserves the label even with no newer sibling", func(t *testing.T) {
		host := newHost("host-7", true)
		machine := newMachine("m-7", t0, map[string]string{clusterv1.MachineControlPlaneLabel: "", clusterv1.ClusterNameLabel: clusterName})
		byoMachine := newByoMachine("bm-7", map[string]string{"barista.platform9.io/upgrade-in-progress": "true"})

		updated := run(t, host, newCluster(false), byoMachine, machine)

		assert.Equal(t, clusterName, updated.Labels[infrav1.ClusterLabelCP])
	})

	t.Run("cluster delete overrides a coincidentally newer sibling: label is stripped", func(t *testing.T) {
		host := newHost("host-8", false)
		machine := newMachine("m-8", t0, map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})
		newerSibling := newMachine("m-8-new", t0.Add(time.Minute), map[string]string{clusterv1.MachineDeploymentNameLabel: "md-0"})

		updated := run(t, host, newCluster(true), newByoMachine("bm-8", nil), machine, newerSibling)

		assert.NotContains(t, updated.Labels, infrav1.ClusterLabel)
	})
}
