// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	certv1 "k8s.io/api/certificates/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/rbacharness"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
)

const (
	rbacAPIGroup  = "rbac.authorization.k8s.io"
	authenticated = "system:authenticated"
	maxLifetime   = int32(3600)
)

// managerIdentity finds the ServiceAccount that the shipped manager
// ClusterRole is bound to, and returns its ClusterRole name and the identity
// its token authenticates as.
func managerIdentity(t *testing.T, admin kubernetes.Interface) (string, rbacharness.Subject) {
	t.Helper()

	roles, err := admin.RbacV1().ClusterRoles().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)

	roleName := ""
	for i := range roles.Items {
		role := &roles.Items[i]
		managesRoles := false
		managesByoHosts := false
		for _, rule := range role.Rules {
			managesRoles = managesRoles || (slices.Contains(rule.APIGroups, rbacAPIGroup) && slices.Contains(rule.Resources, "roles"))
			managesByoHosts = managesByoHosts || slices.Contains(rule.Resources, "byohosts")
		}
		if managesRoles && managesByoHosts {
			roleName = role.Name
		}
	}
	require.NotEmpty(t, roleName, "no shipped ClusterRole grants roles and byohosts")

	bindings, err := admin.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)

	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if binding.RoleRef.Name != roleName {
			continue
		}
		for _, subject := range binding.Subjects {
			if subject.Kind == rbacv1.ServiceAccountKind {
				return roleName, rbacharness.Subject{
					User:   fmt.Sprintf("system:serviceaccount:%s:%s", subject.Namespace, subject.Name),
					Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + subject.Namespace, authenticated},
				}
			}
		}
	}
	require.FailNow(t, "no ServiceAccount is bound to the manager ClusterRole")

	return "", rbacharness.Subject{}
}

// certificateRequestPEM returns a PEM certificate request for commonName with
// the organizations of a host in tenant.
func certificateRequestPEM(t *testing.T, commonName, tenant string) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName, Organization: identity.Organization(tenant)},
	}, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// hostOnboarding is one host that asks for a certificate.
type hostOnboarding struct {
	host    identity.HostIdentity
	tokenID string
	csrName string
}

// startOnboarding creates the BootstrapKubeconfig that issued the token of
// onboarding, and, as that token, the CSR of the host.
func startOnboarding(t *testing.T, h *rbacharness.Harness, adminClient client.Client, onboarding *hostOnboarding) {
	t.Helper()

	issuer := &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: onboarding.host.ByoHostName(), Namespace: onboarding.host.TenantNamespace},
		Spec:       infrastructurev1beta1.BootstrapKubeconfigSpec{HostName: onboarding.host.NormalizedHostName},
	}
	err := adminClient.Create(t.Context(), issuer)
	require.NoError(t, err)

	issuer.Status.TokenID = onboarding.tokenID
	err = adminClient.Status().Update(t.Context(), issuer)
	require.NoError(t, err)

	bootstrap := rbacharness.BootstrapSubject()
	bootstrap.User = "system:bootstrap:" + onboarding.tokenID
	cfg, err := h.ClientConfig(bootstrap)
	require.NoError(t, err)
	bootstrapKube, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)

	csr := &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: onboarding.csrName},
		Spec: certv1.CertificateSigningRequestSpec{
			Request:           certificateRequestPEM(t, onboarding.host.String(), onboarding.host.TenantNamespace),
			SignerName:        certv1.KubeAPIServerClientSignerName,
			Usages:            []certv1.KeyUsage{certv1.UsageClientAuth},
			ExpirationSeconds: ptr.To(maxLifetime),
		},
	}
	_, err = bootstrapKube.CertificatesV1().CertificateSigningRequests().Create(t.Context(), csr, metav1.CreateOptions{})
	require.NoError(t, err)
}

