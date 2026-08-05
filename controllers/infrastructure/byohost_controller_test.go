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
	WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
		return obj.(*infrastructurev1beta1.ByoHost).Spec.UninstallationSecret != nil
	})

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

func TestByohostController_Heartbeat(t *testing.T) {
	// Shared reconciler config for all heartbeat subtests.
	// Each subtest creates its own ByoHost since they mutate different state.
	origTimeout := byoHostReconciler.HeartbeatTimeoutPeriod
	origRecorder := byoHostReconciler.Recorder
	byoHostReconciler.HeartbeatTimeoutPeriod = 3 * time.Second
	byoHostReconciler.Recorder = record.NewFakeRecorder(32)
	t.Cleanup(func() {
		byoHostReconciler.HeartbeatTimeoutPeriod = origTimeout
		byoHostReconciler.Recorder = origRecorder
	})

	t.Run("marks AgentConnected=False and requeues at half-timeout when no heartbeat", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-never")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}

		result, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
		assert.Equal(t, 1500*time.Millisecond, result.RequeueAfter)

		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return conditions.IsFalse(obj.(*infrastructurev1beta1.ByoHost), infrastructurev1beta1.AgentConnected)
		})

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

		helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
		require.NoError(t, err)
		now := metav1.Now()
		byoHost.Status.LastHeartbeatTime = &now
		require.NoError(t, helper.Patch(context.Background(), byoHost))
		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return obj.(*infrastructurev1beta1.ByoHost).Status.LastHeartbeatTime != nil
		})

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return conditions.IsTrue(obj.(*infrastructurev1beta1.ByoHost), infrastructurev1beta1.AgentConnected)
		})
	})

	t.Run("does not bump resourceVersion on a no-op reconcile", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-noop")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}

		helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
		require.NoError(t, err)
		now := metav1.Now()
		byoHost.Status.LastHeartbeatTime = &now
		require.NoError(t, helper.Patch(context.Background(), byoHost))
		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return obj.(*infrastructurev1beta1.ByoHost).Status.LastHeartbeatTime != nil
		})

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return conditions.IsTrue(obj.(*infrastructurev1beta1.ByoHost), infrastructurev1beta1.AgentConnected)
		})

		first := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, first))
		firstRV := first.ResourceVersion

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		assert.Never(t, func() bool {
			second := &infrastructurev1beta1.ByoHost{}
			require.NoError(t, k8sManager.GetClient().Get(context.Background(), key, second))
			return second.ResourceVersion != firstRV
		}, 500*time.Millisecond, 50*time.Millisecond, "resourceVersion should not change on a no-op reconcile")
	})

	t.Run("records Warning event only on transition into disconnected", func(t *testing.T) {
		byoHost := newByoHostForTest(t, "heartbeat-event")
		key := types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
		fakeRecorder := record.NewFakeRecorder(32)
		byoHostReconciler.Recorder = fakeRecorder

		helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
		require.NoError(t, err)
		now := metav1.Now()
		byoHost.Status.LastHeartbeatTime = &now
		require.NoError(t, helper.Patch(context.Background(), byoHost))
		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return obj.(*infrastructurev1beta1.ByoHost).Status.LastHeartbeatTime != nil
		})

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
		WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
			return conditions.IsTrue(obj.(*infrastructurev1beta1.ByoHost), infrastructurev1beta1.AgentConnected)
		})

		time.Sleep(byoHostReconciler.HeartbeatTimeoutPeriod + 50*time.Millisecond)

		_, err = byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		require.NoError(t, err)

		events := eventutils.CollectEvents(fakeRecorder.Events)
		assert.Contains(t, events, "Warning AgentDisconnected agent heartbeat not received within timeout period")
	})
}
