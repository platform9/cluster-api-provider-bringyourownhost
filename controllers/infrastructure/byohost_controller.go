// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"time"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ByoHostReconciler reconciles a ByoHost object
type ByoHostReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// HeartbeatTimeoutPeriod defines the duration after which the agent is
	// considered to be disconnected.  Its value can be overridden at start-up
	// via the --byohostagent-heartbeat-timeout flag in main.go.
	HeartbeatTimeoutPeriod time.Duration
}

// DefaultRetry is the recommended retry for a conflict where multiple clients ( byomachine in this case )
// are making changes to the same resource.
var DefaultRetry = wait.Backoff{
	Steps:    5,
	Duration: 10 * time.Millisecond,
	Factor:   1.0,
	Jitter:   0.1,
}

const (
	// ByohHostReconcilePeriod is the duration to wait before requeueing the ByoHost.
	ByohHostReconcilePeriod = 60 * time.Second
)

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ByoHost object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *ByoHostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)
	byoHost := &infrastructurev1beta1.ByoHost{}
	if err := r.Get(ctx, req.NamespacedName, byoHost); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if last heartbeat timeout is within the HeartbeatTimeoutPeriod
	if byoHost.Status.LastHeartbeatTime != nil && time.Since(byoHost.Status.LastHeartbeatTime.Time) < r.HeartbeatTimeoutPeriod {
		logger.Info("Heartbeat within timeout period")
		byoHost.Status.Connected = true
		conditions.MarkTrue(byoHost, infrastructurev1beta1.AgentConnectedCondition)
	} else {
		logger.Info("Heartbeat timeout detected", "HeartbeatTimeoutPeriod", r.HeartbeatTimeoutPeriod)
		byoHost.Status.Connected = false
		conditions.MarkFalse(byoHost, infrastructurev1beta1.AgentConnectedCondition, infrastructurev1beta1.HeartbeatTimeoutReason, clusterv1.ConditionSeverityWarning, "Heartbeat timeout detected")
	}

	// Update the ByoHost LastHeartbeatCheckTime
	now := metav1.Now()
	byoHost.Status.LastHeartbeatCheckTime = &now
	err := retry.RetryOnConflict(DefaultRetry, func() error {
		return r.Client.Status().Update(ctx, byoHost)
	})
	if err != nil {
		logger.Error(err, "Failed to update ByoHost status")
		return ctrl.Result{}, err
	}

	logger.Info("Reconcile request received")
	return ctrl.Result{RequeueAfter: ByohHostReconcilePeriod}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		Complete(r)
}
