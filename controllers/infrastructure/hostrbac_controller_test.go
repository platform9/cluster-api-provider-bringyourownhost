// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
)

const (
	hostRBACByoHostName = "worker-1-a1b2c"
	hostRBACOtherName   = "worker-2-d4e5f"
)

// hostRBACIssuer returns the BootstrapKubeconfig that owns the host-scoped
// objects of worker-1-a1b2c, with the given uid.
func hostRBACIssuer(uid types.UID) *infrastructurev1beta1.BootstrapKubeconfig {
	issuer := admissionIssuer(hostRBACByoHostName, admissionNamespace, admissionTokenID)
	issuer.UID = uid
	issuer.Status.ApprovedCommonName = admissionIdentity

	return issuer
}

func hostRBACByoHost(name string) *infrastructurev1beta1.ByoHost {
	return &infrastructurev1beta1.ByoHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: admissionNamespace,
			UID:       types.UID("uid-host-" + name),
		},
	}
}

// hostRBACObjects returns the Role and RoleBinding the approver created for
// the host, owned by the BootstrapKubeconfig with the given uid.
func hostRBACObjects(byoHostName string, issuerUID types.UID) (*rbacv1.Role, *rbacv1.RoleBinding) {
	role := wantHostRole()
	role.Name = "byoh-host-" + byoHostName
	role.ResourceVersion = ""
	role.OwnerReferences[0].UID = issuerUID

	binding := wantHostRoleBinding()
	binding.Name = role.Name
	binding.ResourceVersion = ""
	binding.RoleRef.Name = role.Name
	binding.OwnerReferences[0].UID = issuerUID

	return role, binding
}

// registeredHostRBAC returns the objects with the ByoHost added to the owners, as
// the reconciler leaves them once the host registered.
func registeredHostRBAC(byoHostName string, issuerUID types.UID) (*rbacv1.Role, *rbacv1.RoleBinding) {
	role, binding := hostRBACObjects(byoHostName, issuerUID)
	byoHostOwner := metav1.OwnerReference{
		APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
		Kind:       "ByoHost",
		Name:       byoHostName,
		UID:        types.UID("uid-host-" + byoHostName),
	}
	role.OwnerReferences = append(role.OwnerReferences, byoHostOwner)
	binding.OwnerReferences = append(binding.OwnerReferences, byoHostOwner)

	return role, binding
}

func ownerKinds(refs []metav1.OwnerReference) []string {
	kinds := make([]string, 0, len(refs))
	for _, ref := range refs {
		kinds = append(kinds, ref.Kind)
	}

	return kinds
}

func TestHostRBACReconciler_Reconcile(t *testing.T) {
	role, binding := hostRBACObjects(hostRBACByoHostName, "uid-issuer")

	registeredRole, registeredBinding := registeredHostRBAC(hostRBACByoHostName, "uid-issuer")
	otherRegisteredRole, otherRegisteredBinding := registeredHostRBAC(hostRBACOtherName, "uid-other-issuer")

	unlabelledRole := role.DeepCopy()
	unlabelledRole.Labels = nil

	testCases := []struct {
		name string
		objs []client.Object
		// getFails makes every ByoHost read fail.
		getFails      bool
		wantErr       bool
		wantRole      bool
		wantBinding   bool
		wantOtherKept bool
		wantOwners    []string
	}{
		{
			name:        "issuer gone and no ByoHost",
			objs:        []client.Object{role, binding},
			wantRole:    false,
			wantBinding: false,
		},
		{
			name:        "issuer gone and ByoHost exists",
			objs:        []client.Object{role, binding, hostRBACByoHost(hostRBACByoHostName)},
			wantRole:    true,
			wantBinding: true,
			wantOwners:  []string{"BootstrapKubeconfig", "ByoHost"},
		},
		{
			name:        "issuer exists and no ByoHost",
			objs:        []client.Object{role, binding, hostRBACIssuer("uid-issuer")},
			wantRole:    true,
			wantBinding: true,
			wantOwners:  []string{"BootstrapKubeconfig"},
		},
		{
			name:        "issuer and ByoHost exist",
			objs:        []client.Object{role, binding, hostRBACIssuer("uid-issuer"), hostRBACByoHost(hostRBACByoHostName)},
			wantRole:    true,
			wantBinding: true,
			wantOwners:  []string{"BootstrapKubeconfig", "ByoHost"},
		},
		{
			name:        "issuer replaced by one of the same name",
			objs:        []client.Object{role, binding, hostRBACIssuer("uid-new-issuer")},
			wantRole:    false,
			wantBinding: false,
		},
		{
			name:        "registered host deleted while issuer exists",
			objs:        []client.Object{registeredRole, registeredBinding, hostRBACIssuer("uid-issuer")},
			wantRole:    false,
			wantBinding: false,
		},
		{
			name:        "registered host deleted and issuer gone",
			objs:        []client.Object{registeredRole, registeredBinding},
			wantRole:    false,
			wantBinding: false,
		},
		{
			name:          "registered host deleted, other host stays",
			objs:          []client.Object{registeredRole, registeredBinding, otherRegisteredRole, otherRegisteredBinding, hostRBACByoHost(hostRBACOtherName)},
			wantRole:      false,
			wantBinding:   false,
			wantOtherKept: true,
		},
		{
			name:        "Role without the tenant label",
			objs:        []client.Object{unlabelledRole, binding},
			wantRole:    true,
			wantBinding: false,
			wantOwners:  []string{"BootstrapKubeconfig"},
		},
		{
			name:        "RoleBinding missing",
			objs:        []client.Object{role},
			wantRole:    false,
			wantBinding: false,
		},
		{
			name:        "ByoHost read fails",
			objs:        []client.Object{role, binding},
			getFails:    true,
			wantErr:     true,
			wantRole:    true,
			wantBinding: true,
			wantOwners:  []string{"BootstrapKubeconfig"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			funcs := interceptor.Funcs{}
			if tc.getFails {
				funcs.Get = func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*infrastructurev1beta1.ByoHost); ok {
						return errors.New("API server unavailable")
					}

					return cl.Get(ctx, key, obj, opts...)
				}
			}
			cl := newAdmissionClient(t, &funcs, tc.objs...)
			reconciler := &controllers.HostRBACReconciler{Client: cl, APIReader: cl}

			_, err := reconciler.Reconcile(t.Context(), reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: admissionNamespace, Name: hostRBACByoHostName},
			})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			key := client.ObjectKey{Namespace: admissionNamespace, Name: "byoh-host-" + hostRBACByoHostName}
			gotRole := &rbacv1.Role{}
			err = cl.Get(t.Context(), key, gotRole)
			if tc.wantRole {
				require.NoError(t, err)
				assert.ElementsMatch(t, tc.wantOwners, ownerKinds(gotRole.OwnerReferences))
			} else {
				assert.True(t, apierrors.IsNotFound(err), "Role must be removed, got %v", err)
			}

			gotBinding := &rbacv1.RoleBinding{}
			err = cl.Get(t.Context(), key, gotBinding)
			if tc.wantBinding {
				require.NoError(t, err)
				assert.ElementsMatch(t, tc.wantOwners, ownerKinds(gotBinding.OwnerReferences))
			} else {
				assert.True(t, apierrors.IsNotFound(err), "RoleBinding must be removed, got %v", err)
			}

			if tc.wantOtherKept {
				otherKey := client.ObjectKey{Namespace: admissionNamespace, Name: "byoh-host-" + hostRBACOtherName}
				err = cl.Get(t.Context(), otherKey, &rbacv1.Role{})
				assert.NoError(t, err, "the Role of another host must stay")
				err = cl.Get(t.Context(), otherKey, &rbacv1.RoleBinding{})
				assert.NoError(t, err, "the RoleBinding of another host must stay")
			}
		})
	}
}

