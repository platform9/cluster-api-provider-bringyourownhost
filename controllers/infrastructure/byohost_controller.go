// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
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
	// HeartbeatTimeoutPeriod defines the duration after which the agent is
	// considered to be disconnected.  Its value can be overridden at start-up
	// via the --byohostagent-heartbeat-timeout flag in main.go.
	HeartbeatTimeoutPeriod time.Duration
	Recorder               record.EventRecorder
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *ByoHostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	byoHost := &infrastructurev1beta1.ByoHost{}
	if err := r.Get(ctx, req.NamespacedName, byoHost); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	helper, err := patch.NewHelper(byoHost, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if patchErr := helper.Patch(ctx, byoHost); patchErr != nil && reterr == nil {
			reterr = patchErr
		}
	}()

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
				return ctrl.Result{}, fmt.Errorf("failed to delete uninstallation secret %s: %w", secret.Name, delErr)
			}
			logger.Info("deleted uninstallation secret", "secret", secret.Name)
		}

		byoHost.Spec.UninstallationSecret = nil
		logger.Info("cleared uninstallationSecret reference on ByoHost")
	}

	r.reconcileHeartbeat(ctx, byoHost)

	logger.Info("Reconcile request received")
	return ctrl.Result{RequeueAfter: r.HeartbeatTimeoutPeriod / 2}, nil
}

// lastHeartbeatWriteTime scans for whichever managedFields entry owns the
// status.lastHeartbeatTime field path, since server-side field tracking
// transfers ownership of a field to its most recent writer -- rather than
// matching a specific manager name, so this doesn't depend on knowing the
// agent's client identity in advance.
func lastHeartbeatWriteTime(byoHost *infrastructurev1beta1.ByoHost) *metav1.Time {
	for i := range byoHost.ManagedFields {
		entry := &byoHost.ManagedFields[i]
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		var fields struct {
			Status struct {
				LastHeartbeatTime json.RawMessage `json:"f:lastHeartbeatTime"`
			} `json:"f:status"`
		}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			continue
		}
		if fields.Status.LastHeartbeatTime != nil {
			return entry.Time
		}
	}
	return nil
}

// reconcileHeartbeat evaluates whether the host agent's last heartbeat is
// within HeartbeatTimeoutPeriod, sets AgentConnected accordingly, and emits
// an Event only on an actual connect/disconnect transition.
func (r *ByoHostReconciler) reconcileHeartbeat(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) {
	logger := log.FromContext(ctx)

	wasConnected := conditions.IsTrue(byoHost, infrastructurev1beta1.AgentConnected)
	writeTime := lastHeartbeatWriteTime(byoHost)
	if writeTime != nil && time.Since(writeTime.Time) < r.HeartbeatTimeoutPeriod {
		conditions.MarkTrue(byoHost, infrastructurev1beta1.AgentConnected)
	} else {
		logger.Info("Heartbeat timeout detected", "HeartbeatTimeoutPeriod", r.HeartbeatTimeoutPeriod)
		conditions.MarkFalse(byoHost, infrastructurev1beta1.AgentConnected, infrastructurev1beta1.HeartbeatTimeoutReason, clusterv1.ConditionSeverityWarning, "Heartbeat timeout detected")
	}

	if r.Recorder == nil {
		return
	}
	isConnectedNow := conditions.IsTrue(byoHost, infrastructurev1beta1.AgentConnected)
	switch {
	case isConnectedNow && !wasConnected:
		r.Recorder.Event(byoHost, corev1.EventTypeNormal, "AgentConnected", "agent heartbeat received")
	case !isConnectedNow && wasConnected:
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "AgentDisconnected", "agent heartbeat not received within timeout period")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		Complete(r)
}
