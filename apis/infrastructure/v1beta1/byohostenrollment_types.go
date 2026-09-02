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
	// host can use. It is False, with a reason, whenever no usable credential
	// exists: before the first one is created, and after a failed attempt to
	// create or replace one.
	CredentialReady clusterv1.ConditionType = "CredentialReady"

	// Consumed is True once the host has exchanged the credential for a
	// client certificate. The credential is deleted on that transition and is
	// not refreshed again.
	Consumed clusterv1.ConditionType = "Consumed"

	// InvalidHostNameReason is set on CredentialReady when the enrollment name
	// does not normalize to a usable host name. Retrying cannot help: the
	// enrollment has to be created again under a name that normalizes.
	InvalidHostNameReason = "InvalidHostName"

	// TransportUnavailableReason is set on CredentialReady when the
	// deployment-level transport ConfigMap is missing or does not describe a
	// usable API server endpoint. Nothing about the enrollment is wrong; the
	// deployment is not finished.
	TransportUnavailableReason = "TransportUnavailable"

	// CredentialGenerateFailedReason is set on CredentialReady when the token or
	// the credential Secret could not be written.
	CredentialGenerateFailedReason = "CredentialGenerateFailed" //nolint: gosec // a condition reason, not a credential
)

// Constants describing the artifacts a ByoHostEnrollment produces.
const (
	// EnrollmentFinalizer keeps the enrollment around until its bootstrap
	// token Secret has been deleted. That Secret lives in kube-system, which a
	// namespaced object cannot own, so nothing else would remove it.
	EnrollmentFinalizer = "byohostenrollment.infrastructure.cluster.x-k8s.io"

	// CredentialSecretType marks the Secret holding a host's bootstrap
	// kubeconfig, so that RBAC and any sweeper can select these Secrets alone.
	CredentialSecretType corev1.SecretType = "infrastructure.cluster.x-k8s.io/bootstrap-kubeconfig"

	// CredentialSecretNameSuffix is appended to the enrollment name to name its
	// credential Secret.
	CredentialSecretNameSuffix = "-bootstrap"

	// CredentialSecretKubeconfigKey holds the rendered bootstrap kubeconfig.
	CredentialSecretKubeconfigKey = "kubeconfig"

	// CredentialSecretHostNameKey holds the host name this credential was
	// created for. It travels with the credential so the agent does not have to
	// read the machine's own hostname and reach a different answer.
	CredentialSecretHostNameKey = "hostName"

	// EnrollmentLabel is set on the bootstrap token Secret in kube-system,
	// naming the enrollment that created it. The Secret cannot carry an owner
	// reference across namespaces, so this label is the only link back.
	EnrollmentLabel = "byoh.infrastructure.cluster.x-k8s.io/enrollment"

	// MaxK8sObjectNameLength is the longest name a namespaced object may have,
	// an RFC 1123 subdomain.
	MaxK8sObjectNameLength = 253

	// HostCommonNamePrefix precedes the host name in the common name of a
	// host's client certificate. The agent builds the common name it asks for
	// and the approver rebuilds the one it will allow, so both sides derive it
	// from this prefix rather than from a literal of their own.
	HostCommonNamePrefix = "byoh:host:"
)

// ByoHostEnrollmentSpec defines the desired state of ByoHostEnrollment.
type ByoHostEnrollmentSpec struct {
	// TokenTTL is how long each bootstrap token stays valid once created. The
	// credential is regenerated while the host has not yet consumed it, so this
	// bounds the lifetime of a single token rather than the lifetime of the
	// enrollment - use ValidUntil for the latter.
	// +optional
	// +kubebuilder:default="30m"
	TokenTTL *metav1.Duration `json:"tokenTTL,omitempty"`

	// ValidUntil is the deadline past which the credential is no longer
	// regenerated. It exists for hosts that are enrolled well before they first
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
