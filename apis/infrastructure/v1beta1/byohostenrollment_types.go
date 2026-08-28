// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// Conditions and reasons defined on ByoHostEnrollment.
const (
	// CredentialReady is True once the credential Secret named by
	// Status.CredentialSecretRef exists and holds a bootstrap kubeconfig the
	// host can use. It goes back to False while the credential is being
	// replaced, which happens on every retry: a new credential for a host
	// invalidates the previous one.
	CredentialReady clusterv1.ConditionType = "CredentialReady"

	// Consumed is True once the host has exchanged the credential for a
	// client certificate. The credential is deleted on that transition and is
	// not refreshed again.
	Consumed clusterv1.ConditionType = "Consumed"
)

// ByoHostEnrollmentSpec defines the desired state of ByoHostEnrollment.
type ByoHostEnrollmentSpec struct {
	// TokenTTL is how long each minted bootstrap token stays valid. The
	// credential is re-minted while the host has not yet consumed it, so this
	// bounds the lifetime of a single token rather than the lifetime of the
	// enrollment - use ValidUntil for the latter.
	// +optional
	// +kubebuilder:default="30m"
	TokenTTL *metav1.Duration `json:"tokenTTL,omitempty"`

	// ValidUntil is the deadline past which the credential is no longer
	// re-minted. It exists for hosts that are enrolled well before they first
	// boot - an image build, or a machine racked days ahead of use - where a
	// single TokenTTL would expire before the host ever asks for a
	// certificate. Unset means the enrollment is refreshed for as long as it
	// exists.
	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`
}

// ByoHostEnrollmentStatus defines the observed state of ByoHostEnrollment.
type ByoHostEnrollmentStatus struct {
	// ObservedGeneration is the Generation of the spec this status was last
	// computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// CredentialSecretRef names the Secret, in this object's own namespace,
	// holding the bootstrap kubeconfig for this host. The kubeconfig lives in
	// a Secret rather than in status so that reading an enrollment record and
	// reading a host's credential can be granted separately.
	// +optional
	CredentialSecretRef *corev1.LocalObjectReference `json:"credentialSecretRef,omitempty"`

	// TokenID is the public half of the bootstrap token currently backing the
	// credential. A certificate request authenticated as
	// system:bootstrap:<tokenID> resolves back to this enrollment, which is how
	// the approver learns which host a request is allowed to claim.
	// +optional
	TokenID string `json:"tokenID,omitempty"`

	// ExpiresAt is when the current token stops being accepted.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byohostenrollments,scope=Namespaced,shortName=byohe
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=='CredentialReady')].status`
//+kubebuilder:printcolumn:name="Consumed",type="string",JSONPath=`.status.conditions[?(@.type=='Consumed')].status`
//+kubebuilder:printcolumn:name="Secret",type="string",JSONPath=`.status.credentialSecretRef.name`
//+kubebuilder:printcolumn:name="Expires",type="date",JSONPath=`.status.expiresAt`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=`.metadata.creationTimestamp`

// ByoHostEnrollment is the Schema for the byohostenrollments API. One object
// enrolls one host: metadata.name is the host name, which is also the name of
// the ByoHost the host later registers and the suffix of the common name in
// its client certificate. Creating a second enrollment for the same host is a
// name conflict rather than a second credential, so retries are idempotent
// without any extra rule.
//
// Labels on this object become the labels on the resulting ByoHost, which is
// how deployment-specific grouping reaches a host without this API carrying a
// field for it.
type ByoHostEnrollment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoHostEnrollmentSpec   `json:"spec,omitempty"`
	Status ByoHostEnrollmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoHostEnrollmentList contains a list of ByoHostEnrollment.
type ByoHostEnrollmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoHostEnrollment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ByoHostEnrollment{}, &ByoHostEnrollmentList{})
}

// GetConditions gets the ByoHostEnrollment status conditions
func (b *ByoHostEnrollment) GetConditions() clusterv1.Conditions {
	return b.Status.Conditions
}

// SetConditions sets the ByoHostEnrollment status conditions
func (b *ByoHostEnrollment) SetConditions(conditions clusterv1.Conditions) {
	b.Status.Conditions = conditions
}
