// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infrav1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

const (
	hostSecretsNamespace = "tenant-a"
	secretKind           = "Secret"
	byoMachineKind       = "ByoMachine"
)

// hostSecretsMachineScope returns the scope of a ByoMachine named machineName
// with the bootstrap Secret bootstrapSecret. With withInstaller it has an
// installer config that reports the installation and uninstallation Secrets.
func hostSecretsMachineScope(machineName, bootstrapSecret string, withInstaller bool, host *infrav1.ByoHost) (*byoMachineScope, []client.Object) {
	byoMachine := &infrav1.ByoMachine{
		TypeMeta:   metav1.TypeMeta{Kind: byoMachineKind, APIVersion: infrav1.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: machineName, Namespace: hostSecretsNamespace},
	}
	scope := &byoMachineScope{
		Cluster:    &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: hostSecretsNamespace}},
		ByoCluster: &infrav1.ByoCluster{},
		ByoMachine: byoMachine,
		ByoHost:    host,
		Machine: &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: machineName, Namespace: hostSecretsNamespace},
			Spec:       clusterv1.MachineSpec{Bootstrap: clusterv1.Bootstrap{DataSecretName: ptr.To(bootstrapSecret)}},
		},
	}

	objects := []client.Object{}
	if withInstaller {
		byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
			APIVersion: infrav1.GroupVersion.String(),
			Kind:       "K8sInstallerConfigTemplate",
			Name:       "template",
		}
		objects = append(objects, &infrav1.K8sInstallerConfig{
			ObjectMeta: metav1.ObjectMeta{Name: machineName, Namespace: hostSecretsNamespace},
			Status: infrav1.K8sInstallerConfigStatus{
				Ready:                true,
				InstallationSecret:   &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "byoh-install-" + machineName},
				UninstallationSecret: &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "byoh-uninstall-" + machineName},
			},
		})
	}

	return scope, objects
}

// hostSecretsReconciler returns a reconciler over a fake client that holds the
// host-scoped Role and ByoHost of h1-abc12 when withRole is set, and objects.
func hostSecretsReconciler(t *testing.T, withRole bool, objects ...client.Object) (*ByoMachineReconciler, client.WithWatch, *infrav1.ByoHost) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, infrav1.AddToScheme(scheme))

	host := &infrav1.ByoHost{ObjectMeta: metav1.ObjectMeta{Name: "h1-abc12", Namespace: hostSecretsNamespace}}
	objects = append(objects, host)
	if withRole {
		role, _ := HostScopedRBAC(identity.HostIdentity{TenantNamespace: hostSecretsNamespace, NormalizedHostName: "h1", Suffix: "abc12"})
		objects = append(objects, role)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(host).Build()

	return &ByoMachineReconciler{Client: c, APIReader: c, Scheme: scheme, Recorder: record.NewFakeRecorder(8)}, c, host
}

func hostSecretNamesInRole(t *testing.T, c client.Client) []string {
	t.Helper()

	role := &rbacv1.Role{}
	err := c.Get(t.Context(), client.ObjectKey{Namespace: hostSecretsNamespace, Name: "byoh-host-h1-abc12"}, role)
	require.NoError(t, err)

	for i := range role.Rules {
		if isSecretsRule(&role.Rules[i]) {
			return role.Rules[i].ResourceNames
		}
	}

	return nil
}