// reconcileAsManager runs the approver for the CSR named csrName with every
// client authenticated as the manager ServiceAccount.
func reconcileAsManager(ctx context.Context, h *rbacharness.Harness, scheme *runtime.Scheme, manager rbacharness.Subject, csrName string) error {
	cfg, err := h.ClientConfig(manager)
	if err != nil {
		return err
	}

	direct, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}

	// The manager reads BootstrapKubeconfigs by token id through a cache index.
	// A direct client has no index, so the list is filtered here instead.
	managerClient := interceptor.NewClient(direct, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			issuers, ok := list.(*infrastructurev1beta1.BootstrapKubeconfigList)
			listOpts := &client.ListOptions{}
			listOpts.ApplyOptions(opts)
			if !ok || listOpts.FieldSelector == nil {
				return c.List(ctx, list, opts...)
			}

			listErr := c.List(ctx, issuers, client.InNamespace(listOpts.Namespace))
			if listErr != nil {
				return listErr
			}
			tokenID := listOpts.FieldSelector.Requirements()[0].Value
			issuers.Items = slices.DeleteFunc(issuers.Items, func(issuer infrastructurev1beta1.BootstrapKubeconfig) bool {
				return issuer.Status.TokenID != tokenID
			})

			return nil
		},
	})

	managerKube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	reconciler := &controllers.ByoAdmissionReconciler{
		ClientSet:                     managerKube,
		Client:                        managerClient,
		APIReader:                     managerClient,
		MaxCertificateLifetimeSeconds: maxLifetime,
	}
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: csrName}})

	return err
}

// TestRBAC_ManagerCreatesHostRBAC runs the approver as the manager
// ServiceAccount against the shipped manager RBAC. A fake client or an admin
// client cannot show whether the API server lets the manager create the host
// Role, because it checks that the manager holds every right the Role grants.
func TestRBAC_ManagerCreatesHostRBAC(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, infrastructurev1beta1.AddToScheme(scheme))

	for _, source := range rbacharness.Sources() {
		t.Run(string(source), func(t *testing.T) {
			h, err := rbacharness.Start(t.Context(), source)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, h.Stop()) })

			admin, err := kubernetes.NewForConfig(h.AdminConfig())
			require.NoError(t, err)
			adminClient, err := client.New(h.AdminConfig(), client.Options{Scheme: scheme})
			require.NoError(t, err)

			clusterRoleName, manager := managerIdentity(t, admin)
			createRoles := &authv1.ResourceAttributes{Verb: "create", Group: rbacAPIGroup, Resource: "roles", Namespace: rbacharness.TenantA}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), manager, createRoles)
				return reviewErr == nil && got
			}, 10*time.Second, 100*time.Millisecond)

			host1 := identity.HostIdentity{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h1", Suffix: "abc12"}
			startOnboarding(t, h, adminClient, &hostOnboarding{host: host1, tokenID: "abcdef", csrName: "byoh-csr-h1"})

			err = reconcileAsManager(t.Context(), h, scheme, manager, "byoh-csr-h1")
			require.NoError(t, err)

			csr, err := admin.CertificatesV1().CertificateSigningRequests().Get(t.Context(), "byoh-csr-h1", metav1.GetOptions{})
			require.NoError(t, err)
			require.Len(t, csr.Status.Conditions, 1)
			assert.Equal(t, certv1.CertificateApproved, csr.Status.Conditions[0].Type)

			_, err = admin.RbacV1().Roles(rbacharness.TenantA).Get(t.Context(), "byoh-host-h1-abc12", metav1.GetOptions{})
			require.NoError(t, err)
			_, err = admin.RbacV1().RoleBindings(rbacharness.TenantA).Get(t.Context(), "byoh-host-h1-abc12", metav1.GetOptions{})
			require.NoError(t, err)

			subject := tenantGroupSubject(host1.String())
			ownGet := &authv1.ResourceAttributes{Verb: "get", Group: byohGroup, Resource: "byohosts", Namespace: rbacharness.TenantA, Name: ownHost}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), subject, ownGet)
				return reviewErr == nil && got
			}, 10*time.Second, 100*time.Millisecond)

			otherGet := &authv1.ResourceAttributes{Verb: "get", Group: byohGroup, Resource: "byohosts", Namespace: rbacharness.TenantA, Name: otherHost}
			got, err := h.Allowed(t.Context(), subject, otherGet)
			require.NoError(t, err)
			assert.False(t, got)

			// Take delete on ByoHosts away from the manager. The Role grants it,
			// so the API server must now refuse the Role as an escalation.
			clusterRole, err := admin.RbacV1().ClusterRoles().Get(t.Context(), clusterRoleName, metav1.GetOptions{})
			require.NoError(t, err)
			for i := range clusterRole.Rules {
				if slices.Contains(clusterRole.Rules[i].Resources, "byohosts") {
					clusterRole.Rules[i].Verbs = slices.DeleteFunc(clusterRole.Rules[i].Verbs, func(verb string) bool { return verb == "delete" })
				}
			}
			_, err = admin.RbacV1().ClusterRoles().Update(t.Context(), clusterRole, metav1.UpdateOptions{})
			require.NoError(t, err)

			deleteByoHost := &authv1.ResourceAttributes{Verb: "delete", Group: byohGroup, Resource: "byohosts", Namespace: rbacharness.TenantA}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), manager, deleteByoHost)
				return reviewErr == nil && !got
			}, 10*time.Second, 100*time.Millisecond)

			host3 := identity.HostIdentity{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h3", Suffix: "qrs45"}
			startOnboarding(t, h, adminClient, &hostOnboarding{host: host3, tokenID: "ghijkl", csrName: "byoh-csr-h3"})

			err = reconcileAsManager(t.Context(), h, scheme, manager, "byoh-csr-h3")
			require.Error(t, err)
			assert.True(t, apierrors.IsForbidden(err), "want a forbidden error, got %v", err)
			assert.Contains(t, err.Error(), "attempting to grant RBAC permissions not currently held")

			csr, err = admin.CertificatesV1().CertificateSigningRequests().Get(t.Context(), "byoh-csr-h3", metav1.GetOptions{})
			require.NoError(t, err)
			assert.Empty(t, csr.Status.Conditions)
		})
	}
}

