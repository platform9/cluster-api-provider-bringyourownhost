// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// ClusterFinalizer allows ReconcileByoCluster to clean up Byo
	// resources associated with ByoCluster before removing it from the
	// API server.
	ClusterFinalizer = "byocluster.infrastructure.cluster.x-k8s.io"
)

// ByoClusterSpec defines the desired state of ByoCluster
type ByoClusterSpec struct {
	// ControlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint"`

	// BundleLookupBaseRegistry is the base Registry URL that is used for pulling byoh bundle images,
	// if not set, the default will be set to https://quay.io/platform9
	// +optional
	BundleLookupBaseRegistry string `json:"bundleLookupBaseRegistry,omitempty"`
}

// ByoClusterStatus defines the observed state of ByoCluster
type ByoClusterStatus struct {
	// Initialization provides observations of the ByoCluster initialization process.
	// Cluster API reads initialization.provisioned under the v1beta2 provider contract.
	// +optional
	Initialization *ByoClusterInitializationStatus `json:"initialization,omitempty"`

	// Conditions represents the observations of the ByoCluster's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Ready is true when the cluster infrastructure is provisioned.
	//
	// Deprecated: Use initialization.provisioned instead. Kept for one release
	// series so pre-v1beta2-contract consumers keep working.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// FailureDomains is a list of failure domain objects synced from the infrastructure provider.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	FailureDomains []clusterv1.FailureDomain `json:"failureDomains,omitempty"`

	// Deprecated groups all the status fields that are deprecated and will be removed
	// when support for the v1beta1 provider contract is dropped.
	// +optional
	Deprecated *ByoClusterDeprecatedStatus `json:"deprecated,omitempty"`
}

// ByoClusterInitializationStatus provides observations of the ByoCluster initialization process.
// +kubebuilder:validation:MinProperties=1
type ByoClusterInitializationStatus struct {
	// Provisioned is true when the infrastructure provider reports that the cluster
	// infrastructure is fully provisioned.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// ByoClusterDeprecatedStatus groups all the status fields that are deprecated and will be
// removed in a future version.
type ByoClusterDeprecatedStatus struct {
	// V1Beta1 groups all the status fields that are deprecated and will be removed when
	// support for the v1beta1 provider contract is dropped.
	// +optional
	V1Beta1 *ByoClusterV1Beta1DeprecatedStatus `json:"v1beta1,omitempty"`
}

// ByoClusterV1Beta1DeprecatedStatus groups all the status fields that are deprecated and
// will be removed when support for the v1beta1 provider contract is dropped.
type ByoClusterV1Beta1DeprecatedStatus struct {
	// Conditions defines current service state of the ByoCluster, in the legacy
	// Cluster API v1beta1 condition format.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// APIEndpoint represents a reachable Kubernetes API endpoint.
type APIEndpoint struct {
	// Host is the hostname on which the API server is serving.
	Host string `json:"host"`

	// Port is the port on which the API server is serving.
	Port int32 `json:"port"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byoclusters,scope=Namespaced,shortName=byoc
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="PROVISIONED",type=string,JSONPath=".status.initialization.provisioned",description="Indicates if the ByoCluster infrastructure is provisioned"
//+kubebuilder:printcolumn:name="AGE",type=date,JSONPath=".metadata.creationTimestamp"

// ByoCluster is the Schema for the byoclusters API
type ByoCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoClusterSpec   `json:"spec,omitempty"`
	Status ByoClusterStatus `json:"status,omitempty"`
}

// GetConditions returns the conditions of the ByoCluster.
func (byoCluster *ByoCluster) GetConditions() []metav1.Condition {
	return byoCluster.Status.Conditions
}

// SetConditions sets the conditions of the ByoCluster.
func (byoCluster *ByoCluster) SetConditions(conditions []metav1.Condition) {
	byoCluster.Status.Conditions = conditions
}

// GetV1Beta1Conditions returns the legacy v1beta1 conditions of the ByoCluster.
func (byoCluster *ByoCluster) GetV1Beta1Conditions() clusterv1.Conditions {
	if byoCluster.Status.Deprecated == nil || byoCluster.Status.Deprecated.V1Beta1 == nil {
		return nil
	}
	return byoCluster.Status.Deprecated.V1Beta1.Conditions
}

// SetV1Beta1Conditions sets the legacy v1beta1 conditions of the ByoCluster.
func (byoCluster *ByoCluster) SetV1Beta1Conditions(conditions clusterv1.Conditions) {
	if byoCluster.Status.Deprecated == nil {
		byoCluster.Status.Deprecated = &ByoClusterDeprecatedStatus{}
	}
	if byoCluster.Status.Deprecated.V1Beta1 == nil {
		byoCluster.Status.Deprecated.V1Beta1 = &ByoClusterV1Beta1DeprecatedStatus{}
	}
	byoCluster.Status.Deprecated.V1Beta1.Conditions = conditions
}

//+kubebuilder:object:root=true

// ByoClusterList contains a list of ByoCluster
type ByoClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ByoCluster{}, &ByoClusterList{})
}
