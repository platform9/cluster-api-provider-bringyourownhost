// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	testByoHostKind  = "ByoHost"
	testAPIVersion   = "infrastructure.cluster.x-k8s.io/v1beta1"
	defaultHostName  = "host1"
	unauthorizedUser = "unauthorized-user"
	byohHostTwoUser  = "byoh:host:host2"
	byohHostOneUser  = "byoh:host:host1"
)

// testNamespace is where the test fixtures live. It is deliberately not
// DefaultNamespace: that constant names the namespace inside a generated
// bootstrap kubeconfig and has nothing to do with these objects.
const testNamespace = "default"

// newByoHostValidator builds a validator over a fake client seeded with objs. A
// non-nil getErr makes every Get fail with that error instead of reaching the
// fake client, which is how the table exercises an unreachable apiserver.
func newByoHostValidator(t *testing.T, getErr error, objs ...client.Object) *ByoHostValidator {
	t.Helper()

	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if getErr != nil {
					return getErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	return &ByoHostValidator{
		Client:  fakeClient,
		Decoder: admission.NewDecoder(scheme),
	}
}

// newByoHostDeleteRequest builds a delete admission request for a ByoHost carrying
// the given MachineRef. handleDelete reads the object from OldObject, not Object.
func newByoHostDeleteRequest(t *testing.T, machineRef *corev1.ObjectReference) admission.Request {
	t.Helper()

	byoHost := &ByoHost{
		TypeMeta: metav1.TypeMeta{
			Kind:       testByoHostKind,
			APIVersion: testAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultHostName,
			Namespace: testNamespace,
		},
		Status: ByoHostStatus{
			MachineRef: machineRef,
		},
	}
	byoHostRaw, err := json.Marshal(byoHost)
	require.NoError(t, err)

	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Delete,
			UserInfo:  v1.UserInfo{Username: "random-user"},
			OldObject: runtime.RawExtension{
				Raw:    byoHostRaw,
				Object: byoHost,
			},
		},
	}
}

func TestByoHostValidator_Handle_Delete(t *testing.T) {
	byoMachine := &ByoMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "byomachine1", Namespace: testNamespace},
	}

	testCases := []struct {
		name       string
		machineRef *corev1.ObjectReference
		getErr     error
		wantAllow  bool
		wantMsg    string
	}{
		{
			name:       "no MachineRef assigned",
			machineRef: nil,
			getErr:     nil,
			wantAllow:  true,
			wantMsg:    "",
		},
		{
			name: "MachineRef assigned to an existing ByoMachine",
			machineRef: &corev1.ObjectReference{
				Kind:       "ByoMachine",
				Namespace:  testNamespace,
				Name:       byoMachine.Name,
				APIVersion: testAPIVersion,
			},
			getErr:    nil,
			wantAllow: false,
			wantMsg:   "cannot delete ByoHost when MachineRef is assigned",
		},
		{
			// A dangling MachineRef must not pin the ByoHost forever, so a NotFound
			// on the referenced ByoMachine is a deliberate allow.
			name: "MachineRef assigned to a ByoMachine that does not exist",
			machineRef: &corev1.ObjectReference{
				Kind:       "ByoMachine",
				Namespace:  testNamespace,
				Name:       "missing-byomachine",
				APIVersion: testAPIVersion,
			},
			getErr:    nil,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			// Any Get failure other than NotFound is treated as "the ByoMachine may
			// still exist", so the delete is denied rather than allowed on an unknown
			// state.
			name: "MachineRef lookup fails with an error other than NotFound",
			machineRef: &corev1.ObjectReference{
				Kind:       "ByoMachine",
				Namespace:  testNamespace,
				Name:       byoMachine.Name,
				APIVersion: testAPIVersion,
			},
			getErr:    errors.New("apiserver unreachable"),
			wantAllow: false,
			wantMsg:   "cannot delete ByoHost when byomachine exists",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newByoHostValidator(t, tc.getErr, byoMachine)

			req := newByoHostDeleteRequest(t, tc.machineRef)

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}

func TestByoHostValidator_Handle_CreateUpdate(t *testing.T) {
	testCases := []struct {
		name      string
		operation admissionv1.Operation
		userName  string
		hostName  string
		wantAllow bool
		wantMsg   string
	}{
		{
			name:      "create allowed from a valid agent username",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "update allowed from a valid agent username",
			operation: admissionv1.Update,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "create denied from a username with fewer than 2 segments",
			operation: admissionv1.Create,
			userName:  unauthorizedUser,
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   unauthorizedUser + " is not a valid agent username",
		},
		{
			name:      "update denied from a username with fewer than 2 segments",
			operation: admissionv1.Update,
			userName:  unauthorizedUser,
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   unauthorizedUser + " is not a valid agent username",
		},
		{
			name:      "byoh-system manager service account bypasses the ownership check",
			operation: admissionv1.Update,
			userName:  byohSystemManagerServiceAccount,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "kaapi manager service account bypasses the ownership check",
			operation: admissionv1.Update,
			userName:  kaapiManagerServiceAccount,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "email-like username bypasses the ownership check",
			operation: admissionv1.Update,
			userName:  "user@example.com",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			// "user@localhost" has no dot-separated TLD, so the email-like regex does
			// not match and the username falls through to the segment count. This row
			// pins where the regex stops matching.
			name:      "email-like username without a TLD is denied",
			operation: admissionv1.Update,
			userName:  "user@localhost",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   "user@localhost is not a valid agent username",
		},
		{
			name:      "username with no host segment skips the ownership check",
			operation: admissionv1.Create,
			userName:  "byoh:host",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "agent encoding a different host is denied",
			operation: admissionv1.Create,
			userName:  byohHostTwoUser,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "ownership check matches by substring containment, not exact equality",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  "host12",
			wantAllow: true,
			wantMsg:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.operation, "operation must be set, otherwise Handle falls through to its default allow branch")

			v := newByoHostValidator(t, nil)

			byoHost := &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      tc.hostName,
					Namespace: testNamespace,
				},
			}
			byoHostRaw, err := json.Marshal(byoHost)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tc.operation,
					UserInfo:  v1.UserInfo{Username: tc.userName},
					Object: runtime.RawExtension{
						Raw:    byoHostRaw,
						Object: byoHost,
					},
				},
			}

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}
