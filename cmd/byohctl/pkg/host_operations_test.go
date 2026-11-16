// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package pkg

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // tracked for follow-up migration off the deprecated API

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/client"
)

const testNamespace = "byoh-ns"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, infrastructurev1beta1.AddToScheme(s))
	require.NoError(t, capiv1beta1.AddToScheme(s))
	return s
}

func fakeClient(t *testing.T, objs ...runtime.Object) *client.Client {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(testScheme(t), objs...)
	return &client.Client{DynamicClient: dyn}
}

// stubIO is a HostIO stub with canned answers and call counters.
type stubIO struct {
	confirmResp bool
	confirmErr  error
	confirmN    int
	purgeErr    error
	purgeN      int
}

func (s *stubIO) Confirm(string) (bool, error) {
	s.confirmN++
	return s.confirmResp, s.confirmErr
}

func (s *stubIO) Purge() error {
	s.purgeN++
	return s.purgeErr
}

func TestPerformHostOperation(t *testing.T) {
	tests := []struct {
		name            string
		objs            []runtime.Object
		operation       HostOperationType
		force           bool
		confirmResp     bool
		wantErr         bool
		wantErrContains string
		wantPurgeCalls  int
		wantAskCalls    int
	}{
		{
			name:            "deauthorise / ByoHost missing / no force returns error",
			operation:       OperationDeauthorise,
			force:           false,
			wantErr:         true,
			wantErrContains: "Cannot proceed",
		},
		{
			name:      "deauthorise / ByoHost missing / force treats as no-op",
			operation: OperationDeauthorise,
			force:     true,
		},
		{
			name:           "decommission / ByoHost missing / force purges without prompt",
			operation:      OperationDecommission,
			force:          true,
			wantPurgeCalls: 1,
		},
		{
			name:         "decommission / ByoHost missing / no force + user declines skips purge",
			operation:    OperationDecommission,
			force:        false,
			confirmResp:  false,
			wantAskCalls: 1,
		},
		{
			name:           "decommission / ByoHost missing / no force + user confirms runs purge",
			operation:      OperationDecommission,
			force:          false,
			confirmResp:    true,
			wantPurgeCalls: 1,
			wantAskCalls:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakeClient(t, tt.objs...)
			io := &stubIO{confirmResp: tt.confirmResp}

			err := PerformHostOperation(t.Context(), k8sClient, io, tt.operation, testNamespace, "worker-1-abc12", tt.force)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPurgeCalls, io.purgeN, "purge calls mismatch")
			assert.Equal(t, tt.wantAskCalls, io.confirmN, "confirm calls mismatch")
		})
	}
}

func TestPerformHostOperation_ExistingByoHost(t *testing.T) {
	byoHost := func(name string) *infrastructurev1beta1.ByoHost {
		return &infrastructurev1beta1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		}
	}

	tests := []struct {
		name                   string
		objs                   []runtime.Object
		operation              HostOperationType
		force                  bool
		byoHostName            string
		wantErr                bool
		wantErrContains        string
		wantPurgeCalls         int
		wantHostDeleted        bool
		wantRemaining          string
		wantRemainingNamespace string
	}{
		{
			name:            "deauthorise / host not attached to a machine",
			objs:            []runtime.Object{byoHost("worker-1-abc12")},
			operation:       OperationDeauthorise,
			byoHostName:     "worker-1-abc12",
			wantErr:         true,
			wantErrContains: "machineRef is not set",
			wantHostDeleted: false,
		},
		{
			name:            "decommission / host not attached to a machine",
			objs:            []runtime.Object{byoHost("worker-1-abc12")},
			operation:       OperationDecommission,
			byoHostName:     "worker-1-abc12",
			wantErr:         false,
			wantPurgeCalls:  1,
			wantHostDeleted: true,
		},
		{
			name:        "deauthorise / host not attached to a machine / force returns nil and leaves the host untouched",
			objs:        []runtime.Object{byoHost("worker-1-abc12")},
			operation:   OperationDeauthorise,
			force:       true,
			byoHostName: "worker-1-abc12",
		},
		{
			name:                   "decommission / only the bare hostname exists",
			objs:                   []runtime.Object{byoHost("worker-1")},
			operation:              OperationDecommission,
			force:                  true,
			byoHostName:            "worker-1-abc12",
			wantErr:                false,
			wantPurgeCalls:         1,
			wantHostDeleted:        false,
			wantRemaining:          "worker-1",
			wantRemainingNamespace: testNamespace,
		},
		{
			name: "decommission / same name only in another namespace",
			objs: []runtime.Object{
				&infrastructurev1beta1.ByoHost{
					ObjectMeta: metav1.ObjectMeta{Namespace: "other-namespace", Name: "worker-1-abc12"},
				},
			},
			operation:              OperationDecommission,
			force:                  true,
			byoHostName:            "worker-1-abc12",
			wantErr:                false,
			wantPurgeCalls:         1,
			wantHostDeleted:        false,
			wantRemaining:          "worker-1-abc12",
			wantRemainingNamespace: "other-namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakeClient(t, tt.objs...)
			io := &stubIO{}

			err := PerformHostOperation(t.Context(), k8sClient, io, tt.operation, testNamespace, tt.byoHostName, tt.force)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPurgeCalls, io.purgeN, "purge calls mismatch")
			_, getErr := k8sClient.GetByoHostObject(testNamespace, tt.byoHostName)
			if tt.wantHostDeleted {
				assert.Error(t, getErr, "ByoHost should be gone")
			}
			if tt.wantRemaining != "" {
				_, remainingErr := k8sClient.GetByoHostObject(tt.wantRemainingNamespace, tt.wantRemaining)
				assert.NoError(t, remainingErr, "ByoHost should be untouched")
			}
		})
	}
}

