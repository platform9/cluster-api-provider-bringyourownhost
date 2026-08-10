// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const deploymentNameLabel = "cluster.x-k8s.io/deployment-name"

var (
	machineGVR = schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machines",
	}
	machineDeploymentGVR = schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "machinedeployments",
	}
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, capiv1beta1.AddToScheme(scheme))
	return scheme
}

func newTestMachine(namespace, name, deploymentName string) *capiv1beta1.Machine {
	labels := map[string]string{}
	if deploymentName != "" {
		labels[deploymentNameLabel] = deploymentName
	}
	return &capiv1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    labels,
		},
	}
}

func newTestMachineDeployment(namespace, name string, replicas int32) *capiv1beta1.MachineDeployment {
	return &capiv1beta1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: capiv1beta1.MachineDeploymentSpec{
			ClusterName: "test-cluster",
			Replicas:    &replicas,
		},
	}
}

func TestGetUnstructuredMachineObject(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "md1")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)
	assert.Equal(t, "m1", obj.GetName())
	assert.Equal(t, "ns1", obj.GetNamespace())
	assert.Equal(t, "md1", obj.GetLabels()[deploymentNameLabel])
}

func TestGetUnstructuredMachineObject_NotFound(t *testing.T) {
	scheme := newTestScheme(t)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)
	c := &Client{DynamicClient: dynamicClient}

	_, err := c.GetUnstructuredMachineObject("ns1", "missing")
	assert.Error(t, err)
}

func TestAnnotateMachineObject(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "md1")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)

	err = c.AnnotateMachineObject(obj, "ns1", "byoh.platform9.io/decommissioned", "true")
	require.NoError(t, err)

	updated, err := dynamicClient.Resource(machineGVR).Namespace("ns1").Get(context.Background(), "m1", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "true", updated.GetAnnotations()["byoh.platform9.io/decommissioned"])
}

func TestGetMachineDeploymentReplicaCount(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "md1")
	deployment := newTestMachineDeployment("ns1", "md1", 3)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine, deployment)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)

	count, err := c.GetMachineDeploymentReplicaCount(obj, "ns1")
	require.NoError(t, err)
	assert.Equal(t, int32(3), count)
}

func TestGetMachineDeploymentReplicaCount_MissingLabel(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)

	_, err = c.GetMachineDeploymentReplicaCount(obj, "ns1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not have a machine deployment name")
}

func TestScaleDownMachineDeployment(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "md1")
	deployment := newTestMachineDeployment("ns1", "md1", 3)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine, deployment)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)

	err = c.ScaleDownMachineDeployment(obj, "ns1")
	require.NoError(t, err)

	updatedUnstructured, err := dynamicClient.Resource(machineDeploymentGVR).Namespace("ns1").Get(context.Background(), "md1", metav1.GetOptions{})
	require.NoError(t, err)

	updated := &capiv1beta1.MachineDeployment{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructured.UnstructuredContent(), updated))
	assert.Equal(t, int32(2), *updated.Spec.Replicas)
}

func TestScaleDownMachineDeployment_MissingLabel(t *testing.T) {
	scheme := newTestScheme(t)
	machine := newTestMachine("ns1", "m1", "")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, machine)
	c := &Client{DynamicClient: dynamicClient}

	obj, err := c.GetUnstructuredMachineObject("ns1", "m1")
	require.NoError(t, err)

	err = c.ScaleDownMachineDeployment(obj, "ns1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not have a machine deployment name")
}
