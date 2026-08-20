// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newInstallReadyByoHost creates a ByoHost wired with a MachineRef, a
// bootstrap secret, and an installation secret — everything reconcileNormal
// needs to actually drive install and join, rather than bailing out early
// on a not-ready precondition.
func newInstallReadyByoHost(t *testing.T, name string) types.NamespacedName {
	t.Helper()
	ctx := t.Context()

	byoMachine := builder.ByoMachine("default", "machine-"+name).Build()
	require.NoError(t, k8sClient.Create(ctx, byoMachine))
	t.Cleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), byoMachine)) })

	bootstrapSecret := builder.Secret("default", "bootstrap-"+name).
		WithData(`write_files:
- path: fake/path
  content: blah
runCmd:
- echo 'run some command'`).Build()
	require.NoError(t, k8sClient.Create(ctx, bootstrapSecret))
	t.Cleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), bootstrapSecret)) })

	installationSecret := builder.Secret("default", "install-"+name).
		WithKeyData("install", `echo "install"`).
		WithKeyData(uninstallScriptKey, `echo "uninstall"`).Build()
	require.NoError(t, k8sClient.Create(ctx, installationSecret))
	t.Cleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), installationSecret)) })

	byoHost := builder.ByoHost("default", name).Build()
	byoHost.Annotations = map[string]string{
		infrastructurev1beta1.K8sVersionAnnotation:               testK8sVersion,
		infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: testBundleLookupBaseRegistry,
	}
	require.NoError(t, k8sClient.Create(ctx, byoHost))
	t.Cleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), byoHost)) })

	helper, err := patch.NewHelper(byoHost, k8sClient)
	require.NoError(t, err)
	byoHost.Status.MachineRef = &corev1.ObjectReference{
		Kind:       "ByoMachine",
		Namespace:  byoMachine.Namespace,
		Name:       byoMachine.Name,
		UID:        byoMachine.UID,
		APIVersion: byoHost.APIVersion,
	}
	byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{Kind: kindSecret, Namespace: bootstrapSecret.Namespace, Name: bootstrapSecret.Name}
	byoHost.Spec.InstallationSecret = &corev1.ObjectReference{Kind: kindSecret, Namespace: installationSecret.Namespace, Name: installationSecret.Name}
	require.NoError(t, helper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{}))

	return types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
}

// TestHostReconciler_HeartbeatSurvivesConcurrentConditionPatch guards the
// property that made the raw-patch rewrite of patchHeartbeat worth doing:
// Reconcile's own deferred patch.Helper write (which lands conditions like
// K8sComponentsInstallationSucceeded) and the heartbeat pulse's writes,
// happening while Reconcile is still blocked inside a slow install script,
// must both survive — neither should revert or lose the other.
//
// If Reconcile's own tracked object ever regressed to touching
// LastHeartbeatTime again (the bug this whole design avoids), its stale
// pre-install snapshot would win when its deferred patch finally fires,
// clobbering whatever the pulse wrote in the interim — this test catches
// that by comparing the final LastHeartbeatTime against a value observed
// mid-flight, on the server, rather than a wall-clock threshold.
func TestHostReconciler_HeartbeatSurvivesConcurrentConditionPatch(t *testing.T) {
	callCount := 0
	fakeCommandRunner := &cloudinitfakes.FakeICmdRunner{}
	fakeCommandRunner.RunCmdStub = func(_ context.Context, _ string) error {
		callCount++
		if callCount == 1 {
			// Only the install call needs to be slow enough for a pulse
			// tick to land — the join call doesn't add anything this test
			// needs, so let it return immediately.
			time.Sleep(180 * time.Millisecond)
		}
		return nil
	}

	r := &reconciler.HostReconciler{
		Client:              k8sClient,
		CmdRunner:           fakeCommandRunner,
		FileWriter:          &cloudinitfakes.FakeIFileWriter{},
		TemplateParser:      &cloudinitfakes.FakeITemplateParser{},
		Recorder:            record.NewFakeRecorder(32),
		HeartbeatInterval:   30 * time.Millisecond,
		MaxBlockingDuration: time.Second,
	}
	key := newInstallReadyByoHost(t, "heartbeat-concurrent-safety")

	done := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		done <- err
	}()

	// A single poll, timed to land after at least one pulse tick, while
	// Reconcile is still blocked inside the install call. Comparing the
	// final value against what's observed here, rather than against a
	// wall-clock threshold, sidesteps metav1.Time's second-precision
	// truncation through the API server — it only asserts a monotonicity
	// property, which is what actually matters.
	time.Sleep(120 * time.Millisecond)
	mid := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, k8sClient.Get(t.Context(), key, mid))
	require.NotNil(t, mid.Status.LastHeartbeatTime, "expected a heartbeat to have landed while Reconcile was still running")
	lastSeenDuringRun := mid.Status.LastHeartbeatTime.Time

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Reconcile did not return in time")
	}

	afterInstall := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, k8sClient.Get(t.Context(), key, afterInstall))

	installCond := conditions.Get(afterInstall, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
	require.NotNil(t, installCond)
	assert.Equal(t, corev1.ConditionTrue, installCond.Status)

	// join lands on its own reconcile (see host_reconciler.go) -- one more, fast, call to reach it
	_, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	updated := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, k8sClient.Get(t.Context(), key, updated))

	bootstrapCond := conditions.Get(updated, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
	require.NotNil(t, bootstrapCond)
	assert.Equal(t, corev1.ConditionTrue, bootstrapCond.Status)

	require.NotNil(t, updated.Status.LastHeartbeatTime)
	assert.False(t, updated.Status.LastHeartbeatTime.Time.Before(lastSeenDuringRun),
		"Reconcile's own deferred patch must not revert LastHeartbeatTime to a value older than what "+
			"the pulse had already written mid-flight")
}
