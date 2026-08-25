// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const upgradeCohortLabel = "test.byoh.infrastructure.cluster.x-k8s.io/cohort"

// newCohortHost creates a ByoHost labeled for a specific test's Selector, so
// concurrently-run tests in this shared namespace/envtest instance never
// see each other's hosts. builder.ByoHost uses GenerateName, so the
// returned object's real Name is server-assigned - callers must key off the
// returned object, never reconstruct the name from the string passed in.
// connected/agentVersion set the initial state a real, healthy,
// not-yet-touched host would report.
func newCohortHost(t *testing.T, cohort, namePrefix string, connected bool, agentVersion string) *infrastructurev1beta1.ByoHost {
	t.Helper()
	host := builder.ByoHost(defaultNamespace, namePrefix).WithLabels(map[string]string{upgradeCohortLabel: cohort}).Build()
	require.NoError(t, directClient.Create(context.Background(), host))
	t.Cleanup(func() {
		_ = client.IgnoreNotFound(directClient.Delete(context.Background(), host))
	})

	helper, err := patch.NewHelper(host, directClient)
	require.NoError(t, err)
	host.Status.AgentVersion = agentVersion
	if connected {
		conditions.MarkTrue(host, infrastructurev1beta1.AgentConnected)
	} else {
		conditions.MarkFalse(host, infrastructurev1beta1.AgentConnected, infrastructurev1beta1.HeartbeatTimeoutReason, clusterv1.ConditionSeverityWarning, "")
	}
	require.NoError(t, helper.Patch(context.Background(), host))
	return host
}

// refreshHost re-fetches host by its (server-assigned) Name.
func refreshHost(t *testing.T, host *infrastructurev1beta1.ByoHost) *infrastructurev1beta1.ByoHost {
	t.Helper()
	got := &infrastructurev1beta1.ByoHost{}
	require.NoError(t, directClient.Get(context.Background(), types.NamespacedName{Name: host.Name, Namespace: host.Namespace}, got))
	return got
}

// desiredVersion returns host.Spec.DesiredAgent.Version, or "" if no upgrade
// has been assigned to this host yet.
func desiredVersion(host *infrastructurev1beta1.ByoHost) string {
	if host.Spec.DesiredAgent == nil {
		return ""
	}
	return host.Spec.DesiredAgent.Version
}

// patchHost applies mutate to a fresh copy of host and persists it via
// patch.NewHelper, matching this suite's existing byohost_controller_test.go
// idiom.
func patchHost(t *testing.T, host *infrastructurev1beta1.ByoHost, mutate func(*infrastructurev1beta1.ByoHost)) *infrastructurev1beta1.ByoHost {
	t.Helper()
	host = refreshHost(t, host)
	helper, err := patch.NewHelper(host, directClient)
	require.NoError(t, err)
	mutate(host)
	require.NoError(t, helper.Patch(context.Background(), host))
	return host
}

func newUpgradeForTest(t *testing.T, cohort, targetVersion string, maxUnavailable int) *infrastructurev1beta1.ByoHostAgentUpgrade {
	t.Helper()
	mu := intstr.FromInt(maxUnavailable)
	upgrade := &infrastructurev1beta1.ByoHostAgentUpgrade{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("upgrade-%s", cohort),
			Namespace: defaultNamespace,
		},
		Spec: infrastructurev1beta1.ByoHostAgentUpgradeSpec{
			Selector:       metav1.LabelSelector{MatchLabels: map[string]string{upgradeCohortLabel: cohort}},
			TargetVersion:  targetVersion,
			PackageURL:     "registry.example.com/agent-bundle:" + targetVersion,
			MaxUnavailable: &mu,
		},
	}
	require.NoError(t, directClient.Create(context.Background(), upgrade))
	t.Cleanup(func() {
		_ = client.IgnoreNotFound(directClient.Delete(context.Background(), upgrade))
	})
	return upgrade
}

