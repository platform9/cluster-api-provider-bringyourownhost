// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	defaultMaxUnavailable = 1
	defaultPerHostTimeout = 10 * time.Minute
	// requeueInterval paces reconciles while a rollout is Pending/Progressing
	// so PerHostTimeout gets checked even without a triggering ByoHost event.
	requeueInterval = 15 * time.Second
)

// ByoHostAgentUpgradeReconciler reconciles a ByoHostAgentUpgrade object. See
// docs/proposals/agent-self-upgrade-adr.md §2.3.
type ByoHostAgentUpgradeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostagentupgrades,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostagentupgrades/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostagentupgrades/finalizers,verbs=update
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *ByoHostAgentUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	upgrade := &infrastructurev1beta1.ByoHostAgentUpgrade{}
	if err := r.Get(ctx, req.NamespacedName, upgrade); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Terminal phases never resume on their own - matches
	// docs/proposals/agent-self-upgrade-adr.md §2.3 step 4/§2.4: halting on
	// failure is permanent, requiring an explicit new object to retry or
	// roll back, never automatic re-selection by this same object.
	if upgrade.Status.Phase == infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed ||
		upgrade.Status.Phase == infrastructurev1beta1.ByoHostAgentUpgradePhaseCompleted {
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(upgrade, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if patchErr := helper.Patch(ctx, upgrade); patchErr != nil && reterr == nil {
			reterr = patchErr
		}
	}()

	hosts, err := r.listMatchingHosts(ctx, upgrade)
	if err != nil {
		return ctrl.Result{}, err
	}
	upgrade.Status.Total = clampToInt32(len(hosts))

	maxUnavailable, err := resolveMaxUnavailable(upgrade, len(hosts))
	if err != nil {
		return ctrl.Result{}, err
	}
	perHostTimeout := resolvePerHostTimeout(upgrade)

	summary := summarizeHosts(hosts, upgrade.Spec.TargetVersion, perHostTimeout)
	upgrade.Status.Upgraded = clampToInt32(len(summary.converged))

	if len(summary.failed) > 0 {
		return r.markFailed(upgrade, summary.failed)
	}

	if len(summary.unavailable) >= maxUnavailable {
		upgrade.Status.UnavailableCount = clampToInt32(len(summary.unavailable))
		conditions.MarkFalse(upgrade, infrastructurev1beta1.RolloutAvailable, infrastructurev1beta1.InsufficientAvailabilityBudgetReason, clusterv1.ConditionSeverityWarning, "")
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	conditions.MarkTrue(upgrade, infrastructurev1beta1.RolloutAvailable)

	picked, err := r.pickAndAssign(ctx, upgrade, &summary, maxUnavailable)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Reflect this tick's own picks immediately rather than waiting for the
	// next reconcile to notice them - summary alone only knows about state
	// from before this tick started picking.
	allInFlight := append(append([]*infrastructurev1beta1.ByoHost{}, summary.inFlight...), picked...)
	upgrade.Status.UnavailableCount = clampToInt32(len(summary.unavailable) + len(picked))

	if len(summary.converged) == len(hosts) && len(hosts) > 0 {
		upgrade.Status.Phase = infrastructurev1beta1.ByoHostAgentUpgradePhaseCompleted
		return ctrl.Result{}, nil
	}
	if len(allInFlight) > 0 {
		upgrade.Status.Phase = infrastructurev1beta1.ByoHostAgentUpgradePhaseProgressing
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *ByoHostAgentUpgradeReconciler) listMatchingHosts(ctx context.Context, upgrade *infrastructurev1beta1.ByoHostAgentUpgrade) ([]infrastructurev1beta1.ByoHost, error) {
	selector, err := metav1.LabelSelectorAsSelector(&upgrade.Spec.Selector)
	if err != nil {
		log.FromContext(ctx).Error(err, "invalid selector")
		return nil, err
	}
	hostList := &infrastructurev1beta1.ByoHostList{}
	if err := r.List(ctx, hostList, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     upgrade.Namespace,
	}); err != nil {
		return nil, err
	}
	return hostList.Items, nil
}

func resolveMaxUnavailable(upgrade *infrastructurev1beta1.ByoHostAgentUpgrade, total int) (int, error) {
	if upgrade.Spec.MaxUnavailable == nil {
		return defaultMaxUnavailable, nil
	}
	return intstr.GetScaledValueFromIntOrPercent(upgrade.Spec.MaxUnavailable, total, true)
}

func resolvePerHostTimeout(upgrade *infrastructurev1beta1.ByoHostAgentUpgrade) time.Duration {
	if upgrade.Spec.PerHostTimeout != nil {
		return upgrade.Spec.PerHostTimeout.Duration
	}
	return defaultPerHostTimeout
}

func (r *ByoHostAgentUpgradeReconciler) pickAndAssign(ctx context.Context, upgrade *infrastructurev1beta1.ByoHostAgentUpgrade, summary *hostSummary, maxUnavailable int) ([]*infrastructurev1beta1.ByoHost, error) {
	candidates := make([]*infrastructurev1beta1.ByoHost, 0, len(summary.notYetUpgraded))
	for _, host := range summary.notYetUpgraded {
		if _, unavailable := summary.unavailable[host.Name]; !unavailable {
			candidates = append(candidates, host)
		}
	}
	picked := pickHosts(candidates, maxUnavailable-len(summary.unavailable))
	for _, host := range picked {
		host.Spec.DesiredAgent = &infrastructurev1beta1.DesiredAgentSpec{
			Version:         upgrade.Spec.TargetVersion,
			PackageURL:      upgrade.Spec.PackageURL,
			PackageChecksum: upgrade.Spec.PackageChecksum,
			AssignedAt:      metav1.Now(),
		}
		if err := r.Update(ctx, host); err != nil {
			log.FromContext(ctx).Error(err, "failed to assign agent upgrade to host", "host", host.Name)
			return nil, err
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(upgrade, corev1.EventTypeNormal, "HostSelected", "assigned agent upgrade to host %s", host.Name)
		}
	}
	return picked, nil
}

func (r *ByoHostAgentUpgradeReconciler) markFailed(upgrade *infrastructurev1beta1.ByoHostAgentUpgrade, failed []*infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	for _, host := range failed {
		upgrade.Status.FailedHosts = append(upgrade.Status.FailedHosts, host.Name)
		if r.Recorder != nil {
			r.Recorder.Eventf(upgrade, corev1.EventTypeWarning, "HostUpgradeFailed", "host %s failed to upgrade", host.Name)
		}
	}
	upgrade.Status.Phase = infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed
	return ctrl.Result{}, nil
}

// hostSummary partitions the Selector-matched hosts for one reconcile tick.
type hostSummary struct {
	converged      []*infrastructurev1beta1.ByoHost
	inFlight       []*infrastructurev1beta1.ByoHost
	failed         []*infrastructurev1beta1.ByoHost
	notYetUpgraded []*infrastructurev1beta1.ByoHost
	// unavailable is the union (deduplicated by host name) of in-flight and
	// disconnected hosts - see docs/proposals/agent-self-upgrade-adr.md §2.3
	// "Availability accounting".
	unavailable map[string]struct{}
}

func summarizeHosts(hosts []infrastructurev1beta1.ByoHost, targetVersion string, perHostTimeout time.Duration) hostSummary {
	s := hostSummary{unavailable: map[string]struct{}{}}

	for i := range hosts {
		host := &hosts[i]
		converged := host.Status.AgentVersion == targetVersion
		assignedThisTarget := host.Spec.DesiredAgent != nil && host.Spec.DesiredAgent.Version == targetVersion

		switch {
		case converged:
			s.converged = append(s.converged, host)
		case assignedThisTarget && conditions.IsFalse(host, infrastructurev1beta1.AgentUpgradeSucceeded):
			s.failed = append(s.failed, host)
			s.unavailable[host.Name] = struct{}{}
		case assignedThisTarget:
			// In flight: assigned but not yet converged, and not explicitly
			// failed - includes hosts stuck silently (§2.2 step 8).
			if time.Since(host.Spec.DesiredAgent.AssignedAt.Time) > perHostTimeout {
				s.failed = append(s.failed, host)
			} else {
				s.inFlight = append(s.inFlight, host)
			}
			s.unavailable[host.Name] = struct{}{}
		default:
			s.notYetUpgraded = append(s.notYetUpgraded, host)
		}

		// Computed unconditionally, even for converged hosts: this is what
		// catches a host that converged and later crashed, without a
		// bake/soak timer - see docs/proposals/agent-self-upgrade-adr.md §2.3
		// "Availability accounting" and §4's rejection of a bake period.
		if !conditions.IsTrue(host, infrastructurev1beta1.AgentConnected) {
			s.unavailable[host.Name] = struct{}{}
		}
	}
	return s
}

// clampToInt32 narrows n to int32, saturating at the int32 bounds instead of
// wrapping. Host counts never approach 2^31, so the saturation is unreachable
// in practice. It is here so the narrowing is provably safe rather than
// resting on that assumption.
func clampToInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

// pickHosts returns up to n hosts from candidates, in list order. Ordering
// is deliberately whatever the API server returned - no priority scheme is
// specified by the ADR.
func pickHosts(candidates []*infrastructurev1beta1.ByoHost, n int) []*infrastructurev1beta1.ByoHost {
	if n <= 0 {
		return nil
	}
	if n > len(candidates) {
		n = len(candidates)
	}
	return candidates[:n]
}

// SetupWithManager sets up the controller with the Manager.
//
// This only watches ByoHostAgentUpgrade itself, not ByoHost - reacting to a
// ByoHost's heartbeat/AgentVersion change would need a mapping function
// that lists every ByoHostAgentUpgrade in the host's namespace and checks
// Selector membership, which is more machinery than justified right now.
// Pending/Progressing rollouts requeue every requeueInterval instead, so a
// host converging or failing is noticed within that bound rather than
// immediately - a deliberate simplification, not an oversight.
func (r *ByoHostAgentUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHostAgentUpgrade{}).
		Complete(r)
}
