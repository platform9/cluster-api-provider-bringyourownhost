// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// ByoHostAgentUpgradePhase is the coarse-grained state of a
// ByoHostAgentUpgrade rollout.
type ByoHostAgentUpgradePhase string

const (
	// ByoHostAgentUpgradePhasePending means no host has been selected for
	// upgrade yet - either the rollout hasn't started reconciling, or it is
	// blocked (see RolloutBlocked) before ever picking a host.
	ByoHostAgentUpgradePhasePending ByoHostAgentUpgradePhase = "Pending"
	// ByoHostAgentUpgradePhaseProgressing means at least one host has
	// converged or is in flight, and no host has failed.
	ByoHostAgentUpgradePhaseProgressing ByoHostAgentUpgradePhase = "Progressing"
	// ByoHostAgentUpgradePhaseCompleted means every Selector-matched host
	// has converged on TargetVersion.
	ByoHostAgentUpgradePhaseCompleted ByoHostAgentUpgradePhase = "Completed"
	// ByoHostAgentUpgradePhaseFailed is terminal: a host explicitly failed
	// or timed out. No further hosts are ever selected by this CR again -
	// see docs/proposals/agent-self-upgrade-adr.md §2.3 step 5/§2.4.
	ByoHostAgentUpgradePhaseFailed ByoHostAgentUpgradePhase = "Failed"
)

// Conditions and reasons defined on ByoHostAgentUpgrade.
const (
	// RolloutBlocked documents that the rollout is not currently selecting
	// new hosts because MaxUnavailable's budget is already consumed by
	// hosts that are unavailable for a reason unrelated to this rollout
	// having touched them - not a failure, and not terminal: it clears on
	// its own once the budget frees up. See
	// docs/proposals/agent-self-upgrade-adr.md §2.3 step 4.
	RolloutBlocked clusterv1.ConditionType = "RolloutBlocked"

	// InsufficientAvailabilityBudgetReason indicates
	// |InFlight ∪ Disconnected| >= MaxUnavailable.
	InsufficientAvailabilityBudgetReason = "InsufficientAvailabilityBudget"
)

// ByoHostAgentUpgradeSpec defines the desired state of ByoHostAgentUpgrade.
type ByoHostAgentUpgradeSpec struct {
	// Selector selects which ByoHosts, within this object's own namespace,
	// this rollout targets. Namespace boundaries provide isolation - there
	// is no separate tenant/cohort concept: create this object in the
	// namespace you want upgraded, and further narrow with Selector (e.g.
	// by HostArchitectureLabel/HostOSFamilyLabel for a mixed fleet, or
	// clusterv1.ClusterNameLabel for a single ByoCluster's hosts).
	Selector metav1.LabelSelector `json:"selector"`

	// TargetVersion is the agent version this rollout converges selected
	// hosts to. Must be an explicit, fully-resolved version - never
	// "latest" or any other floating reference - enforced here rather than
	// by convention.
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+(\.[0-9]+)?(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`
	TargetVersion string `json:"targetVersion"`

	// PackageURL is an OCI image reference - pulled via `imgpkg pull`, the
	// same mechanism ByoHostSpec.DesiredAgent.PackageURL uses - for the
	// bundle containing this cohort's .deb/.rpm. All hosts matched by
	// Selector must share the same package family (HostOSFamilyLabel); this
	// is not validated here (see the ADR's open question on that gap).
	PackageURL string `json:"packageURL"`

	// PackageChecksum, if set, is the expected checksum ("sha256:<hex>") of
	// the .deb/.rpm extracted from the PackageURL bundle.
	// +optional
	PackageChecksum string `json:"packageChecksum,omitempty"`

	// MaxUnavailable bounds |InFlight ∪ Disconnected| - hosts this rollout
	// is actively upgrading, plus hosts unavailable for any other reason -
	// at any single point in time. Defaults to 1 if unset.
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// PerHostTimeout bounds how long a host may stay in flight (assigned
	// DesiredAgent but not yet converged, and not yet explicitly
	// failed) before this rollout considers it failed. Defaults to 10m if
	// unset.
	// +optional
	PerHostTimeout *metav1.Duration `json:"perHostTimeout,omitempty"`
}

// ByoHostAgentUpgradeStatus defines the observed state of ByoHostAgentUpgrade.
type ByoHostAgentUpgradeStatus struct {
	// +optional
	Phase ByoHostAgentUpgradePhase `json:"phase,omitempty"`

	// Total is the number of ByoHosts currently matching Selector.
	// +optional
	Total int32 `json:"total,omitempty"`

	// Upgraded is the number of matched hosts that have converged on
	// TargetVersion.
	// +optional
	Upgraded int32 `json:"upgraded,omitempty"`

	// UnavailableCount is the most recently computed
	// |InFlight ∪ Disconnected|, exposed for observability rather than kept
	// purely internal.
	// +optional
	UnavailableCount int32 `json:"unavailableCount,omitempty"`

	// FailedHosts lists hosts that explicitly failed (AgentUpgradeSucceeded
	// == False) or timed out. Once populated, Phase is Failed and stays
	// Failed - this list is never used to retry automatically.
	// +optional
	FailedHosts []string `json:"failedHosts,omitempty"`

	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byohostagentupgrades,scope=Namespaced,shortName=byohau
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Target",type="string",JSONPath=`.spec.targetVersion`
//+kubebuilder:printcolumn:name="Upgraded",type="string",JSONPath=`.status.upgraded`
//+kubebuilder:printcolumn:name="Total",type="string",JSONPath=`.status.total`

// ByoHostAgentUpgrade is the Schema for the byohostagentupgrades API. See
// docs/proposals/agent-self-upgrade-adr.md §2.3.
type ByoHostAgentUpgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoHostAgentUpgradeSpec   `json:"spec,omitempty"`
	Status ByoHostAgentUpgradeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoHostAgentUpgradeList contains a list of ByoHostAgentUpgrade.
type ByoHostAgentUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoHostAgentUpgrade `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ByoHostAgentUpgrade{}, &ByoHostAgentUpgradeList{})
}

// GetConditions gets the ByoHostAgentUpgrade status conditions
func (b *ByoHostAgentUpgrade) GetConditions() clusterv1.Conditions {
	return b.Status.Conditions
}

// SetConditions sets the ByoHostAgentUpgrade status conditions
func (b *ByoHostAgentUpgrade) SetConditions(conditions clusterv1.Conditions) {
	b.Status.Conditions = conditions
}