func reconcileUpgrade(t *testing.T, upgrade *infrastructurev1beta1.ByoHostAgentUpgrade) *infrastructurev1beta1.ByoHostAgentUpgrade {
	t.Helper()
	key := types.NamespacedName{Name: upgrade.Name, Namespace: upgrade.Namespace}
	_, err := byoHostAgentUpgradeReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	got := &infrastructurev1beta1.ByoHostAgentUpgrade{}
	require.NoError(t, directClient.Get(context.Background(), key, got))
	return got
}

func TestByoHostAgentUpgradeController_BatchSizing(t *testing.T) {
	cohort := "batch-sizing"
	hosts := make([]*infrastructurev1beta1.ByoHost, 5)
	for i := range hosts {
		hosts[i] = newCohortHost(t, cohort, fmt.Sprintf("%s-host-%d-", cohort, i), true, "v1.0.0")
	}
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 2)

	upgrade = reconcileUpgrade(t, upgrade)

	assigned := 0
	for _, h := range hosts {
		if desiredVersion(refreshHost(t, h)) == "v2.0.0" {
			assigned++
		}
	}
	assert.Equal(t, 2, assigned, "must never assign more than MaxUnavailable hosts in one tick")
	assert.EqualValues(t, 2, upgrade.Status.UnavailableCount)
	assert.Equal(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseProgressing, upgrade.Status.Phase)
}

func TestByoHostAgentUpgradeController_PreexistingDisconnectionBlocksAndSelfResolves(t *testing.T) {
	cohort := "pre-existing-disconnect"
	// One host already down for a reason unrelated to this rollout, before
	// the CR is even created.
	downHost := newCohortHost(t, cohort, cohort+"-down-", false, "v1.0.0")
	healthy1 := newCohortHost(t, cohort, cohort+"-healthy1-", true, "v1.0.0")
	healthy2 := newCohortHost(t, cohort, cohort+"-healthy2-", true, "v1.0.0")
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)

	upgrade = reconcileUpgrade(t, upgrade)

	assert.Equal(t, "", desiredVersion(refreshHost(t, healthy1)), "budget already consumed by the pre-existing disconnection - nothing should be picked")
	assert.Equal(t, "", desiredVersion(refreshHost(t, healthy2)))
	assert.NotEqual(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase, "pre-existing unavailability must block softly, not fail")
	assert.True(t, conditions.IsFalse(upgrade, infrastructurev1beta1.RolloutAvailable))
	cond := conditions.Get(upgrade, infrastructurev1beta1.RolloutAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.InsufficientAvailabilityBudgetReason, cond.Reason)

	// The unrelated host recovers.
	patchHost(t, downHost, func(h *infrastructurev1beta1.ByoHost) {
		conditions.MarkTrue(h, infrastructurev1beta1.AgentConnected)
	})

	upgrade = reconcileUpgrade(t, upgrade)

	assert.True(t, conditions.IsTrue(upgrade, infrastructurev1beta1.RolloutAvailable), "budget freed up - rollout should resume on its own")
	// Any one of the three hosts (including the now-recovered downHost
	// itself, a valid not-yet-upgraded candidate) may be picked - List()
	// order isn't specified. What matters is exactly one starts.
	picked := 0
	for _, h := range []*infrastructurev1beta1.ByoHost{downHost, healthy1, healthy2} {
		if desiredVersion(refreshHost(t, h)) == "v2.0.0" {
			picked++
		}
	}
	assert.Equal(t, 1, picked, "exactly one host should now be picked (MaxUnavailable=1)")
}

