// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	certv1 "k8s.io/api/certificates/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/rbacharness"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
)

const (
	byohGroup = "infrastructure.cluster.x-k8s.io"
	csrGroup  = "certificates.k8s.io"
	ownHost   = "h1-abc12"
)

// TestRBAC_HostKeepsAccess lists the access the agent and byohctl need. A
// later change that tightens the RBAC must keep every row allowed.
func TestRBAC_HostKeepsAccess(t *testing.T) {
	ownHostSelector := &authv1.FieldSelectorAttributes{
		Requirements: []metav1.FieldSelectorRequirement{
			{
				Key:      "metadata.name",
				Operator: metav1.FieldSelectorOpIn,
				Values:   []string{ownHost},
			},
		},
	}

	testCases := []struct {
		name  string
		attrs authv1.ResourceAttributes
	}{
		{
			name: "create own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "get own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "list own ByoHost with the name selector",
			attrs: authv1.ResourceAttributes{
				Verb:          "list",
				Group:         byohGroup,
				Resource:      "byohosts",
				Namespace:     rbacharness.TenantA,
				Name:          ownHost,
				FieldSelector: ownHostSelector,
			},
		},
		{
			name: "watch own ByoHost with the name selector",
			attrs: authv1.ResourceAttributes{
				Verb:          "watch",
				Group:         byohGroup,
				Resource:      "byohosts",
				Namespace:     rbacharness.TenantA,
				Name:          ownHost,
				FieldSelector: ownHostSelector,
			},
		},
		{
			name: "update own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "update",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "patch own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "patch",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "delete own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "delete",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "get own ByoHost status",
			attrs: authv1.ResourceAttributes{
				Verb:        "get",
				Group:       byohGroup,
				Resource:    "byohosts",
				Subresource: "status",
				Namespace:   rbacharness.TenantA,
				Name:        ownHost,
			},
		},
		{
			name: "patch own ByoHost status",
			attrs: authv1.ResourceAttributes{
				Verb:        "patch",
				Group:       byohGroup,
				Resource:    "byohosts",
				Subresource: "status",
				Namespace:   rbacharness.TenantA,
				Name:        ownHost,
			},
		},
		{
			name: "create Events in the tenant",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Resource:  "events",
				Namespace: rbacharness.TenantA,
			},
		},
		{
			name: "patch Events in the tenant",
			attrs: authv1.ResourceAttributes{
				Verb:      "patch",
				Resource:  "events",
				Namespace: rbacharness.TenantA,
			},
		},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), rbacharness.HostSubject(), &tc.attrs)
				require.NoError(t, err)
				assert.True(t, got)
			})
		}
	}
}

// TestRBAC_BootstrapAccess pins the access of a bootstrap token: the CSR verbs
// the agent uses and nothing else.
func TestRBAC_BootstrapAccess(t *testing.T) {
	csr := func(verb string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{
			Verb:     verb,
			Group:    csrGroup,
			Resource: "certificatesigningrequests",
		}
	}
	csrSubresource := func(verb, subresource string) authv1.ResourceAttributes {
		attrs := csr(verb)
		attrs.Subresource = subresource
		return attrs
	}

	testCases := []struct {
		name      string
		attrs     authv1.ResourceAttributes
		wantAllow bool
	}{
		{name: "create CSRs", attrs: csr("create"), wantAllow: true},
		{name: "get CSRs", attrs: csr("get"), wantAllow: true},
		{name: "list CSRs", attrs: csr("list"), wantAllow: true},
		{name: "watch CSRs", attrs: csr("watch"), wantAllow: true},
		{name: "update CSRs", attrs: csr("update"), wantAllow: false},
		{name: "patch CSRs", attrs: csr("patch"), wantAllow: false},
		{name: "delete CSRs", attrs: csr("delete"), wantAllow: false},
		{name: "deletecollection CSRs", attrs: csr("deletecollection"), wantAllow: false},
		{name: "update CSR approval", attrs: csrSubresource("update", "approval"), wantAllow: false},
		{name: "update CSR status", attrs: csrSubresource("update", "status"), wantAllow: false},
		{
			name: "approve on the kube-apiserver-client signer",
			attrs: authv1.ResourceAttributes{
				Verb:     "approve",
				Group:    csrGroup,
				Resource: "signers",
				Name:     certv1.KubeAPIServerClientSignerName,
			},
			wantAllow: false,
		},
		{
			name: "get a Secret in kube-system",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Resource:  "secrets",
				Namespace: metav1.NamespaceSystem,
			},
			wantAllow: false,
		},
		{
			name: "get a Secret in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Resource:  "secrets",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
		{
			name: "create BootstrapKubeconfigs in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Group:     byohGroup,
				Resource:  "bootstrapkubeconfigs",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
		{
			name: "list ByoHosts in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "list",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), rbacharness.BootstrapSubject(), &tc.attrs)
				require.NoError(t, err)
				assert.Equal(t, tc.wantAllow, got)
			})
		}
	}
}