func TestHostRBACReconciler_Reconcile_OwnerWrittenOnce(t *testing.T) {
	role, binding := hostRBACObjects(hostRBACByoHostName, "uid-issuer")
	cl := newAdmissionClient(t, &interceptor.Funcs{}, role, binding, hostRBACIssuer("uid-issuer"), hostRBACByoHost(hostRBACByoHostName))
	reconciler := &controllers.HostRBACReconciler{Client: cl, APIReader: cl}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: admissionNamespace, Name: hostRBACByoHostName}}
	key := client.ObjectKey{Namespace: admissionNamespace, Name: role.Name}

	_, err := reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	afterFirst := &rbacv1.Role{}
	err = cl.Get(t.Context(), key, afterFirst)
	require.NoError(t, err)

	_, err = reconciler.Reconcile(t.Context(), req)
	require.NoError(t, err)
	afterSecond := &rbacv1.Role{}
	err = cl.Get(t.Context(), key, afterSecond)
	require.NoError(t, err)

	assert.Equal(t, afterFirst.ResourceVersion, afterSecond.ResourceVersion, "a Role that already has the owner is not written")
	assert.Equal(t, role.Rules, afterSecond.Rules, "the rules stay as they were")
	assert.Equal(t, "ByoHost", afterSecond.OwnerReferences[len(afterSecond.OwnerReferences)-1].Kind)
	assert.Equal(t, hostRBACByoHostName, afterSecond.OwnerReferences[len(afterSecond.OwnerReferences)-1].Name)
}

func TestHostRBACRequests(t *testing.T) {
	approved := hostRBACIssuer("uid-issuer")
	notApproved := admissionIssuer(hostRBACByoHostName, admissionNamespace, admissionTokenID)
	badName := hostRBACIssuer("uid-issuer")
	badName.Status.ApprovedCommonName = "not-an-identity"

	wantRequest := []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: admissionNamespace, Name: hostRBACByoHostName},
	}}

	testCases := []struct {
		name string
		obj  client.Object
		want []reconcile.Request
	}{
		{
			name: "ByoHost",
			obj:  hostRBACByoHost(hostRBACByoHostName),
			want: wantRequest,
		},
		{
			name: "BootstrapKubeconfig with an approved common name",
			obj:  approved,
			want: wantRequest,
		},
		{
			name: "BootstrapKubeconfig without an approved common name",
			obj:  notApproved,
			want: nil,
		},
		{
			name: "BootstrapKubeconfig with a common name that is not an identity",
			obj:  badName,
			want: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := controllers.HostRBACRequests(t.Context(), tc.obj)
			assert.Equal(t, tc.want, got)
		})
	}
}
