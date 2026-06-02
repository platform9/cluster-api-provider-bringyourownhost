// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// ByoHostReconciler reconciles a ByoHost object
type ByoHostReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;delete

func (r *ByoHostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	byoHost := &infrastructurev1beta1.ByoHost{}
	if err := r.Get(ctx, req.NamespacedName, byoHost); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Delete the uninstall secret once the agent has completed cleanup.
	// The agent removes the cleanup annotation as its final step, so absence of
	// the annotation combined with no machineRef means the host is fully cleaned up.
	// The uninstall secret has no ownerReference (by design, to survive K8sInstallerConfig
	// deletion), so it must be explicitly deleted here by the manager.
	_, hasCleanupAnnotation := byoHost.GetAnnotations()[infrastructurev1beta1.HostCleanupAnnotation]
	if byoHost.Spec.UninstallationSecret != nil &&
		byoHost.Status.MachineRef == nil &&
		!hasCleanupAnnotation {

		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      byoHost.Spec.UninstallationSecret.Name,
			Namespace: byoHost.Spec.UninstallationSecret.Namespace,
		}, secret)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err == nil {
			if delErr := r.Delete(ctx, secret); delErr != nil && !apierrors.IsNotFound(delErr) {
				logger.Error(delErr, "failed to delete uninstallation secret", "secret", secret.Name)
				return ctrl.Result{}, delErr
			}
			logger.Info("deleted uninstallation secret", "secret", secret.Name)
		}

		// Clear the stale reference so re-used hosts get a fresh uninstall secret
		// on their next machine assignment.
		helper, patchErr := patch.NewHelper(byoHost, r.Client)
		if patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		byoHost.Spec.UninstallationSecret = nil
		if patchErr = helper.Patch(ctx, byoHost); patchErr != nil {
			logger.Error(patchErr, "failed to clear uninstallationSecret reference on ByoHost")
			return ctrl.Result{}, patchErr
		}
		logger.Info("cleared uninstallationSecret reference on ByoHost")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		Complete(r)
}