const otherHost = "h2-xyz99"

// tenantGroupSubject returns the identity of a certificate that holds only the
// group of tenant-a and its own user. A real certificate also holds
// byoh:hosts, which the cluster-wide binding still matches.
func tenantGroupSubject(user string) rbacharness.Subject {
	return rbacharness.Subject{
		User:   user,
		Groups: []string{identity.TenantGroupPrefix + rbacharness.TenantA, "system:authenticated"},
	}
}

// createHostScopedRBAC creates, through the approver's own builder, the Role and
// RoleBinding of the hosts h1-abc12 and h2-xyz99 in tenant-a. It returns once
// the authorizer reflects both, because it reads the objects from a cache.
func createHostScopedRBAC(t *testing.T, h *rbacharness.Harness, host1, host2 rbacharness.Subject) {
	t.Helper()

	hosts := []identity.HostIdentity{
		{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h1", Suffix: "abc12"},
		{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h2", Suffix: "xyz99"},
	}
	for _, host := range hosts {
		role, binding := controllers.HostScopedRBAC(host)
		err := h.CreateRBAC(t.Context(), role, binding)
		require.NoError(t, err)
	}

	for subject, name := range map[*rbacharness.Subject]string{&host1: ownHost, &host2: otherHost} {
		attrs := &authv1.ResourceAttributes{
			Verb:      "get",
			Group:     byohGroup,
			Resource:  "byohosts",
			Namespace: rbacharness.TenantA,
			Name:      name,
		}
		require.Eventually(t, func() bool {
			got, err := h.Allowed(t.Context(), *subject, attrs)
			return err == nil && got
		}, 10*time.Second, 100*time.Millisecond, "authorizer did not see the Role of %s", name)
	}
}

// TestRBAC_HostScopedRole checks the access of a host whose certificate holds
// the tenant group and its own user, against the Role and RoleBinding the
// approver creates for each host.
func TestRBAC_HostScopedRole(t *testing.T) {
	host1 := tenantGroupSubject("byoh:host:tenant-a:h1-abc12")
	host2 := tenantGroupSubject("byoh:host:tenant-a:h2-xyz99")

	selector := func(name string) *authv1.FieldSelectorAttributes {
		return &authv1.FieldSelectorAttributes{
			Requirements: []metav1.FieldSelectorRequirement{
				{
					Key:      "metadata.name",
					Operator: metav1.FieldSelectorOpIn,
					Values:   []string{name},
				},
			},
		}
	}
	byohost := func(verb, namespace, name string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{
			Verb:      verb,
			Group:     byohGroup,
			Resource:  "byohosts",
			Namespace: namespace,
			Name:      name,
		}
	}
	status := func(verb, name string) authv1.ResourceAttributes {
		attrs := byohost(verb, rbacharness.TenantA, name)
		attrs.Subresource = "status"

		return attrs
	}
	withSelector := func(attrs authv1.ResourceAttributes, name string) authv1.ResourceAttributes {
		attrs.FieldSelector = selector(name)

		return attrs
	}

	testCases := []struct {
		name    string
		subject rbacharness.Subject
		attrs   authv1.ResourceAttributes
		want    bool
	}{
		{name: "host 1 gets its ByoHost", subject: host1, attrs: byohost("get", rbacharness.TenantA, ownHost), want: true},
		{name: "host 1 updates its ByoHost", subject: host1, attrs: byohost("update", rbacharness.TenantA, ownHost), want: true},
		{name: "host 1 patches its ByoHost", subject: host1, attrs: byohost("patch", rbacharness.TenantA, ownHost), want: true},
		{name: "host 1 deletes its ByoHost", subject: host1, attrs: byohost("delete", rbacharness.TenantA, ownHost), want: true},
		{name: "host 1 gets its ByoHost status", subject: host1, attrs: status("get", ownHost), want: true},
		{name: "host 1 patches its ByoHost status", subject: host1, attrs: status("patch", ownHost), want: true},
		{
			name:    "host 1 lists ByoHosts with its name selector",
			subject: host1,
			attrs:   withSelector(byohost("list", rbacharness.TenantA, ownHost), ownHost),
			want:    true,
		},
		{
			name:    "host 1 watches ByoHosts with its name selector",
			subject: host1,
			attrs:   withSelector(byohost("watch", rbacharness.TenantA, ownHost), ownHost),
			want:    true,
		},
		{name: "host 2 gets its ByoHost", subject: host2, attrs: byohost("get", rbacharness.TenantA, otherHost), want: true},
		{name: "host 1 gets host 2's ByoHost", subject: host1, attrs: byohost("get", rbacharness.TenantA, otherHost), want: false},
		{name: "host 1 patches host 2's ByoHost", subject: host1, attrs: byohost("patch", rbacharness.TenantA, otherHost), want: false},
		{name: "host 1 deletes host 2's ByoHost", subject: host1, attrs: byohost("delete", rbacharness.TenantA, otherHost), want: false},
		{name: "host 1 patches host 2's ByoHost status", subject: host1, attrs: status("patch", otherHost), want: false},
		{
			name:    "host 1 lists ByoHosts with host 2's name selector",
			subject: host1,
			attrs:   withSelector(byohost("list", rbacharness.TenantA, otherHost), otherHost),
			want:    false,
		},
		{name: "host 1 lists ByoHosts without a name selector", subject: host1, attrs: byohost("list", rbacharness.TenantA, ""), want: false},
		{name: "host 2 gets host 1's ByoHost", subject: host2, attrs: byohost("get", rbacharness.TenantA, ownHost), want: false},
		{name: "host 1 gets its own name in tenant-b", subject: host1, attrs: byohost("get", rbacharness.TenantB, ownHost), want: false},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		createHostScopedRBAC(t, h, host1, host2)

		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), tc.subject, &tc.attrs)
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}

		t.Run(string(source), func(t *testing.T) {
			checkHostInformer(t, h, host1)
		})
	}
}

