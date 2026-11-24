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
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
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

const (
	hcNamespace   = "default"
	hcClusterName = "test-cluster"
	hcMDName      = "md-0"
)

// hostCleanupFixture builds the Cluster/owner/Machine graphs that
// markHostForCleanup inspects to decide whether a ByoMachine delete keeps the
// host's kaapi.pf9.io/cluster-name(-cp) reservation label. Rollout cases are
// modeled for both maxSurge=0 and maxSurge>=1 object states, since the
// decision must not depend on the surge value. See KAAP-2639.
type hostCleanupFixture struct {
	scheme    *runtime.Scheme
	t0        time.Time
	deletedAt time.Time
}

func newHostCleanupFixture(t *testing.T) *hostCleanupFixture {
	s := runtime.NewScheme()
	require.NoError(t, infrav1.AddToScheme(s))
	require.NoError(t, clusterv1.AddToScheme(s))
	require.NoError(t, controlplanev1.AddToScheme(s))
	t0 := time.Now().Add(-time.Hour)
	return &hostCleanupFixture{scheme: s, t0: t0, deletedAt: t0.Add(30 * time.Minute)}
}

func (f *hostCleanupFixture) at(offset time.Duration) time.Time { return f.t0.Add(offset) }

func (f *hostCleanupFixture) cluster(deleting bool) *clusterv1.Cluster {
	c := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: hcClusterName, Namespace: hcNamespace}}
	if deleting {
		f.markDeleting(c)
	}
	return c
}

func (f *hostCleanupFixture) byoMachine(annotations map[string]string) *infrav1.ByoMachine {
	return &infrav1.ByoMachine{ObjectMeta: metav1.ObjectMeta{Name: "bm", Namespace: hcNamespace, Annotations: annotations}}
}

func (f *hostCleanupFixture) upgradeInProgress() map[string]string {
	return map[string]string{upgradeInProgressAnnotation: "true"}
}

func (f *hostCleanupFixture) markDeleting(obj client.Object) {
	obj.SetDeletionTimestamp(&metav1.Time{Time: f.deletedAt})
	obj.SetFinalizers([]string{"keep-for-test"})
}

func (f *hostCleanupFixture) kcp(replicas int32) *controlplanev1.KubeadmControlPlane {
	return &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "kcp", Namespace: hcNamespace, UID: types.UID("kcp-uid")},
		Spec:       controlplanev1.KubeadmControlPlaneSpec{Replicas: ptr.To(replicas)},
	}
}

func (f *hostCleanupFixture) md(revision string) *clusterv1.MachineDeployment {
	return &clusterv1.MachineDeployment{ObjectMeta: metav1.ObjectMeta{
		Name: hcMDName, Namespace: hcNamespace, UID: types.UID("md-uid"),
		Annotations: map[string]string{clusterv1.RevisionAnnotation: revision},
	}}
}

func (f *hostCleanupFixture) ms(name, revision string, replicas int32) *clusterv1.MachineSet {
	return &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: hcNamespace, UID: types.UID(name + "-uid"),
			Annotations: map[string]string{clusterv1.RevisionAnnotation: revision},
		},
		Spec: clusterv1.MachineSetSpec{Replicas: ptr.To(replicas)},
	}
}

// machine builds a Machine controlled by owner (a MachineSet or a
// KubeadmControlPlane), or a standalone Machine when owner is nil. An empty
// upToDate leaves the UpToDate condition unset.
func (f *hostCleanupFixture) machine(name string, created time.Time, owner client.Object, upToDate metav1.ConditionStatus) *clusterv1.Machine {
	m := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: hcNamespace, CreationTimestamp: metav1.NewTime(created),
		Labels: map[string]string{clusterv1.ClusterNameLabel: hcClusterName},
	}}
	switch o := owner.(type) {
	case *clusterv1.MachineSet:
		m.Labels[clusterv1.MachineDeploymentNameLabel] = hcMDName
		m.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineSet",
			Name: o.Name, UID: o.UID, Controller: ptr.To(true),
		}}
	case *controlplanev1.KubeadmControlPlane:
		m.Labels[clusterv1.MachineControlPlaneLabel] = ""
		m.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: controlplanev1.GroupVersion.String(), Kind: "KubeadmControlPlane",
			Name: o.Name, UID: o.UID, Controller: ptr.To(true),
		}}
	}
	if upToDate != "" {
		m.Status.Conditions = []metav1.Condition{{
			Type: clusterv1.MachineUpToDateCondition, Status: upToDate, Reason: "Test", LastTransitionTime: metav1.Now(),
		}}
	}
	return m
}

