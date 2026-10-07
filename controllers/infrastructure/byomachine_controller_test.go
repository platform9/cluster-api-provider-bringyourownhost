// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"go/build"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	v1beta2conditions "sigs.k8s.io/cluster-api/util/conditions"
	conditions "sigs.k8s.io/cluster-api/util/conditions/deprecated/v1beta1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestByoMachineController_ReconcilesLegacyStatus(t *testing.T) {
	c := startIsolatedEnv(t)
	ns := newTestNamespace(t, c, "byomachine-legacy-status")
	legacyCondition := func(condType, status, reason, severity string) map[string]interface{} {
		cond := map[string]interface{}{"type": condType, "status": status, "lastTransitionTime": "2025-06-01T00:00:00Z"}
		if reason != "" {
			cond["reason"] = reason
			cond["severity"] = severity
		}
		return cond
	}

	tests := []struct {
		name   string
		status map[string]interface{}
	}{
		{
			name: "provisioned",
			status: map[string]interface{}{
				"ready": true,
				"conditions": []interface{}{
					legacyCondition("Ready", "True", "", ""),
					legacyCondition(string(infrastructurev1beta1.BYOHostReady), "True", "", ""),
				},
			},
		},
		{
			name: "provisioning",
			status: map[string]interface{}{
				"ready": false,
				"conditions": []interface{}{
					legacyCondition("Ready", "False", infrastructurev1beta1.InstallationSecretNotAvailableReason, "Info"),
					legacyCondition(string(infrastructurev1beta1.BYOHostReady), "False", infrastructurev1beta1.InstallationSecretNotAvailableReason, "Info"),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			byoMachine, r := newAttachedByoMachine(t, c, ns, "legacy-"+tt.name)
			storeLegacyStatus(t, c, byoMachine, "byomachines.infrastructure.cluster.x-k8s.io", tt.status)

			_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(byoMachine)})
			require.NoError(t, err)

			updated := &infrastructurev1beta1.ByoMachine{}
			require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(byoMachine), updated))
			require.NotNil(t, updated.Status.Initialization)
			assert.Equal(t, ptr.To(true), updated.Status.Initialization.Provisioned)
			assert.True(t, updated.Status.Ready) //nolint:staticcheck // deprecated field is still written
			for _, condType := range []string{clusterv1.ReadyCondition, string(infrastructurev1beta1.BYOHostReady)} {
				cond := v1beta2conditions.Get(updated, condType)
				require.NotNil(t, cond, condType)
				assert.Equal(t, metav1.ConditionTrue, cond.Status, condType)
			}
			assert.True(t, conditions.IsTrue(updated, infrastructurev1beta1.BYOHostReady))
			assertConditionsInV1Beta2Shape(t, c, updated)
		})
	}
}

// A ByoMachine already being deleted when BYOH is upgraded still holds a legacy
// BYOHostReady with no reason. Deletion rewrites that condition, so its status patch
// is accepted and the reconcile returns no error rather than failing once on the stale
// item.
func TestByoMachineController_DeletesLegacyStatus(t *testing.T) {
	c := startIsolatedEnv(t)
	ns := newTestNamespace(t, c, "byomachine-legacy-delete")
	byoMachine, r := newAttachedByoMachine(t, c, ns, "legacy-deleting")

	finalizerPatch, err := patch.NewHelper(byoMachine, c)
	require.NoError(t, err)
	controllerutil.AddFinalizer(byoMachine, infrastructurev1beta1.MachineFinalizer)
	require.NoError(t, finalizerPatch.Patch(t.Context(), byoMachine))

	storeLegacyStatus(t, c, byoMachine, "byomachines.infrastructure.cluster.x-k8s.io", map[string]interface{}{
		"ready": true,
		"conditions": []interface{}{
			map[string]interface{}{"type": string(infrastructurev1beta1.BYOHostReady), "status": "True", "lastTransitionTime": "2025-06-01T00:00:00Z"},
		},
	})
	require.NoError(t, c.Delete(t.Context(), byoMachine))

	_, err = r.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(byoMachine)})
	require.NoError(t, err)

	err = c.Get(t.Context(), client.ObjectKeyFromObject(byoMachine), &infrastructurev1beta1.ByoMachine{})
	assert.True(t, apierrors.IsNotFound(err), "expected the ByoMachine to be deleted, got %v", err)
}