// checkHostInformer starts the cache of the agent, scoped to the tenant
// namespace and a ByoHost name selector, as host 1.
func checkHostInformer(t *testing.T, h *rbacharness.Harness, host1 rbacharness.Subject) {
	t.Helper()

	scheme := runtime.NewScheme()
	err := infrastructurev1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name     string
		selector fields.Selector
		wantSync bool
	}{
		{
			name:     "own ByoHost selector",
			selector: fields.SelectorFromSet(fields.Set{"metadata.name": ownHost}),
			wantSync: true,
		},
		{
			name:     "other host's ByoHost selector",
			selector: fields.SelectorFromSet(fields.Set{"metadata.name": otherHost}),
			wantSync: false,
		},
		{
			name:     "no selector",
			selector: fields.Everything(),
			wantSync: false,
		},
	}

	cfg, err := h.ClientConfig(host1)
	require.NoError(t, err)

	for _, tc := range testCases {
		t.Run("informer/"+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			c, err := cache.New(cfg, cache.Options{
				Scheme:            scheme,
				DefaultNamespaces: map[string]cache.Config{rbacharness.TenantA: {}},
				ByObject: map[client.Object]cache.ByObject{
					&infrastructurev1beta1.ByoHost{}: {Field: tc.selector},
				},
			})
			require.NoError(t, err)

			_, err = c.GetInformer(ctx, &infrastructurev1beta1.ByoHost{})
			require.NoError(t, err)

			go func() { _ = c.Start(ctx) }()

			assert.Equal(t, tc.wantSync, c.WaitForCacheSync(ctx))
		})
	}
}

// setHostSecretNames sets the Secret rule of the host-scoped Role of h1-abc12 to
// names, through the builder the manager uses, and creates the Role first when
// no other test has. It returns once the authorizer reflects the change: the
// host may get sentinel exactly when wantSentinel says so.
func setHostSecretNames(t *testing.T, h *rbacharness.Harness, host1 rbacharness.Subject, names []string, sentinel string, wantSentinel bool) {
	t.Helper()

	admin, err := kubernetes.NewForConfig(h.AdminConfig())
	require.NoError(t, err)

	roles := admin.RbacV1().Roles(rbacharness.TenantA)
	role, err := roles.Get(t.Context(), "byoh-host-"+ownHost, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		hostIdentity := identity.HostIdentity{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h1", Suffix: "abc12"}
		newRole, binding := controllers.HostScopedRBAC(hostIdentity)
		require.NoError(t, h.CreateRBAC(t.Context(), newRole, binding))
		role, err = roles.Get(t.Context(), newRole.Name, metav1.GetOptions{})
	}
	require.NoError(t, err)

	role.Rules = controllers.HostSecretRules(role.Rules, names)
	_, err = roles.Update(t.Context(), role, metav1.UpdateOptions{})
	require.NoError(t, err)

	attrs := &authv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: rbacharness.TenantA, Name: sentinel}
	require.Eventually(t, func() bool {
		got, reviewErr := h.Allowed(t.Context(), host1, attrs)
		return reviewErr == nil && got == wantSentinel
	}, 10*time.Second, 100*time.Millisecond, "authorizer did not reflect the Secret rule")
}

