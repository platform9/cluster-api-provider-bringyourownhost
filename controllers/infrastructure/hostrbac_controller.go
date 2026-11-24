// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

// HostRBACReconciler owns the lifetime of the host-scoped Role and RoleBinding
// that the approver creates. Once the host has a ByoHost, the ByoHost owns them
// too, and they are removed when it is deleted. A host that never registers
// keeps them until its BootstrapKubeconfig is deleted. A running host never
// loses its Role.
//
// A request names the ByoHost of the host. Its namespace is the tenant
// namespace that holds the Role.
type HostRBACReconciler struct {
	Client client.Client

	// APIReader reads straight from the API server. The decision to delete must
	// not rest on a cache that has not seen a new ByoHost yet.
	APIReader client.Reader
}

// HostRBACRequests maps a ByoHost or a BootstrapKubeconfig to the request for
// the host-scoped objects of its host. A BootstrapKubeconfig that has approved
// no common name has no host-scoped objects.
func HostRBACRequests(_ context.Context, obj client.Object) []reconcile.Request {
	switch o := obj.(type) {
	case *infrastructurev1beta1.ByoHost:
		return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
	case *infrastructurev1beta1.BootstrapKubeconfig:
		host, err := identity.Parse(o.Status.ApprovedCommonName)
		if err != nil {
			return nil
		}

		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Namespace: host.TenantNamespace,
			Name:      host.ByoHostName(),
		}}}
	default:
		return nil
	}
}

// Reconcile adds the ByoHost as owner of the host-scoped Role and RoleBinding
// when it exists, and deletes both when no owner is left.
func (r *HostRBACReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	byoHost := &infrastructurev1beta1.ByoHost{}
	err := r.APIReader.Get(ctx, req.NamespacedName, byoHost)
	hostGone := apierrors.IsNotFound(err)
	if err != nil && !hostGone {
		return reconcile.Result{}, fmt.Errorf("get ByoHost %q: %w", req.Name, err)
	}

	for _, obj := range []client.Object{&rbacv1.Role{}, &rbacv1.RoleBinding{}} {
		key := client.ObjectKey{Namespace: req.Namespace, Name: hostRBACName(req.Name)}
		err = r.APIReader.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("get %T %q: %w", obj, key.Name, err)
		}

		// Only the objects the approver labels for this tenant are host-scoped.
		if obj.GetLabels()[identity.TenantLabel] != req.Namespace {
			continue
		}

		if !hostGone {
			err = r.ownByoHost(ctx, byoHost, obj)
		} else {
			err = r.deleteIfUnowned(ctx, obj, req.Name)
		}
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// ownByoHost adds byoHost to the owners of obj when it is not one yet.
func (r *HostRBACReconciler) ownByoHost(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost, obj client.Object) error {
	scheme := r.Client.Scheme()

	owned, err := controllerutil.HasOwnerReference(obj.GetOwnerReferences(), byoHost, scheme)
	if err != nil {
		return fmt.Errorf("check owners of %T %q: %w", obj, obj.GetName(), err)
	}
	if owned {
		return nil
	}

	err = controllerutil.SetOwnerReference(byoHost, obj, scheme)
	if err != nil {
		return fmt.Errorf("set ByoHost %q as owner of %T %q: %w", byoHost.Name, obj, obj.GetName(), err)
	}

	err = r.Client.Update(ctx, obj)
	if err != nil {
		return fmt.Errorf("update owners of %T %q: %w", obj, obj.GetName(), err)
	}

	return nil
}

// deleteIfUnowned deletes obj when its host is gone. The caller has checked that
// the ByoHost named byoHostName is gone. A ByoHost owner on obj records that the
// host registered, so its deletion ends the host. Otherwise the host never
// registered, and obj stays while a BootstrapKubeconfig that owns it exists. An
// issuer recreated under the same name has another UID and does not count.
func (r *HostRBACReconciler) deleteIfUnowned(ctx context.Context, obj client.Object, byoHostName string) error {
	for _, owner := range obj.GetOwnerReferences() {
		if ownerGroupKind(&owner) == "ByoHost" && owner.Name == byoHostName {
			return r.delete(ctx, obj)
		}
	}

	for _, owner := range obj.GetOwnerReferences() {
		if ownerGroupKind(&owner) != "BootstrapKubeconfig" {
			continue
		}

		issuer := &infrastructurev1beta1.BootstrapKubeconfig{}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: owner.Name}, issuer)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("get BootstrapKubeconfig %q: %w", owner.Name, err)
		}
		if issuer.UID == owner.UID {
			return nil
		}
	}

	return r.delete(ctx, obj)
}

// ownerGroupKind returns the kind of owner when it is an object of this API
// group, and an empty string otherwise.
func ownerGroupKind(owner *metav1.OwnerReference) string {
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil || gv.Group != infrastructurev1beta1.GroupVersion.Group {
		return ""
	}

	return owner.Kind
}

// delete deletes obj. The resource version precondition keeps an object that
// changed since the read, for example by a new rule, until the next reconcile
// checks it again.
func (r *HostRBACReconciler) delete(ctx context.Context, obj client.Object) error {
	resourceVersion := obj.GetResourceVersion()
	err := r.Client.Delete(ctx, obj, client.Preconditions{ResourceVersion: &resourceVersion})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %T %q: %w", obj, obj.GetName(), err)
	}

	return nil
}

// SetupWithManager watches the ByoHosts and BootstrapKubeconfigs of the
// manager. A ByoHost matters when it appears, to become an owner, and when it
// goes. A BootstrapKubeconfig matters when it goes.
func (r *HostRBACReconciler) SetupWithManager(mgr ctrl.Manager) error {
	byoHostEvents := predicate.Funcs{
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	issuerEvents := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("hostrbac").
		Watches(&infrastructurev1beta1.ByoHost{}, handler.EnqueueRequestsFromMapFunc(HostRBACRequests), builder.WithPredicates(byoHostEvents)).
		Watches(&infrastructurev1beta1.BootstrapKubeconfig{}, handler.EnqueueRequestsFromMapFunc(HostRBACRequests), builder.WithPredicates(issuerEvents)).
		Complete(r)
}