// startIsolatedEnv starts an API server of the test's own with the BYOH and Cluster API
// CRDs. Unlike the suite's shared envtest, no manager runs controllers against it, so the
// test's explicit reconcile is the only one, and relaxing a CRD's schema (see
// storeLegacyStatus) can't affect other tests.
func startIsolatedEnv(t *testing.T) client.Client {
	t.Helper()
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.12.11", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}
	restConfig, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })

	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, infrastructurev1beta1.AddToScheme(s))
	require.NoError(t, clusterv1.AddToScheme(s))
	c, err := client.New(restConfig, client.Options{Scheme: s})
	require.NoError(t, err)
	return c
}

// newAttachedByoMachine creates a ByoMachine whose ByoHost is attached and whose Node exists,
// so that a reconcile runs through to setting the providerID, plus a reconciler that reads
// the Node from a workload cluster of its own.
func newAttachedByoMachine(t *testing.T, c client.Client, ns, name string) (*infrastructurev1beta1.ByoMachine, *controllers.ByoMachineReconciler) {
	t.Helper()
	infraCluster := builder.ByoCluster(ns, name).Build()
	require.NoError(t, c.Create(t.Context(), infraCluster))
	cluster := builder.Cluster(ns, name).WithInfrastructureRef(infraCluster).Build()
	require.NoError(t, c.Create(t.Context(), cluster))
	clusterPatch, err := patch.NewHelper(cluster, c)
	require.NoError(t, err)
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	require.NoError(t, clusterPatch.Patch(t.Context(), cluster))

	machine := builder.Machine(ns, name).
		WithClusterName(name).
		WithClusterVersion("v1.34.0").
		WithBootstrapDataSecret(fakeBootstrapSecret).
		Build()
	require.NoError(t, c.Create(t.Context(), machine))
	byoMachine := builder.ByoMachine(ns, name).WithClusterLabel(name).WithOwnerMachine(machine).Build()
	require.NoError(t, c.Create(t.Context(), byoMachine))

	byoHost := builder.ByoHost(ns, name).
		WithLabels(map[string]string{infrastructurev1beta1.AttachedByoMachineLabel: ns + "." + byoMachine.Name}).
		Build()
	require.NoError(t, c.Create(t.Context(), byoHost))
	hostPatch, err := patch.NewHelper(byoHost, c)
	require.NoError(t, err)
	byoHost.Status.MachineRef = &corev1.ObjectReference{
		Kind:      "ByoMachine",
		Namespace: ns,
		Name:      byoMachine.Name,
		UID:       byoMachine.UID,
	}
	require.NoError(t, hostPatch.Patch(t.Context(), byoHost))

	workloadClient := fake.NewClientBuilder().WithObjects(builder.Node(ns, byoHost.Name).Build()).Build()
	return byoMachine, &controllers.ByoMachineReconciler{
		Client:       c,
		ClusterCache: clustercache.NewFakeClusterCache(workloadClient, client.ObjectKeyFromObject(cluster)),
		Recorder:     record.NewFakeRecorder(32),
	}
}

var crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// storeLegacyStatus leaves obj in the API server with the given status, as written by a
// controller from before the v1beta2 contract: status.conditions in the legacy shape
// (severity set, reason empty on True conditions), which the current schema rejects.
// It does this by relaxing the CRD's status schema for the write, then restoring it and
// waiting until the API server enforces it again.
func storeLegacyStatus(t *testing.T, c client.Client, obj client.Object, crdName string, status map[string]interface{}) {
	t.Helper()
	testCtx := t.Context()
	gvk, err := c.GroupVersionKindFor(obj)
	require.NoError(t, err)

	original := setCRDStatusSchema(t, c, crdName, nil)

	require.Eventually(t, func() bool {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		if getErr := c.Get(testCtx, client.ObjectKeyFromObject(obj), u); getErr != nil {
			return false
		}
		u.Object["status"] = runtime.DeepCopyJSON(status)
		return c.Status().Update(testCtx, u) == nil
	}, 10*time.Second, 100*time.Millisecond, "the relaxed %s schema never accepted the legacy status", crdName)

	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(gvk)
	require.NoError(t, c.Get(testCtx, client.ObjectKeyFromObject(obj), stored))
	storedConditions, _, err := unstructured.NestedSlice(stored.Object, "status", "conditions")
	require.NoError(t, err)
	require.Equal(t, status["conditions"], storedConditions, "the legacy conditions were not stored as given")

	setCRDStatusSchema(t, c, crdName, original)
	// A condition with an empty reason only passes while the relaxed schema is still served.
	require.Eventually(t, func() bool {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		if getErr := c.Get(testCtx, client.ObjectKeyFromObject(obj), u); getErr != nil {
			return false
		}
		probe := map[string]interface{}{"type": "SchemaProbe", "status": "True", "lastTransitionTime": "2025-01-01T00:00:00Z"}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		if err := unstructured.SetNestedSlice(u.Object, append(conds, probe), "status", "conditions"); err != nil {
			return false
		}
		return apierrors.IsInvalid(c.Status().Update(testCtx, u, client.DryRunAll))
	}, 10*time.Second, 100*time.Millisecond, "the restored %s schema is not enforced", crdName)
}

// setCRDStatusSchema replaces the status schema of every version of the CRD and returns the
// previous ones. Passing nil relaxes it to accept any status.
func setCRDStatusSchema(t *testing.T, c client.Client, crdName string, schemas []interface{}) []interface{} {
	t.Helper()
	var previous []interface{}
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(crdGVK)
		if err := c.Get(context.Background(), client.ObjectKey{Name: crdName}, crd); err != nil {
			return err
		}
		versions, _, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
		if err != nil {
			return err
		}
		previous = make([]interface{}, len(versions))
		for i := range versions {
			version := versions[i].(map[string]interface{})
			path := []string{"schema", "openAPIV3Schema", "properties", "status"}
			previous[i], _, _ = unstructured.NestedFieldCopy(version, path...)
			next := interface{}(map[string]interface{}{"type": "object", "x-kubernetes-preserve-unknown-fields": true})
			if schemas != nil {
				next = schemas[i]
			}
			if err := unstructured.SetNestedField(version, next, path...); err != nil {
				return err
			}
		}
		if err := unstructured.SetNestedSlice(crd.Object, versions, "spec", "versions"); err != nil {
			return err
		}
		return c.Update(context.Background(), crd)
	}))
	return previous
}

// assertConditionsInV1Beta2Shape asserts that every stored status.conditions entry of obj
// has been rewritten in the v1beta2 shape: a reason set and no legacy severity left over.
func assertConditionsInV1Beta2Shape(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	gvk, err := c.GroupVersionKindFor(obj)
	require.NoError(t, err)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(obj), u))

	conds, _, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	require.NoError(t, err)
	require.NotEmpty(t, conds)
	for _, cond := range conds {
		cond := cond.(map[string]interface{})
		assert.NotEmpty(t, cond["reason"], "condition %v has no reason", cond["type"])
		assert.NotContains(t, cond, "severity", "condition %v kept its legacy severity", cond["type"])
	}
}

// newTestNamespace creates a namespace of the test's own, so objects from different tests
// sharing an API server don't collide.
func newTestNamespace(t *testing.T, c client.Client, name string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	require.NoError(t, c.Create(context.Background(), ns))
	return name
}