func TestPerformHostOperation_ManagementStepFailure(t *testing.T) {
	const hostName = "worker-1-abc12"

	attachedHost := &infrastructurev1beta1.ByoHost{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: hostName},
		Status: infrastructurev1beta1.ByoHostStatus{
			MachineRef: &corev1.ObjectReference{Namespace: testNamespace, Name: "machine-1"},
		},
	}
	machine := func(labels map[string]string) *capiv1beta1.Machine {
		return &capiv1beta1.Machine{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "machine-1", Labels: labels},
		}
	}
	machineDeployment := func(replicas int32) *capiv1beta1.MachineDeployment {
		return &capiv1beta1.MachineDeployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "md-1"},
			Spec:       capiv1beta1.MachineDeploymentSpec{Replicas: &replicas},
		}
	}
	mdLabels := map[string]string{"cluster.x-k8s.io/deployment-name": "md-1"}
	twoReplicas := int64(2)
	failVerb := func(verb, resource string) func(*dynamicfake.FakeDynamicClient) {
		return func(dyn *dynamicfake.FakeDynamicClient) {
			dyn.PrependReactor(verb, resource, func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("injected failure")
			})
		}
	}

	tests := []struct {
		name            string
		objs            []runtime.Object
		inject          func(*dynamicfake.FakeDynamicClient)
		operation       HostOperationType
		force           bool
		confirmResp     bool
		wantErrContains string
		wantPurgeCalls  int
		wantHostDeleted bool
		wantDecision    bool
		wantReplicas    *int64
	}{
		{
			name:            "decommission / machine object missing / no force stops before purge",
			objs:            []runtime.Object{attachedHost},
			operation:       OperationDecommission,
			wantErrContains: "failed to get machine object",
		},
		{
			name:            "decommission / machine object missing / force deletes the host and purges",
			objs:            []runtime.Object{attachedHost},
			operation:       OperationDecommission,
			force:           true,
			wantPurgeCalls:  1,
			wantHostDeleted: true,
		},
		{
			name:            "decommission / machine has no deployment label / force deletes the host and purges",
			objs:            []runtime.Object{attachedHost, machine(nil)},
			operation:       OperationDecommission,
			force:           true,
			wantPurgeCalls:  1,
			wantHostDeleted: true,
		},
		{
			name:            "decommission / annotating the machine fails / no force stops before purge",
			objs:            []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(2)},
			inject:          failVerb("update", "machines"),
			operation:       OperationDecommission,
			wantErrContains: "failed to annotate machine object",
		},
		{
			name:            "decommission / annotating the machine fails / force deletes the host and purges",
			objs:            []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(2)},
			inject:          failVerb("update", "machines"),
			operation:       OperationDecommission,
			force:           true,
			wantPurgeCalls:  1,
			wantHostDeleted: true,
			wantReplicas:    &twoReplicas,
		},
		{
			name:            "decommission / last node declined / force still stops",
			objs:            []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(1)},
			operation:       OperationDecommission,
			force:           true,
			confirmResp:     false,
			wantErrContains: "De-auth",
			wantDecision:    true,
		},
		{
			name:            "decommission / deleting the ByoHost fails / no force stops before purge",
			objs:            []runtime.Object{&infrastructurev1beta1.ByoHost{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: hostName}}},
			inject:          failVerb("delete", "byohosts"),
			operation:       OperationDecommission,
			wantErrContains: "failed to delete ByoHosts object",
		},
		{
			name:           "decommission / deleting the ByoHost fails / force still purges",
			objs:           []runtime.Object{&infrastructurev1beta1.ByoHost{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: hostName}}},
			inject:         failVerb("delete", "byohosts"),
			operation:      OperationDecommission,
			force:          true,
			wantPurgeCalls: 1,
		},
		{
			name:            "deauthorise / machine object missing / no force reports the failure",
			objs:            []runtime.Object{attachedHost},
			operation:       OperationDeauthorise,
			wantErrContains: "failed to get machine object",
		},
		{
			name:      "deauthorise / machine object missing / force returns nil and leaves the host untouched",
			objs:      []runtime.Object{attachedHost},
			operation: OperationDeauthorise,
			force:     true,
		},
		{
			name:            "deauthorise / annotating the machine fails / no force reports the failure",
			objs:            []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(2)},
			inject:          failVerb("update", "machines"),
			operation:       OperationDeauthorise,
			wantErrContains: "failed to annotate machine object",
		},
		{
			name:         "deauthorise / annotating the machine fails / force returns nil and leaves the host untouched",
			objs:         []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(2)},
			inject:       failVerb("update", "machines"),
			operation:    OperationDeauthorise,
			force:        true,
			wantReplicas: &twoReplicas,
		},
		{
			name:            "deauthorise / last node declined / force still stops",
			objs:            []runtime.Object{attachedHost, machine(mdLabels), machineDeployment(1)},
			operation:       OperationDeauthorise,
			force:           true,
			wantErrContains: "De-auth",
			wantDecision:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := fakeClient(t, tt.objs...)
			dyn, ok := k8sClient.DynamicClient.(*dynamicfake.FakeDynamicClient)
			require.True(t, ok)
			if tt.inject != nil {
				tt.inject(dyn)
			}
			io := &stubIO{confirmResp: tt.confirmResp}

			err := PerformHostOperation(t.Context(), k8sClient, io, tt.operation, testNamespace, hostName, tt.force)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPurgeCalls, io.purgeN, "purge calls mismatch")

			var decisionErr *userDecisionError
			isDecision := errors.As(err, &decisionErr)
			assert.Equal(t, tt.wantDecision, isDecision, "only a declined prompt is a user decision")

			if tt.wantReplicas != nil {
				// Scaling down without the delete-machine annotation would remove a different machine.
				deploymentGVR := capiv1beta1.GroupVersion.WithResource("machinedeployments")
				md, getMDErr := dyn.Resource(deploymentGVR).Namespace(testNamespace).Get(t.Context(), "md-1", metav1.GetOptions{})
				require.NoError(t, getMDErr)
				gotReplicas, found, nestedErr := unstructured.NestedInt64(md.Object, "spec", "replicas")
				require.NoError(t, nestedErr)
				require.True(t, found)
				assert.Equal(t, *tt.wantReplicas, gotReplicas)
			}

			// Reads bypass the injected reactors, which only fail the verb under test.
			_, getErr := k8sClient.GetByoHostObject(testNamespace, hostName)
			if tt.wantHostDeleted {
				assert.Error(t, getErr, "ByoHost should be gone")
			} else {
				assert.NoError(t, getErr, "ByoHost should still exist")
			}
		})
	}
}

func TestUserDecisionError(t *testing.T) {
	cause := errors.New("stdin closed")
	err := fmt.Errorf("wrapped: %w", &userDecisionError{err: cause})

	var decisionErr *userDecisionError
	require.True(t, errors.As(err, &decisionErr))
	assert.Equal(t, "stdin closed", decisionErr.Error())
	assert.ErrorIs(t, err, cause)
}