// TestRBAC_ManagerGrantsHostSecrets updates the host Role as the manager
// ServiceAccount against the shipped manager RBAC. The API server accepts the
// Secret rule only while the manager itself holds get on Secrets.
func TestRBAC_ManagerGrantsHostSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, infrastructurev1beta1.AddToScheme(scheme))

	for _, source := range rbacharness.Sources() {
		t.Run(string(source), func(t *testing.T) {
			h, err := rbacharness.Start(t.Context(), source)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, h.Stop()) })

			admin, err := kubernetes.NewForConfig(h.AdminConfig())
			require.NoError(t, err)
			adminClient, err := client.New(h.AdminConfig(), client.Options{Scheme: scheme})
			require.NoError(t, err)

			clusterRoleName, manager := managerIdentity(t, admin)
			updateRoles := &authv1.ResourceAttributes{Verb: "update", Group: rbacAPIGroup, Resource: "roles", Namespace: rbacharness.TenantA}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), manager, updateRoles)
				return reviewErr == nil && got
			}, 10*time.Second, 100*time.Millisecond)

			host1 := identity.HostIdentity{TenantNamespace: rbacharness.TenantA, NormalizedHostName: "h1", Suffix: "abc12"}
			startOnboarding(t, h, adminClient, &hostOnboarding{host: host1, tokenID: "abcdef", csrName: "byoh-csr-h1"})
			err = reconcileAsManager(t.Context(), h, scheme, manager, "byoh-csr-h1")
			require.NoError(t, err)

			managerCfg, err := h.ClientConfig(manager)
			require.NoError(t, err)
			managerKube, err := kubernetes.NewForConfig(managerCfg)
			require.NoError(t, err)

			setSecrets := func(names []string) error {
				role, getErr := managerKube.RbacV1().Roles(rbacharness.TenantA).Get(t.Context(), "byoh-host-h1-abc12", metav1.GetOptions{})
				require.NoError(t, getErr)
				role.Rules = controllers.HostSecretRules(role.Rules, names)
				_, updateErr := managerKube.RbacV1().Roles(rbacharness.TenantA).Update(t.Context(), role, metav1.UpdateOptions{})

				return updateErr
			}

			err = setSecrets([]string{"m1-bootstrap", "byoh-install-m1", "byoh-uninstall-m1"})
			require.NoError(t, err)

			subject := tenantGroupSubject(host1.String())
			getBootstrap := &authv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: rbacharness.TenantA, Name: "m1-bootstrap"}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), subject, getBootstrap)
				return reviewErr == nil && got
			}, 10*time.Second, 100*time.Millisecond)

			// Take get on Secrets away from the manager. The rule grants it, so
			// the API server must now refuse the new names as an escalation.
			clusterRole, err := admin.RbacV1().ClusterRoles().Get(t.Context(), clusterRoleName, metav1.GetOptions{})
			require.NoError(t, err)
			for i := range clusterRole.Rules {
				if slices.Contains(clusterRole.Rules[i].Resources, "secrets") {
					clusterRole.Rules[i].Verbs = slices.DeleteFunc(clusterRole.Rules[i].Verbs, func(verb string) bool { return verb == "get" })
				}
			}
			_, err = admin.RbacV1().ClusterRoles().Update(t.Context(), clusterRole, metav1.UpdateOptions{})
			require.NoError(t, err)

			getSecrets := &authv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: rbacharness.TenantA}
			require.Eventually(t, func() bool {
				got, reviewErr := h.Allowed(t.Context(), manager, getSecrets)
				return reviewErr == nil && !got
			}, 10*time.Second, 100*time.Millisecond)

			err = setSecrets([]string{"m2-bootstrap"})
			require.Error(t, err)
			assert.True(t, apierrors.IsForbidden(err), "want a forbidden error, got %v", err)
			assert.Contains(t, err.Error(), "attempting to grant RBAC permissions not currently held")
		})
	}
}