// deletingMachine is the Machine whose ByoMachine is being deleted.
func (f *hostCleanupFixture) deletingMachine(owner client.Object, upToDate metav1.ConditionStatus) *clusterv1.Machine {
	m := f.machine("m-deleting", f.t0, owner, upToDate)
	f.markDeleting(m)
	return m
}

// keepsLabel runs markHostForCleanup for machine and reports whether the
// persisted ByoHost still carries its cluster reservation label(s).
func (f *hostCleanupFixture) keepsLabel(t *testing.T, cluster *clusterv1.Cluster, byoMachine *infrav1.ByoMachine, machine *clusterv1.Machine, others ...client.Object) bool {
	t.Helper()
	_, cp := machine.Labels[clusterv1.MachineControlPlaneLabel]
	hostLabels := map[string]string{infrav1.ClusterLabel: hcClusterName}
	if cp {
		hostLabels[infrav1.ClusterLabelCP] = hcClusterName
	}
	host := &infrav1.ByoHost{ObjectMeta: metav1.ObjectMeta{Name: "host", Namespace: hcNamespace, Labels: hostLabels}}

	objs := append([]client.Object{host, cluster, byoMachine, machine}, others...)
	fakeClient := fake.NewClientBuilder().WithScheme(f.scheme).WithObjects(objs...).Build()
	r := &ByoMachineReconciler{Client: fakeClient}
	scope, err := newByoMachineScope(byoMachineScopeParams{
		Client: fakeClient, Cluster: cluster, Machine: machine,
		ByoCluster: &infrav1.ByoCluster{}, ByoMachine: byoMachine, ByoHost: host,
	})
	require.NoError(t, err)

	require.NoError(t, r.markHostForCleanup(context.Background(), scope))

	updated := &infrav1.ByoHost{}
	require.NoError(t, fakeClient.Get(context.Background(), client.ObjectKey{Name: host.Name, Namespace: hcNamespace}, updated))
	assert.Contains(t, updated.Annotations, infrav1.HostCleanupAnnotation)
	_, hasLabel := updated.Labels[infrav1.ClusterLabel]
	if cp {
		_, hasCPLabel := updated.Labels[infrav1.ClusterLabelCP]
		assert.Equal(t, hasLabel, hasCPLabel, "cluster-name and cluster-name-cp labels must be kept or stripped together")
	}
	return hasLabel
}

func TestByoMachineReconciler_markHostForCleanup_releasesHost(t *testing.T) {
	f := newHostCleanupFixture(t)

	t.Run("cluster delete strips the label even mid-upgrade with an outdated machine", func(t *testing.T) {
		kcp := f.kcp(3)
		machine := f.deletingMachine(kcp, metav1.ConditionFalse)

		assert.False(t, f.keepsLabel(t, f.cluster(true), f.byoMachine(f.upgradeInProgress()), machine, kcp))
	})

	t.Run("MachineDeployment delete strips the label", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 2)
		f.markDeleting(md)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms))
	})

	t.Run("MachineDeployment already gone strips the label", func(t *testing.T) {
		ms := f.ms("ms-1", "1", 2)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, ms))
	})

	t.Run("worker scale-down strips the label even when a newer sibling exists", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 2)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)
		older := f.machine("m-older", f.at(-time.Minute), ms, metav1.ConditionTrue)
		newer := f.machine("m-newer", f.at(time.Minute), ms, metav1.ConditionTrue)

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms, older, newer))
	})

	t.Run("control-plane scale-down strips the label when KCP deletes the oldest machine", func(t *testing.T) {
		kcp := f.kcp(1)
		machine := f.deletingMachine(kcp, metav1.ConditionTrue)
		newer := f.machine("m-newer", f.at(time.Minute), kcp, metav1.ConditionTrue)

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, kcp, newer))
	})

	t.Run("standalone machine with no owner strips the label", func(t *testing.T) {
		machine := f.deletingMachine(nil, "")

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine))
	})

	t.Run("owning MachineSet deleted strips the label", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 2)
		f.markDeleting(ms)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)

		assert.False(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms))
	})
}