// TestRBAC_HostSecrets checks the Secrets a host may read once the manager sets
// the Secret rule of its Role to the Secrets of its binding.
func TestRBAC_HostSecrets(t *testing.T) {
	host1 := tenantGroupSubject("byoh:host:tenant-a:h1-abc12")
	secret := func(verb, namespace, name string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{Verb: verb, Resource: "secrets", Namespace: namespace, Name: name}
	}
	ownByoHost := authv1.ResourceAttributes{
		Verb:      "get",
		Group:     byohGroup,
		Resource:  "byohosts",
		Namespace: rbacharness.TenantA,
		Name:      ownHost,
	}

	type row struct {
		name  string
		attrs authv1.ResourceAttributes
		want  bool
	}
	bound := []row{
		{name: "gets the bootstrap Secret", attrs: secret("get", rbacharness.TenantA, "m1-bootstrap"), want: true},
		{name: "gets the installation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-install-m1"), want: true},
		{name: "gets the uninstallation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-uninstall-m1"), want: true},
		{name: "still gets its ByoHost", attrs: ownByoHost, want: true},
		{name: "does not get byoh-token", attrs: secret("get", rbacharness.TenantA, "byoh-token"), want: false},
		{name: "does not get a credential Secret", attrs: secret("get", rbacharness.TenantA, "h1-abc12-cred"), want: false},
		{name: "does not get byoh-bootstrap-kc", attrs: secret("get", rbacharness.TenantA, "byoh-bootstrap-kc"), want: false},
		{name: "does not get the Secrets of host 2", attrs: secret("get", rbacharness.TenantA, "m2-bootstrap"), want: false},
		{name: "does not get host 2's installation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-install-m2"), want: false},
		{name: "does not get host 2's uninstallation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-uninstall-m2"), want: false},
		{name: "does not get its Secret name in tenant-b", attrs: secret("get", rbacharness.TenantB, "m1-bootstrap"), want: false},
		{name: "does not list Secrets in tenant-a", attrs: secret("list", rbacharness.TenantA, ""), want: false},
		{name: "does not watch Secrets in tenant-a", attrs: secret("watch", rbacharness.TenantA, ""), want: false},
		{name: "does not list Secrets in tenant-b", attrs: secret("list", rbacharness.TenantB, ""), want: false},
		{name: "does not list Secrets in kube-system", attrs: secret("list", "kube-system", ""), want: false},
		{name: "does not list Secrets in kaapi", attrs: secret("list", "kaapi", ""), want: false},
		{name: "does not update its bootstrap Secret", attrs: secret("update", rbacharness.TenantA, "m1-bootstrap"), want: false},
	}
	released := []row{
		{name: "released host does not get the bootstrap Secret", attrs: secret("get", rbacharness.TenantA, "m1-bootstrap"), want: false},
		{name: "released host does not get the installation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-install-m1"), want: false},
		{name: "released host does not get the uninstallation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-uninstall-m1"), want: false},
		{name: "released host still gets its ByoHost", attrs: ownByoHost, want: true},
	}
	rebound := []row{
		{name: "second machine: gets the new bootstrap Secret", attrs: secret("get", rbacharness.TenantA, "m3-bootstrap"), want: true},
		{name: "second machine: gets the new installation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-install-m3"), want: true},
		{name: "second machine: gets the new uninstallation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-uninstall-m3"), want: true},
		{name: "second machine: does not get the old bootstrap Secret", attrs: secret("get", rbacharness.TenantA, "m1-bootstrap"), want: false},
		{name: "second machine: does not get the old installation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-install-m1"), want: false},
		{name: "second machine: does not get the old uninstallation Secret", attrs: secret("get", rbacharness.TenantA, "byoh-uninstall-m1"), want: false},
	}

	phases := []struct {
		name     string
		names    []string
		sentinel string
		want     bool
		rows     []row
	}{
		{name: "bound", names: []string{"m1-bootstrap", "byoh-install-m1", "byoh-uninstall-m1"}, sentinel: "m1-bootstrap", want: true, rows: bound},
		{name: "released", names: nil, sentinel: "m1-bootstrap", want: false, rows: released},
		{name: "rebound", names: []string{"m3-bootstrap", "byoh-install-m3", "byoh-uninstall-m3"}, sentinel: "m3-bootstrap", want: true, rows: rebound},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, phase := range phases {
			setHostSecretNames(t, h, host1, phase.names, phase.sentinel, phase.want)

			for _, tc := range phase.rows {
				t.Run(fmt.Sprintf("%s/%s/%s", source, phase.name, tc.name), func(t *testing.T) {
					got, err := h.Allowed(t.Context(), host1, &tc.attrs)
					require.NoError(t, err)
					assert.Equal(t, tc.want, got)
				})
			}
		}
	}
}