// managerAccessCase is one access review of the manager ServiceAccount.
type managerAccessCase struct {
	name      string
	attrs     authv1.ResourceAttributes
	wantAllow bool
}

// assertManagerAccess runs every case as the manager ServiceAccount against
// the shipped manager RBAC of each source. The authorizer reads informer
// caches, so the first allowed case is polled until the shipped rules are
// visible. Denied cases are checked once afterwards.
func assertManagerAccess(t *testing.T, cases []managerAccessCase) {
	t.Helper()

	for _, source := range rbacharness.Sources() {
		t.Run(string(source), func(t *testing.T) {
			h, err := rbacharness.Start(t.Context(), source)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, h.Stop()) })

			admin, err := kubernetes.NewForConfig(h.AdminConfig())
			require.NoError(t, err)
			_, manager := managerIdentity(t, admin)

			for i := range cases {
				tc := &cases[i]
				t.Run(tc.name, func(t *testing.T) {
					if tc.wantAllow {
						require.Eventually(t, func() bool {
							got, reviewErr := h.Allowed(t.Context(), manager, &tc.attrs)
							return reviewErr == nil && got
						}, 10*time.Second, 100*time.Millisecond)

						return
					}

					got, reviewErr := h.Allowed(t.Context(), manager, &tc.attrs)
					require.NoError(t, reviewErr)
					assert.False(t, got)
				})
			}
		})
	}
}

// TestRBAC_ManagerNamespaces checks that the manager can read a Namespace and
// nothing more. Denied cases run after the allowed one in each source, so a
// denial is not an artifact of a cache that has not synced.
func TestRBAC_ManagerNamespaces(t *testing.T) {
	namespaces := func(verb string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{Verb: verb, Resource: "namespaces"}
	}

	assertManagerAccess(t, []managerAccessCase{
		{name: "get namespaces", attrs: namespaces("get"), wantAllow: true},
		{name: "list namespaces", attrs: namespaces("list"), wantAllow: false},
		{name: "watch namespaces", attrs: namespaces("watch"), wantAllow: false},
		{name: "update namespaces", attrs: namespaces("update"), wantAllow: false},
		{name: "delete namespaces", attrs: namespaces("delete"), wantAllow: false},
	})
}

// TestRBAC_ManagerApproveSigner checks that the manager may approve only the
// kube-apiserver-client signer. Approving a CSR of another signer is refused
// by the API server with Forbidden, so the approver must not touch those.
func TestRBAC_ManagerApproveSigner(t *testing.T) {
	approve := func(signer string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{
			Verb:     "approve",
			Group:    "certificates.k8s.io",
			Resource: "signers",
			Name:     signer,
		}
	}

	assertManagerAccess(t, []managerAccessCase{
		{name: "approve kube-apiserver-client", attrs: approve("kubernetes.io/kube-apiserver-client"), wantAllow: true},
		{name: "approve kube-apiserver-client-kubelet", attrs: approve("kubernetes.io/kube-apiserver-client-kubelet"), wantAllow: false},
	})
}