func TestByoMachineReconciler_markHostForCleanup_keepsHostOnReplacement(t *testing.T) {
	f := newHostCleanupFixture(t)

	t.Run("KCP rollout with maxSurge=1", func(t *testing.T) {
		kcp := f.kcp(1)
		machine := f.deletingMachine(kcp, metav1.ConditionFalse)
		surged := f.machine("m-new", f.at(10*time.Minute), kcp, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, kcp, surged))
	})

	t.Run("KCP rollout with maxSurge=0", func(t *testing.T) {
		kcp := f.kcp(3)
		machine := f.deletingMachine(kcp, metav1.ConditionFalse)
		old1 := f.machine("m-old-1", f.at(time.Minute), kcp, metav1.ConditionFalse)
		old2 := f.machine("m-old-2", f.at(2*time.Minute), kcp, metav1.ConditionFalse)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, kcp, old1, old2))
	})

	t.Run("MachineDeployment rollout with maxSurge>=1", func(t *testing.T) {
		md, oldMS, newMS := f.md("2"), f.ms("ms-old", "1", 0), f.ms("ms-new", "2", 1)
		machine := f.deletingMachine(oldMS, metav1.ConditionFalse)
		surged := f.machine("m-new", f.at(10*time.Minute), newMS, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, oldMS, newMS, surged))
	})

	t.Run("MachineDeployment rollout with maxSurge=0", func(t *testing.T) {
		// replicas=1, maxUnavailable=1: the old machine goes first, nothing else exists yet.
		md, oldMS, newMS := f.md("2"), f.ms("ms-old", "1", 0), f.ms("ms-new", "2", 0)
		machine := f.deletingMachine(oldMS, metav1.ConditionFalse)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, oldMS, newMS))
	})

	t.Run("MachineDeployment rollout with a stale UpToDate condition", func(t *testing.T) {
		md, oldMS := f.md("2"), f.ms("ms-old", "1", 1)
		machine := f.deletingMachine(oldMS, metav1.ConditionTrue)
		remaining := f.machine("m-old-2", f.at(time.Minute), oldMS, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, oldMS, remaining))
	})

	t.Run("MachineHealthCheck remediation", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 1)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)
		machine.Status.Conditions = append(machine.Status.Conditions,
			metav1.Condition{Type: clusterv1.MachineHealthCheckSucceededCondition, Status: metav1.ConditionFalse, Reason: "Unhealthy", LastTransitionTime: metav1.Now()},
			metav1.Condition{Type: clusterv1.MachineOwnerRemediatedCondition, Status: metav1.ConditionFalse, Reason: "RemediationInProgress", LastTransitionTime: metav1.Now()},
		)
		// Without the MHC signal, one live sibling at replicas=1 would read as a scale-down.
		other := f.machine("m-other", f.at(time.Minute), ms, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms, other))
	})

	t.Run("manual worker machine delete before the replacement exists", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 2)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)
		other := f.machine("m-other", f.at(-time.Minute), ms, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms, other))
	})

	t.Run("manual worker machine delete after the replacement exists", func(t *testing.T) {
		md, ms := f.md("1"), f.ms("ms-1", "1", 2)
		machine := f.deletingMachine(ms, metav1.ConditionTrue)
		other := f.machine("m-other", f.at(time.Minute), ms, metav1.ConditionTrue)
		replacement := f.machine("m-replacement", f.deletedAt.Add(time.Second), ms, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, md, ms, other, replacement))
	})

	t.Run("manual control-plane machine delete", func(t *testing.T) {
		kcp := f.kcp(3)
		machine := f.deletingMachine(kcp, metav1.ConditionTrue)
		other1 := f.machine("m-other-1", f.at(-2*time.Minute), kcp, metav1.ConditionTrue)
		other2 := f.machine("m-other-2", f.at(-time.Minute), kcp, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(nil), machine, kcp, other1, other2))
	})

	t.Run("upgrade-in-progress annotation", func(t *testing.T) {
		kcp := f.kcp(1)
		machine := f.deletingMachine(kcp, metav1.ConditionTrue)
		// Without the annotation, one live sibling at replicas=1 would read as a scale-down.
		other := f.machine("m-other", f.at(time.Minute), kcp, metav1.ConditionTrue)

		assert.True(t, f.keepsLabel(t, f.cluster(false), f.byoMachine(f.upgradeInProgress()), machine, kcp, other))
	})
}
