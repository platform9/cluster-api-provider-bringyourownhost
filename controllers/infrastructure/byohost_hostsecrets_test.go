// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

func TestByoHostReconciler_ReleaseRemovesSecretAccess(t *testing.T) {
	testCases := []struct {
		name       string
		machineRef *corev1.ObjectReference
		annotation map[string]string
		// noUninstall is set for a host whose machine has no installer config.
		noUninstall     bool
		bootstrapSecret *corev1.ObjectReference
		want            []string
	}{
		{
			name: "released and cleaned host",
			want: nil,
		},
		{
			name:        "released host without an installer config",
			noUninstall: true,
			want:        nil,
		},
		{
			name:            "host being attached to a machine",
			noUninstall:     true,
			bootstrapSecret: &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "m1-bootstrap"},
			want:            []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		},
		{
			name:       "host still bound to a machine",
			machineRef: &corev1.ObjectReference{Kind: byoMachineKind, Name: "m1", Namespace: hostSecretsNamespace},
			want:       []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		},
		{
			name:       "host that has not finished its cleanup",
			annotation: map[string]string{infrav1.HostCleanupAnnotation: ""},
			want:       []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, infrav1.AddToScheme(scheme))

			byoHost := &infrav1.ByoHost{
				ObjectMeta: metav1.ObjectMeta{Name: "h1-abc12", Namespace: hostSecretsNamespace, Annotations: tc.annotation},
				Spec: infrav1.ByoHostSpec{
					UninstallationSecret: &corev1.ObjectReference{Kind: secretKind, Namespace: hostSecretsNamespace, Name: "byoh-uninstall-m1"},
				},
				Status: infrav1.ByoHostStatus{MachineRef: tc.machineRef},
			}
			byoHost.Spec.BootstrapSecret = tc.bootstrapSecret
			if tc.noUninstall {
				byoHost.Spec.UninstallationSecret = nil
			}
			role, _ := HostScopedRBAC(identity.HostIdentity{TenantNamespace: hostSecretsNamespace, NormalizedHostName: "h1", Suffix: "abc12"})
			role.Rules = append(role.Rules, secretsRuleH1)
			uninstallSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "byoh-uninstall-m1", Namespace: hostSecretsNamespace}}

			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(byoHost, role, uninstallSecret).WithStatusSubresource(byoHost).Build()
			r := &ByoHostReconciler{
				Client:                 c,
				APIReader:              c,
				Scheme:                 scheme,
				HeartbeatTimeoutPeriod: time.Minute,
				Recorder:               record.NewFakeRecorder(8),
			}

			_, err := r.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(byoHost)})
			require.NoError(t, err)

			assert.Equal(t, tc.want, hostSecretNamesInRole(t, c))
		})
	}
}