func TestByoMachineReconciler_SetHostSecretAccess(t *testing.T) {
	testCases := []struct {
		name            string
		machineName     string
		bootstrapSecret string
		withInstaller   bool
		hostSpec        infrav1.ByoHostSpec
		want            []string
	}{
		{
			name:            "machine with an installer config",
			machineName:     "m1",
			bootstrapSecret: "m1-bootstrap",
			withInstaller:   true,
			want:            []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		},
		{
			name:            "machine without an installer config",
			machineName:     "m1",
			bootstrapSecret: "m1-bootstrap",
			want:            []string{"m1-bootstrap"},
		},
		{
			name:            "second machine replaces the names of the first",
			machineName:     "m2",
			bootstrapSecret: "m2-bootstrap",
			withInstaller:   true,
			want:            []string{"byoh-install-m2", "byoh-uninstall-m2", "m2-bootstrap"},
		},
		{
			name:            "Secret references the host wrote in its spec are ignored",
			machineName:     "m1",
			bootstrapSecret: "m1-bootstrap",
			withInstaller:   true,
			hostSpec: infrav1.ByoHostSpec{
				BootstrapSecret:      &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "byoh-token"},
				InstallationSecret:   &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "byoh-token"},
				UninstallationSecret: &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "h1-cred"},
			},
			want: []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scope, objects := hostSecretsMachineScope(tc.machineName, tc.bootstrapSecret, tc.withInstaller, nil)
			r, c, host := hostSecretsReconciler(t, true, objects...)
			host.Spec = tc.hostSpec

			err := r.setHostSecretAccess(t.Context(), scope, host)
			require.NoError(t, err)

			assert.Equal(t, tc.want, hostSecretNamesInRole(t, c))
		})
	}

	t.Run("a Role rule removed by hand is back", func(t *testing.T) {
		scope, objects := hostSecretsMachineScope("m1", "m1-bootstrap", true, nil)
		r, c, host := hostSecretsReconciler(t, true, objects...)
		require.NoError(t, r.setHostSecretAccess(t.Context(), scope, host))

		role := &rbacv1.Role{}
		key := client.ObjectKey{Namespace: hostSecretsNamespace, Name: "byoh-host-h1-abc12"}
		require.NoError(t, c.Get(t.Context(), key, role))
		role.Rules = HostSecretRules(role.Rules, nil)
		require.NoError(t, c.Update(t.Context(), role))
		require.Empty(t, hostSecretNamesInRole(t, c))

		err := r.setHostSecretAccess(t.Context(), scope, host)
		require.NoError(t, err)
		assert.Equal(t, []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"}, hostSecretNamesInRole(t, c))
	})

	t.Run("a host with no Role binds without error and no Role is created", func(t *testing.T) {
		scope, objects := hostSecretsMachineScope("m1", "m1-bootstrap", true, nil)
		r, c, host := hostSecretsReconciler(t, false, objects...)

		err := r.setHostSecretAccess(t.Context(), scope, host)
		require.NoError(t, err)

		err = c.Get(t.Context(), client.ObjectKey{Namespace: hostSecretsNamespace, Name: "byoh-host-h1-abc12"}, &rbacv1.Role{})
		assert.True(t, apierrors.IsNotFound(err), "want not found, got %v", err)
	})
}

func TestByoMachineReconciler_AttachByoHost(t *testing.T) {
	t.Run("the Role names the bootstrap Secret and the spec holds it", func(t *testing.T) {
		scope, objects := hostSecretsMachineScope("m1", "m1-bootstrap", false, nil)
		r, c, _ := hostSecretsReconciler(t, true, objects...)

		_, err := r.attachByoHost(t.Context(), scope)
		require.NoError(t, err)

		assert.Equal(t, []string{"m1-bootstrap"}, hostSecretNamesInRole(t, c))
		stored := &infrav1.ByoHost{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: hostSecretsNamespace, Name: "h1-abc12"}, stored))
		assert.Equal(t, "m1-bootstrap", stored.Spec.BootstrapSecret.Name)
	})

	t.Run("a failed Role update leaves the spec unwritten", func(t *testing.T) {
		scope, objects := hostSecretsMachineScope("m1", "m1-bootstrap", false, nil)
		r, c, _ := hostSecretsReconciler(t, true, objects...)
		failing := interceptor.NewClient(c, interceptor.Funcs{
			Update: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, isRole := obj.(*rbacv1.Role); isRole {
					return errors.New("role update refused")
				}
				return w.Update(ctx, obj, opts...)
			},
		})
		r.Client = failing

		_, err := r.attachByoHost(t.Context(), scope)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "role update refused")

		stored := &infrav1.ByoHost{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: hostSecretsNamespace, Name: "h1-abc12"}, stored))
		assert.Nil(t, stored.Spec.BootstrapSecret)
		assert.Nil(t, stored.Status.MachineRef)
	})
}
