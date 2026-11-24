// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

var (
	secretsRuleH1 = rbacv1.PolicyRule{
		APIGroups:     []string{""},
		Resources:     []string{"secrets"},
		ResourceNames: []string{"byoh-install-m1", "byoh-uninstall-m1", "m1-bootstrap"},
		Verbs:         []string{"get"},
	}
	byoHostRuleH1 = rbacv1.PolicyRule{
		APIGroups:     []string{"infrastructure.cluster.x-k8s.io"},
		Resources:     []string{"byohosts"},
		ResourceNames: []string{"h1-abc12"},
		Verbs:         []string{"get"},
	}
)

func TestHostSecretRules(t *testing.T) {
	testCases := []struct {
		name  string
		rules []rbacv1.PolicyRule
		names []string
		want  []rbacv1.PolicyRule
	}{
		{
			name:  "names are added as one get rule, sorted",
			rules: []rbacv1.PolicyRule{byoHostRuleH1},
			names: []string{"m1-bootstrap", "byoh-uninstall-m1", "byoh-install-m1"},
			want:  []rbacv1.PolicyRule{byoHostRuleH1, secretsRuleH1},
		},
		{
			name:  "duplicate and empty names are dropped",
			rules: []rbacv1.PolicyRule{byoHostRuleH1},
			names: []string{"m1-bootstrap", "", "m1-bootstrap", "byoh-install-m1", "byoh-uninstall-m1"},
			want:  []rbacv1.PolicyRule{byoHostRuleH1, secretsRuleH1},
		},
		{
			name:  "an earlier Secret rule is replaced",
			rules: []rbacv1.PolicyRule{byoHostRuleH1, secretsRuleH1},
			names: []string{"m2-bootstrap"},
			want: []rbacv1.PolicyRule{
				byoHostRuleH1,
				{
					APIGroups:     []string{""},
					Resources:     []string{"secrets"},
					ResourceNames: []string{"m2-bootstrap"},
					Verbs:         []string{"get"},
				},
			},
		},
		{
			name:  "no names removes the Secret rule instead of leaving an empty name list",
			rules: []rbacv1.PolicyRule{byoHostRuleH1, secretsRuleH1},
			names: nil,
			want:  []rbacv1.PolicyRule{byoHostRuleH1},
		},
		{
			name:  "no names and no Secret rule changes nothing",
			rules: []rbacv1.PolicyRule{byoHostRuleH1},
			names: nil,
			want:  []rbacv1.PolicyRule{byoHostRuleH1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := HostSecretRules(tc.rules, tc.names)
			assert.Equal(t, tc.want, got)
		})
	}
}

// hostSecretsFixture returns a fake client holding the host-scoped Role of
// h1-abc12 with the given extra rules, and the ByoHost it is for.
func hostSecretsFixture(t *testing.T, label string, extra ...rbacv1.PolicyRule) (client.WithWatch, *rbacv1.Role, *infrastructurev1beta1.ByoHost) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))

	host := identity.HostIdentity{TenantNamespace: "tenant-a", NormalizedHostName: "h1", Suffix: "abc12"}
	role, _ := HostScopedRBAC(host)
	role.Rules = append(role.Rules, extra...)
	role.Labels = map[string]string{identity.TenantLabel: label}

	byoHost := &infrastructurev1beta1.ByoHost{ObjectMeta: metav1.ObjectMeta{Name: "h1-abc12", Namespace: "tenant-a"}}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(role).Build(), role, byoHost
}

