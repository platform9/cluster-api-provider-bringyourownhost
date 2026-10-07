// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// MachineFinalizer allows ReconcileByoMachine to clean up Byo
	// resources associated with ByoMachine before removing it from the
	// API Server.
	MachineFinalizer = "byomachine.infrastructure.cluster.x-k8s.io"
)

// ByoMachineSpec defines the desired state of ByoMachine
type ByoMachineSpec struct {
	// Label Selector to choose the byohost
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	ProviderID string `json:"providerID,omitempty"`

	// InstallerRef is an optional reference to a installer-specific resource that holds
	// the details of InstallationSecret to be used to install BYOH Bundle.
	// +optional
	InstallerRef *corev1.ObjectReference `json:"installerRef,omitempty"`
}

// NetworkStatus provides information about one of a VM's networks.
type NetworkStatus struct {
	// Connected is a flag that indicates whether this network is currently
	// connected to the VM.
	Connected bool `json:"connected,omitempty"`

	// IPAddrs is one or more IP addresses reported by vm-tools.
	// +optional
	IPAddrs []string `json:"ipAddrs,omitempty"`

	// MACAddr is the MAC address of the network device.
	MACAddr string `json:"macAddr"`

	// NetworkInterfaceName is the name of the network interface.
	// +optional
	NetworkInterfaceName string `json:"networkInterfaceName,omitempty"`

	// IsDefault is a flag that indicates whether this interface name is where
	// the default gateway sit on.
	IsDefault bool `json:"isDefault,omitempty"`
}

// ByoMachineStatus defines the observed state of ByoMachine
type ByoMachineStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// HostInfo has the attached host platform details.
	// +optional
	HostInfo HostInfo `json:"hostinfo,omitempty"`

	// Initialization provides observations of the ByoMachine initialization process.
	// Cluster API reads initialization.provisioned under the v1beta2 provider contract.
	// +optional
	Initialization *ByoMachineInitializationStatus `json:"initialization,omitempty"`

	// Conditions represents the observations of the ByoMachine's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Ready is true when the ByoMachine is attached to a host and the node's providerID is set.
	//
	// Deprecated: Use initialization.provisioned instead. Kept for one release
	// series so pre-v1beta2-contract consumers keep working.
	// +optional
	Ready bool `json:"ready"`

	// Deprecated groups all the status fields that are deprecated and will be removed
	// when support for the v1beta1 provider contract is dropped.
	// +optional
	Deprecated *ByoMachineDeprecatedStatus `json:"deprecated,omitempty"`
}

// ByoMachineInitializationStatus provides observations of the ByoMachine initialization process.
// +kubebuilder:validation:MinProperties=1
type ByoMachineInitializationStatus struct {
	// Provisioned is true when the infrastructure provider reports that the machine's
	// infrastructure is fully provisioned.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// ByoMachineDeprecatedStatus groups all the status fields that are deprecated and will be
// removed in a future version.
type ByoMachineDeprecatedStatus struct {
	// V1Beta1 groups all the status fields that are deprecated and will be removed when
	// support for the v1beta1 provider contract is dropped.
	// +optional
	V1Beta1 *ByoMachineV1Beta1DeprecatedStatus `json:"v1beta1,omitempty"`
}

// ByoMachineV1Beta1DeprecatedStatus groups all the status fields that are deprecated and
// will be removed when support for the v1beta1 provider contract is dropped.
type ByoMachineV1Beta1DeprecatedStatus struct {
	// Conditions defines current service state of the ByoMachine, in the legacy
	// Cluster API v1beta1 condition format.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byomachines,scope=Namespaced,shortName=byom
//+kubebuilder:subresource:status

// ByoMachine is the Schema for the byomachines API
type ByoMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoMachineSpec   `json:"spec,omitempty"`
	Status ByoMachineStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoMachineList contains a list of ByoMachine
type ByoMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ByoMachine{}, &ByoMachineList{})
}

// GetConditions returns the conditions of the ByoMachine.
func (byoMachine *ByoMachine) GetConditions() []metav1.Condition {
	return byoMachine.Status.Conditions
}

// SetConditions sets the conditions of the ByoMachine.
func (byoMachine *ByoMachine) SetConditions(conditions []metav1.Condition) {
	byoMachine.Status.Conditions = conditions
}

// GetV1Beta1Conditions returns the legacy v1beta1 conditions of the ByoMachine.
func (byoMachine *ByoMachine) GetV1Beta1Conditions() clusterv1.Conditions {
	if byoMachine.Status.Deprecated == nil || byoMachine.Status.Deprecated.V1Beta1 == nil {
		return nil
	}
	return byoMachine.Status.Deprecated.V1Beta1.Conditions
}

// SetV1Beta1Conditions sets the legacy v1beta1 conditions of the ByoMachine.
func (byoMachine *ByoMachine) SetV1Beta1Conditions(conditions clusterv1.Conditions) {
	if byoMachine.Status.Deprecated == nil {
		byoMachine.Status.Deprecated = &ByoMachineDeprecatedStatus{}
	}
	if byoMachine.Status.Deprecated.V1Beta1 == nil {
		byoMachine.Status.Deprecated.V1Beta1 = &ByoMachineV1Beta1DeprecatedStatus{}
	}
	byoMachine.Status.Deprecated.V1Beta1.Conditions = conditions
}
