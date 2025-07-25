//go:build !ignore_autogenerated

// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Code generated by controller-gen. DO NOT EDIT.

package v1beta1

import (
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIEndpoint) DeepCopyInto(out *APIEndpoint) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIEndpoint.
func (in *APIEndpoint) DeepCopy() *APIEndpoint {
	if in == nil {
		return nil
	}
	out := new(APIEndpoint)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BootstrapKubeconfig) DeepCopyInto(out *BootstrapKubeconfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BootstrapKubeconfig.
func (in *BootstrapKubeconfig) DeepCopy() *BootstrapKubeconfig {
	if in == nil {
		return nil
	}
	out := new(BootstrapKubeconfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *BootstrapKubeconfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BootstrapKubeconfigList) DeepCopyInto(out *BootstrapKubeconfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]BootstrapKubeconfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BootstrapKubeconfigList.
func (in *BootstrapKubeconfigList) DeepCopy() *BootstrapKubeconfigList {
	if in == nil {
		return nil
	}
	out := new(BootstrapKubeconfigList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *BootstrapKubeconfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BootstrapKubeconfigSpec) DeepCopyInto(out *BootstrapKubeconfigSpec) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BootstrapKubeconfigSpec.
func (in *BootstrapKubeconfigSpec) DeepCopy() *BootstrapKubeconfigSpec {
	if in == nil {
		return nil
	}
	out := new(BootstrapKubeconfigSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BootstrapKubeconfigStatus) DeepCopyInto(out *BootstrapKubeconfigStatus) {
	*out = *in
	if in.BootstrapKubeconfigData != nil {
		in, out := &in.BootstrapKubeconfigData, &out.BootstrapKubeconfigData
		*out = new(string)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BootstrapKubeconfigStatus.
func (in *BootstrapKubeconfigStatus) DeepCopy() *BootstrapKubeconfigStatus {
	if in == nil {
		return nil
	}
	out := new(BootstrapKubeconfigStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoCluster) DeepCopyInto(out *ByoCluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoCluster.
func (in *ByoCluster) DeepCopy() *ByoCluster {
	if in == nil {
		return nil
	}
	out := new(ByoCluster)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoCluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterList) DeepCopyInto(out *ByoClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ByoCluster, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterList.
func (in *ByoClusterList) DeepCopy() *ByoClusterList {
	if in == nil {
		return nil
	}
	out := new(ByoClusterList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterSpec) DeepCopyInto(out *ByoClusterSpec) {
	*out = *in
	out.ControlPlaneEndpoint = in.ControlPlaneEndpoint
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterSpec.
func (in *ByoClusterSpec) DeepCopy() *ByoClusterSpec {
	if in == nil {
		return nil
	}
	out := new(ByoClusterSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterStatus) DeepCopyInto(out *ByoClusterStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(apiv1beta1.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.FailureDomains != nil {
		in, out := &in.FailureDomains, &out.FailureDomains
		*out = make(apiv1beta1.FailureDomains, len(*in))
		for key, val := range *in {
			(*out)[key] = *val.DeepCopy()
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterStatus.
func (in *ByoClusterStatus) DeepCopy() *ByoClusterStatus {
	if in == nil {
		return nil
	}
	out := new(ByoClusterStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterTemplate) DeepCopyInto(out *ByoClusterTemplate) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterTemplate.
func (in *ByoClusterTemplate) DeepCopy() *ByoClusterTemplate {
	if in == nil {
		return nil
	}
	out := new(ByoClusterTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoClusterTemplate) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterTemplateList) DeepCopyInto(out *ByoClusterTemplateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ByoClusterTemplate, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterTemplateList.
func (in *ByoClusterTemplateList) DeepCopy() *ByoClusterTemplateList {
	if in == nil {
		return nil
	}
	out := new(ByoClusterTemplateList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoClusterTemplateList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterTemplateResource) DeepCopyInto(out *ByoClusterTemplateResource) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterTemplateResource.
func (in *ByoClusterTemplateResource) DeepCopy() *ByoClusterTemplateResource {
	if in == nil {
		return nil
	}
	out := new(ByoClusterTemplateResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoClusterTemplateSpec) DeepCopyInto(out *ByoClusterTemplateSpec) {
	*out = *in
	in.Template.DeepCopyInto(&out.Template)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoClusterTemplateSpec.
func (in *ByoClusterTemplateSpec) DeepCopy() *ByoClusterTemplateSpec {
	if in == nil {
		return nil
	}
	out := new(ByoClusterTemplateSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoHost) DeepCopyInto(out *ByoHost) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoHost.
func (in *ByoHost) DeepCopy() *ByoHost {
	if in == nil {
		return nil
	}
	out := new(ByoHost)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoHost) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoHostList) DeepCopyInto(out *ByoHostList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ByoHost, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoHostList.
func (in *ByoHostList) DeepCopy() *ByoHostList {
	if in == nil {
		return nil
	}
	out := new(ByoHostList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoHostList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoHostSpec) DeepCopyInto(out *ByoHostSpec) {
	*out = *in
	if in.BootstrapSecret != nil {
		in, out := &in.BootstrapSecret, &out.BootstrapSecret
		*out = new(v1.ObjectReference)
		**out = **in
	}
	if in.InstallationSecret != nil {
		in, out := &in.InstallationSecret, &out.InstallationSecret
		*out = new(v1.ObjectReference)
		**out = **in
	}
	if in.UninstallationSecret != nil {
		in, out := &in.UninstallationSecret, &out.UninstallationSecret
		*out = new(v1.ObjectReference)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoHostSpec.
func (in *ByoHostSpec) DeepCopy() *ByoHostSpec {
	if in == nil {
		return nil
	}
	out := new(ByoHostSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoHostStatus) DeepCopyInto(out *ByoHostStatus) {
	*out = *in
	if in.MachineRef != nil {
		in, out := &in.MachineRef, &out.MachineRef
		*out = new(v1.ObjectReference)
		**out = **in
	}
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(apiv1beta1.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	out.HostDetails = in.HostDetails
	if in.Network != nil {
		in, out := &in.Network, &out.Network
		*out = make([]NetworkStatus, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoHostStatus.
func (in *ByoHostStatus) DeepCopy() *ByoHostStatus {
	if in == nil {
		return nil
	}
	out := new(ByoHostStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachine) DeepCopyInto(out *ByoMachine) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachine.
func (in *ByoMachine) DeepCopy() *ByoMachine {
	if in == nil {
		return nil
	}
	out := new(ByoMachine)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoMachine) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineList) DeepCopyInto(out *ByoMachineList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ByoMachine, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineList.
func (in *ByoMachineList) DeepCopy() *ByoMachineList {
	if in == nil {
		return nil
	}
	out := new(ByoMachineList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoMachineList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineSpec) DeepCopyInto(out *ByoMachineSpec) {
	*out = *in
	if in.Selector != nil {
		in, out := &in.Selector, &out.Selector
		*out = new(metav1.LabelSelector)
		(*in).DeepCopyInto(*out)
	}
	if in.InstallerRef != nil {
		in, out := &in.InstallerRef, &out.InstallerRef
		*out = new(v1.ObjectReference)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineSpec.
func (in *ByoMachineSpec) DeepCopy() *ByoMachineSpec {
	if in == nil {
		return nil
	}
	out := new(ByoMachineSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineStatus) DeepCopyInto(out *ByoMachineStatus) {
	*out = *in
	out.HostInfo = in.HostInfo
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(apiv1beta1.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineStatus.
func (in *ByoMachineStatus) DeepCopy() *ByoMachineStatus {
	if in == nil {
		return nil
	}
	out := new(ByoMachineStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineTemplate) DeepCopyInto(out *ByoMachineTemplate) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineTemplate.
func (in *ByoMachineTemplate) DeepCopy() *ByoMachineTemplate {
	if in == nil {
		return nil
	}
	out := new(ByoMachineTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoMachineTemplate) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineTemplateList) DeepCopyInto(out *ByoMachineTemplateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]ByoMachineTemplate, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineTemplateList.
func (in *ByoMachineTemplateList) DeepCopy() *ByoMachineTemplateList {
	if in == nil {
		return nil
	}
	out := new(ByoMachineTemplateList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *ByoMachineTemplateList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineTemplateResource) DeepCopyInto(out *ByoMachineTemplateResource) {
	*out = *in
	in.Spec.DeepCopyInto(&out.Spec)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineTemplateResource.
func (in *ByoMachineTemplateResource) DeepCopy() *ByoMachineTemplateResource {
	if in == nil {
		return nil
	}
	out := new(ByoMachineTemplateResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineTemplateSpec) DeepCopyInto(out *ByoMachineTemplateSpec) {
	*out = *in
	in.Template.DeepCopyInto(&out.Template)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineTemplateSpec.
func (in *ByoMachineTemplateSpec) DeepCopy() *ByoMachineTemplateSpec {
	if in == nil {
		return nil
	}
	out := new(ByoMachineTemplateSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ByoMachineTemplateStatus) DeepCopyInto(out *ByoMachineTemplateStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ByoMachineTemplateStatus.
func (in *ByoMachineTemplateStatus) DeepCopy() *ByoMachineTemplateStatus {
	if in == nil {
		return nil
	}
	out := new(ByoMachineTemplateStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *HostInfo) DeepCopyInto(out *HostInfo) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new HostInfo.
func (in *HostInfo) DeepCopy() *HostInfo {
	if in == nil {
		return nil
	}
	out := new(HostInfo)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfig) DeepCopyInto(out *K8sInstallerConfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfig.
func (in *K8sInstallerConfig) DeepCopy() *K8sInstallerConfig {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *K8sInstallerConfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigList) DeepCopyInto(out *K8sInstallerConfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]K8sInstallerConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigList.
func (in *K8sInstallerConfigList) DeepCopy() *K8sInstallerConfigList {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *K8sInstallerConfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigSpec) DeepCopyInto(out *K8sInstallerConfigSpec) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigSpec.
func (in *K8sInstallerConfigSpec) DeepCopy() *K8sInstallerConfigSpec {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigStatus) DeepCopyInto(out *K8sInstallerConfigStatus) {
	*out = *in
	if in.InstallationSecret != nil {
		in, out := &in.InstallationSecret, &out.InstallationSecret
		*out = new(v1.ObjectReference)
		**out = **in
	}
	if in.UninstallationSecret != nil {
		in, out := &in.UninstallationSecret, &out.UninstallationSecret
		*out = new(v1.ObjectReference)
		**out = **in
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigStatus.
func (in *K8sInstallerConfigStatus) DeepCopy() *K8sInstallerConfigStatus {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigTemplate) DeepCopyInto(out *K8sInstallerConfigTemplate) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigTemplate.
func (in *K8sInstallerConfigTemplate) DeepCopy() *K8sInstallerConfigTemplate {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *K8sInstallerConfigTemplate) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigTemplateList) DeepCopyInto(out *K8sInstallerConfigTemplateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]K8sInstallerConfigTemplate, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigTemplateList.
func (in *K8sInstallerConfigTemplateList) DeepCopy() *K8sInstallerConfigTemplateList {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigTemplateList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *K8sInstallerConfigTemplateList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigTemplateResource) DeepCopyInto(out *K8sInstallerConfigTemplateResource) {
	*out = *in
	out.Spec = in.Spec
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigTemplateResource.
func (in *K8sInstallerConfigTemplateResource) DeepCopy() *K8sInstallerConfigTemplateResource {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigTemplateResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigTemplateSpec) DeepCopyInto(out *K8sInstallerConfigTemplateSpec) {
	*out = *in
	out.Template = in.Template
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigTemplateSpec.
func (in *K8sInstallerConfigTemplateSpec) DeepCopy() *K8sInstallerConfigTemplateSpec {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigTemplateSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *K8sInstallerConfigTemplateStatus) DeepCopyInto(out *K8sInstallerConfigTemplateStatus) {
	*out = *in
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new K8sInstallerConfigTemplateStatus.
func (in *K8sInstallerConfigTemplateStatus) DeepCopy() *K8sInstallerConfigTemplateStatus {
	if in == nil {
		return nil
	}
	out := new(K8sInstallerConfigTemplateStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkStatus) DeepCopyInto(out *NetworkStatus) {
	*out = *in
	if in.IPAddrs != nil {
		in, out := &in.IPAddrs, &out.IPAddrs
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkStatus.
func (in *NetworkStatus) DeepCopy() *NetworkStatus {
	if in == nil {
		return nil
	}
	out := new(NetworkStatus)
	in.DeepCopyInto(out)
	return out
}
