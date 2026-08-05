// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	eventutils "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// newByoHostForTest creates a ByoHost in the API server and registers cleanup.
func newByoHostForTest(t *testing.T, name string) *infrastructurev1beta1.ByoHost {
	t.Helper()
	byoHost := builder.ByoHost(defaultNamespace, name).Build()
	require.NoError(t, k8sManager.GetClient().Create(context.Background(), byoHost))
	t.Cleanup(func() {
		_ = client.IgnoreNotFound(k8sManager.GetClient().Delete(context.Background(), byoHost))
	})
	return byoHost
}

func TestByohostController_UninstallSecretCleanup(t *testing.T) {
	byoHost := newByoHostForTest(t, "byohost-uninstall-test")
	byoHostLookupKey := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}

	uninstallSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-uninstall-secret",
			Namespace: byoHost.Namespace,
		},
		Data: map[string][]byte{"uninstall": []byte("echo uninstall")},
	}
	require.NoError(t, k8sManager.GetClient().Create(context.Background(), uninstallSecret))
	t.Cleanup(func() {
		_ = client.IgnoreNotFound(k8sManager.GetClient().Delete(context.Background(), uninstallSecret))
	})

	helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
	require.NoError(t, err)
	byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
		Name:      uninstallSecret.Name,
		Namespace: uninstallSecret.Namespace,
	}
	require.NoError(t, helper.Patch(context.Background(), byoHost))

	t.Run("deletes secret and clears ref when MachineRef is nil and no cleanup annotation", func(t *testing.T) {
		_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: byoHostLookupKey})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			err := k8sManager.GetClient().Get(context.Background(),
				types.NamespacedName{Name: uninstallSecret.Name, Namespace: uninstallSecret.Namespace},
				&corev1.Secret{})
			return apierrors.IsNotFound(err)
		}, 5*time.Second, 100*time.Millisecond, "uninstall secret should be deleted")

		updated := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), byoHostLookupKey, updated))
		assert.Nil(t, updated.Spec.UninstallationSecret)
	})
}

// setupHeartbeatReconciler sets HeartbeatTimeoutPeriod=3s and a fresh
// FakeRecorder on the shared byoHostReconciler, restoring originals on cleanup.
func setupHeartbeatReconciler(t *testing.T) {
	t.Helper()
	origTimeout := byoHostReconciler.HeartbeatTimeoutPeriod
	origRecorder := byoHostReconciler.Recorder
	byoHostReconciler.HeartbeatTimeoutPeriod = 3 * time.Second
	byoHostReconciler.Recorder = record.NewFakeRecorder(32)
	t.Cleanup(func() {
		byoHostReconciler.HeartbeatTimeoutPeriod = origTimeout
		byoHostReconciler.Recorder = origRecorder
	})
}

// patchHeartbeatTime sets LastHeartbeatTime=now on byoHost via a status patch.
// byoHostReconciler uses a direct client so no cache sync is needed before
// calling Reconcile.
func patchHeartbeatTime(t *testing.T, byoHost *infrastructurev1beta1.ByoHost) {
	t.Helper()
	helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
	require.NoError(t, err)
	now := metav1.Now()
	byoHost.Status.LastHeartbeatTime = &now
	require.NoError(t, helper.Patch(context.Background(), byoHost))
}

// testHeartbeatRecovery is the subtest body for the recovery case — extracted
// to keep TestByohostController_Heartbeat within funlen limits.
func testHeartbeatRecovery(t *testing.T) {
	t.Helper()
	byoHost := newByoHostForTest(t, "heartbeat-recover")
	key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
	fakeRecorder := record.NewFakeRecorder(32)
	byoHostReconciler.Recorder = fakeRecorder

	// First reconcile with no heartbeat — host goes disconnected.
	_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	// Agent "comes back" — patch a fresh heartbeat directly in the API server.
	updated := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, updated))
	patchHeartbeatTime(t, updated)

	// Second reconcile — direct client reads the fresh heartbeat immediately.
	_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	final := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, final))
	assert.True(t, conditions.IsTrue(final, infrastructurev1beta1.AgentConnected))

	events := eventutils.CollectEvents(fakeRecorder.Events)
	assert.Contains(t, events, "Normal AgentConnected agent heartbeat received")
}

func TestByohostController_Heartbeat(t *testing.T) {
	// Shared reconciler config — each subtest creates its own ByoHost.
	// byoHostReconciler uses a direct client, so patches are visible to
	// Reconcile immediately without any cache sync wait.
	setupHeartbeatReconciler(t)

	t.Run("marks AgentConnected=False and requeues at half-timeout when no heartbeat", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-never")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}

		result, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
		assert.Equal(t, 1500*time.Millisecond, result.RequeueAfter)

		updated := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, updated))
		cond := conditions.Get(updated, infrastructurev1beta1.AgentConnected)
		require.NotNil(t, cond)
		assert.Equal(t, corev1.ConditionFalse, cond.Status)
		assert.Equal(t, infrastructurev1beta1.HeartbeatTimeoutReason, cond.Reason)
		assert.Equal(t, clusterv1.ConditionSeverityWarning, cond.Severity)
		assert.Equal(t, "Heartbeat timeout detected", cond.Message)
	})

	t.Run("marks AgentConnected=True when a recent heartbeat is present", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-recent")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
		patchHeartbeatTime(t, byoHost)

		_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		updated := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, updated))
		assert.True(t, conditions.IsTrue(updated, infrastructurev1beta1.AgentConnected))
	})

	t.Run("does not bump resourceVersion on a no-op reconcile", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-noop")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
		patchHeartbeatTime(t, byoHost)

		_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		first := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, first))
		firstRV := first.ResourceVersion

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		second := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, second))
		assert.Equal(t, firstRV, second.ResourceVersion, "resourceVersion should not change on a no-op reconcile")
	})

	t.Run("recovers to AgentConnected=True and emits Normal event when heartbeat resumes after timeout", testHeartbeatRecovery)

	t.Run("records Warning event only on transition into disconnected", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-event")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
		fakeRecorder := record.NewFakeRecorder(32)
		byoHostReconciler.Recorder = fakeRecorder
		patchHeartbeatTime(t, byoHost)

		_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		time.Sleep(byoHostReconciler.HeartbeatTimeoutPeriod + 50*time.Millisecond)

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		events := eventutils.CollectEvents(fakeRecorder.Events)
		assert.Contains(t, events, "Warning AgentDisconnected agent heartbeat not received within timeout period")
	})
}