func TestSetHostSecretAccess(t *testing.T) {
	roleKey := client.ObjectKey{Namespace: "tenant-a", Name: "byoh-host-h1-abc12"}
	names := []string{"m1-bootstrap", "byoh-install-m1", "byoh-uninstall-m1"}

	t.Run("adds the Secret rule and keeps the ByoHost rules", func(t *testing.T) {
		c, role, byoHost := hostSecretsFixture(t, "tenant-a")

		err := setHostSecretAccess(t.Context(), c, c, byoHost, names)
		require.NoError(t, err)

		got := &rbacv1.Role{}
		err = c.Get(t.Context(), roleKey, got)
		require.NoError(t, err)
		assert.Equal(t, HostSecretRules(role.Rules, names), got.Rules)
		assert.Len(t, got.Rules, 3)
	})

	t.Run("empties the Secret rule when no names are left", func(t *testing.T) {
		c, role, byoHost := hostSecretsFixture(t, "tenant-a", secretsRuleH1)

		err := setHostSecretAccess(t.Context(), c, c, byoHost, nil)
		require.NoError(t, err)

		got := &rbacv1.Role{}
		err = c.Get(t.Context(), roleKey, got)
		require.NoError(t, err)
		assert.Equal(t, role.Rules[:2], got.Rules)
	})

	t.Run("does not write when the Role is current", func(t *testing.T) {
		c, _, byoHost := hostSecretsFixture(t, "tenant-a", secretsRuleH1)
		before := &rbacv1.Role{}
		require.NoError(t, c.Get(t.Context(), roleKey, before))

		writes := 0
		counting := interceptor.NewClient(c, interceptor.Funcs{
			Update: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes++
				return w.Update(ctx, obj, opts...)
			},
		})

		err := setHostSecretAccess(t.Context(), c, counting, byoHost, names)
		require.NoError(t, err)

		after := &rbacv1.Role{}
		require.NoError(t, c.Get(t.Context(), roleKey, after))
		assert.Equal(t, 0, writes)
		assert.Equal(t, before.ResourceVersion, after.ResourceVersion)
	})

	t.Run("a host with no Role is skipped and no Role is created", func(t *testing.T) {
		c, _, _ := hostSecretsFixture(t, "tenant-a")
		tokenHost := &infrastructurev1beta1.ByoHost{ObjectMeta: metav1.ObjectMeta{Name: "token-host", Namespace: "tenant-a"}}

		err := setHostSecretAccess(t.Context(), c, c, tokenHost, names)
		require.NoError(t, err)

		err = c.Get(t.Context(), client.ObjectKey{Namespace: "tenant-a", Name: "byoh-host-token-host"}, &rbacv1.Role{})
		assert.True(t, apierrors.IsNotFound(err), "want not found, got %v", err)
	})

	t.Run("a Role without the tenant label of its namespace is not changed", func(t *testing.T) {
		c, role, byoHost := hostSecretsFixture(t, "tenant-b")

		err := setHostSecretAccess(t.Context(), c, c, byoHost, names)
		require.NoError(t, err)

		got := &rbacv1.Role{}
		err = c.Get(t.Context(), roleKey, got)
		require.NoError(t, err)
		assert.Equal(t, role.Rules, got.Rules)
	})

	t.Run("a conflict re-reads the Role and retries", func(t *testing.T) {
		c, _, byoHost := hostSecretsFixture(t, "tenant-a")

		conflicts := 1
		conflicting := interceptor.NewClient(c, interceptor.Funcs{
			Update: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if conflicts > 0 {
					conflicts--
					return apierrors.NewConflict(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}, obj.GetName(), errors.New("stale"))
				}
				return w.Update(ctx, obj, opts...)
			},
		})

		err := setHostSecretAccess(t.Context(), c, conflicting, byoHost, names)
		require.NoError(t, err)

		got := &rbacv1.Role{}
		err = c.Get(t.Context(), roleKey, got)
		require.NoError(t, err)
		assert.Contains(t, got.Rules, secretsRuleH1)
	})

	t.Run("an update error is returned", func(t *testing.T) {
		c, _, byoHost := hostSecretsFixture(t, "tenant-a")
		failing := interceptor.NewClient(c, interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				return errors.New("boom")
			},
		})

		err := setHostSecretAccess(t.Context(), c, failing, byoHost, names)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}