func TestByoHostAgentUpgradeController_InFlightAndDisconnectedCountsOnce(t *testing.T) {
	cohort := "union-not-sum"
	host := newCohortHost(t, cohort, cohort+"-host-", true, "v1.0.0")
	newCohortHost(t, cohort, cohort+"-other-", true, "v1.0.0")
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)

	upgrade = reconcileUpgrade(t, upgrade)
	require.Equal(t, "v2.0.0", desiredVersion(refreshHost(t, host)), "sanity: this host should have been picked")

	// The picked host is now both in-flight (assigned, not yet converged)
	// AND disconnected (e.g. mid syscall.Exec) at the same time.
	patchHost(t, host, func(h *infrastructurev1beta1.ByoHost) {
		conditions.MarkFalse(h, infrastructurev1beta1.AgentConnected, infrastructurev1beta1.HeartbeatTimeoutReason, clusterv1.ConditionSeverityWarning, "")
	})

	upgrade = reconcileUpgrade(t, upgrade)

	assert.EqualValues(t, 1, upgrade.Status.UnavailableCount, "union, not sum: one host in both sets counts once")
}

func TestByoHostAgentUpgradeController_ConvergedThenDisconnectedBlocksNextBatchWithoutFailing(t *testing.T) {
	cohort := "post-convergence-crash"
	// Three hosts so there's still a legitimate "next batch" candidate left
	// after the first one converges and a second one starts - List() order
	// isn't specified, so which host is picked first/second is determined
	// dynamically below rather than assumed.
	all := []*infrastructurev1beta1.ByoHost{
		newCohortHost(t, cohort, cohort+"-a-", true, "v1.0.0"),
		newCohortHost(t, cohort, cohort+"-b-", true, "v1.0.0"),
		newCohortHost(t, cohort, cohort+"-c-", true, "v1.0.0"),
	}
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)

	upgrade = reconcileUpgrade(t, upgrade)
	first := findByDesiredVersion(t, all, "v2.0.0")
	require.NotNil(t, first, "exactly one host should have been picked (MaxUnavailable=1)")

	// Simulate a successful upgrade: AgentVersion converges. That frees the
	// budget, so this same reconcile should start a second host.
	patchHost(t, first, func(h *infrastructurev1beta1.ByoHost) { h.Status.AgentVersion = "v2.0.0" })
	upgrade = reconcileUpgrade(t, upgrade)
	assert.EqualValues(t, 1, upgrade.Status.Upgraded)
	second := findByDesiredVersion(t, all, "v2.0.0", first.Name)
	require.NotNil(t, second, "the freed budget should let a second host start")

	// ...then, moments later, the first (already-converged) host crashes.
	patchHost(t, first, func(h *infrastructurev1beta1.ByoHost) {
		conditions.MarkFalse(h, infrastructurev1beta1.AgentConnected, infrastructurev1beta1.HeartbeatTimeoutReason, clusterv1.ConditionSeverityWarning, "")
	})

	upgrade = reconcileUpgrade(t, upgrade)

	assert.EqualValues(t, 2, upgrade.Status.UnavailableCount, "the crashed (converged) host plus the still in-flight second host")
	third := findByDesiredVersion(t, all, "v2.0.0", first.Name, second.Name)
	assert.Nil(t, third, "no further host should start while the crashed host consumes the budget")
	assert.NotEqual(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase, "a post-convergence crash is not itself an AgentUpgradeSucceeded=False failure")
	assert.NotContains(t, upgrade.Status.FailedHosts, first.Name)
}

// findByDesiredVersion returns the first host in all (refreshed from the API
// server) whose Spec.DesiredAgent.Version equals version, excluding any host
// named in exclude. Returns nil if none match.
func findByDesiredVersion(t *testing.T, all []*infrastructurev1beta1.ByoHost, version string, exclude ...string) *infrastructurev1beta1.ByoHost {
	t.Helper()
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	for _, h := range all {
		if skip[h.Name] {
			continue
		}
		if fresh := refreshHost(t, h); desiredVersion(fresh) == version {
			return fresh
		}
	}
	return nil
}

