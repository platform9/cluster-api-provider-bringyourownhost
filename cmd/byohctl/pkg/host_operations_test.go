// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package pkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
