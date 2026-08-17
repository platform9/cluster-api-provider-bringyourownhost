// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// patchHeartbeat sends lastHeartbeatTime/agentVersion/machineID as a raw
// status patch, independent of any patch.Helper a concurrent Reconcile
// call might be using — see Reconcile in host_reconciler.go.
func (r *HostReconciler) patchHeartbeat(ctx context.Context, hostName, namespace string) error {
	byoHost := &infrastructurev1beta1.ByoHost{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: hostName, Namespace: namespace}, byoHost); err != nil {
		return err
	}

	now := metav1.Now()
	if !heartbeatDue(byoHost.Status.LastHeartbeatTime, now, r.HeartbeatInterval) {
		return nil
	}

	machineID, machineIDErr := registration.GetMachineID(os.ReadFile)
	if machineIDErr != nil {
		ctrl.LoggerFrom(ctx).Error(machineIDErr, "failed to read /etc/machine-id")
	}
	patchBytes, err := buildHeartbeatPatch(now, version.Get().GitVersion, machineID, machineIDErr)
	if err != nil {
		return err
	}
	return r.Client.Status().Patch(ctx, byoHost, client.RawPatch(types.MergePatchType, patchBytes))
}

// heartbeatDue reports whether enough time has passed since last to send
// another heartbeat, so a burst of reconciles doesn't spam the API server.
func heartbeatDue(last *metav1.Time, now metav1.Time, interval time.Duration) bool {
	return last == nil || now.Sub(last.Time) >= interval
}

// buildHeartbeatPatch builds patchHeartbeat's raw JSON merge patch body.
// machineID is omitted entirely when machineIDErr != nil, so a transient
// read failure can't clobber a previously-known-good value.
func buildHeartbeatPatch(now metav1.Time, agentVersion, machineID string, machineIDErr error) ([]byte, error) {
	status := map[string]interface{}{
		"lastHeartbeatTime": now,
		"agentVersion":      agentVersion,
	}
	if machineIDErr == nil {
		status["machineID"] = machineID
	}
	return json.Marshal(map[string]interface{}{"status": status})
}

func (r *HostReconciler) pulseHeartbeat(ctx context.Context, hostName, namespace string) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.patchHeartbeat(ctx, hostName, namespace); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "failed to pulse heartbeat")
			}
		}
	}
}

// startHeartbeatPulse keeps the heartbeat fresh for the duration of a
// single blocking call (kubeadm install/join), bounded by
// MaxBlockingDuration (see its own doc comment). Callers defer the
// returned stop func around the blocking call.
func (r *HostReconciler) startHeartbeatPulse(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) func() {
	if r.MaxBlockingDuration <= 0 {
		return func() {}
	}
	pulseCtx, stop := context.WithTimeout(ctx, r.MaxBlockingDuration)
	go r.pulseHeartbeat(pulseCtx, byoHost.Name, byoHost.Namespace)
	return stop
}