func TestByoHostAgentUpgradeController_ExplicitFailureHaltsPermanently(t *testing.T) {
	cohort := "explicit-failure"
	host := newCohortHost(t, cohort, cohort+"-host-", true, "v1.0.0")
	untouched := newCohortHost(t, cohort, cohort+"-untouched-", true, "v1.0.0")
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)

	upgrade = reconcileUpgrade(t, upgrade)
	require.Equal(t, "v2.0.0", desiredVersion(refreshHost(t, host)))

	patchHost(t, host, func(h *infrastructurev1beta1.ByoHost) {
		conditions.MarkFalse(h, infrastructurev1beta1.AgentUpgradeSucceeded, infrastructurev1beta1.PackageInstallFailedReason, clusterv1.ConditionSeverityWarning, "")
	})

	upgrade = reconcileUpgrade(t, upgrade)

	assert.Equal(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase)
	assert.Contains(t, upgrade.Status.FailedHosts, host.Name)
	assert.Equal(t, "", desiredVersion(refreshHost(t, untouched)), "untouched host must stay untouched once the rollout has failed")

	// Even after the host would otherwise look recovered, Failed never resumes.
	patchHost(t, host, func(h *infrastructurev1beta1.ByoHost) { h.Status.AgentVersion = "v2.0.0" })

	upgrade = reconcileUpgrade(t, upgrade)
	assert.Equal(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase, "Failed is terminal")
	assert.Equal(t, "", desiredVersion(refreshHost(t, untouched)))
}

func TestByoHostAgentUpgradeController_PerHostTimeoutFailsAndHalts(t *testing.T) {
	cohort := "per-host-timeout"
	stuck := newCohortHost(t, cohort, cohort+"-stuck-", true, "v1.0.0")
	untouched := newCohortHost(t, cohort, cohort+"-untouched-", true, "v1.0.0")
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)
	shortTimeout := metav1.Duration{Duration: 50 * time.Millisecond}
	upgrade.Spec.PerHostTimeout = &shortTimeout
	require.NoError(t, directClient.Update(context.Background(), upgrade))

	upgrade = reconcileUpgrade(t, upgrade)
	require.Equal(t, "v2.0.0", desiredVersion(refreshHost(t, stuck)))
	require.NotEqual(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase,
		"a host must not be treated as timed out on the same tick it was assigned")

	time.Sleep(100 * time.Millisecond)
	upgrade = reconcileUpgrade(t, upgrade)

	assert.Equal(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed, upgrade.Status.Phase)
	assert.Contains(t, upgrade.Status.FailedHosts, stuck.Name)
	assert.Equal(t, "", desiredVersion(refreshHost(t, untouched)))
}

func TestByoHostAgentUpgradeController_CompletesWhenAllConverge(t *testing.T) {
	cohort := "completes"
	host := newCohortHost(t, cohort, cohort+"-host-", true, "v1.0.0")
	upgrade := newUpgradeForTest(t, cohort, "v2.0.0", 1)

	upgrade = reconcileUpgrade(t, upgrade)
	require.Equal(t, "v2.0.0", desiredVersion(refreshHost(t, host)))

	patchHost(t, host, func(h *infrastructurev1beta1.ByoHost) { h.Status.AgentVersion = "v2.0.0" })

	upgrade = reconcileUpgrade(t, upgrade)

	assert.Equal(t, infrastructurev1beta1.ByoHostAgentUpgradePhaseCompleted, upgrade.Status.Phase)
	assert.EqualValues(t, 1, upgrade.Status.Upgraded)
	assert.Empty(t, upgrade.Status.FailedHosts)
}

func TestByoHostAgentUpgradeController_RejectsFloatingTargetVersion(t *testing.T) {
	upgrade := &infrastructurev1beta1.ByoHostAgentUpgrade{
		ObjectMeta: metav1.ObjectMeta{Name: "rejects-latest", Namespace: defaultNamespace},
		Spec: infrastructurev1beta1.ByoHostAgentUpgradeSpec{
			Selector:      metav1.LabelSelector{MatchLabels: map[string]string{upgradeCohortLabel: "irrelevant"}},
			TargetVersion: "latest",
			PackageURL:    "registry.example.com/agent-bundle:latest",
		},
	}
	err := directClient.Create(context.Background(), upgrade)
	assert.Error(t, err, "the CRD's validation pattern must reject a floating TargetVersion at admission")
}
