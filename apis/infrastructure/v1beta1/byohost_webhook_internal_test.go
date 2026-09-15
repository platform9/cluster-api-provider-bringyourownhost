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
	byohHostOneUser  = "byoh:host:host1:x7k2p"
	byohHostTwoUser  = "byoh:host:host2:x7k2p"
	unauthorizedUser = "unauthorized-user"
)

// testNamespace defines the namespace where the objects in test are created.
const testNamespace = "default"

// newFakeByoHostValidator builds a validator over a fake client seeded with objs.
func newFakeByoHostValidator(t *testing.T, getErr error, objs ...client.Object) *ByoHostValidator {
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

func TestByoHostValidator_handleDelete(t *testing.T) {
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
			wantMsg:   "apiserver unreachable",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeByoHostValidator(t, tc.getErr, byoMachine)

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
					MachineRef: tc.machineRef,
				},
			}
			byoHostRaw, err := json.Marshal(byoHost)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					UserInfo:  v1.UserInfo{Username: "random-user"},
					OldObject: runtime.RawExtension{
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

func TestByoHostValidator_handleCreateUpdate(t *testing.T) {
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
		// BEGIN NOTE: The ideal behavior we're testing here is that manager
		// service accounts are in the allowlist. But today this test will
		// still pass even if they're not in the allowlist because we don't
		// actually check for the number of colon separated segments.
		{

			name:      "byoh-system manager service account is allowed",
			operation: admissionv1.Update,
			userName:  byohSystemManagerServiceAccount,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "kaapi manager service account is allowed",
			operation: admissionv1.Update,
			userName:  kaapiManagerServiceAccount,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		// END NOTE
		{
			name:      "email-like username is allowed",
			operation: admissionv1.Update,
			userName:  "user@example.com",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "email-like username without a TLD is denied",
			operation: admissionv1.Update,
			userName:  "user@localhost",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   "user@localhost is not a valid agent username",
		},
		{
			name:      "username with no host segment is allowed",
			operation: admissionv1.Create,
			userName:  "byoh:host",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			// NOTE: The host-ownership check is commented out in the webhook
			// while only token-based kubeconfigs are supported, so an agent
			// encoding a different host is still allowed through. This test
			// will fail once the check is enabled.
			name:      "agent encoding a different host is allowed",
			operation: admissionv1.Create,
			userName:  byohHostTwoUser,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			// NOTE: Similar to above, but tests that a partial match of a
			// hostname is allowed ("host12" contains "host1"). The test will
			// also fail once the check is enabled.
			name:      "host name containing the encoded host is allowed",
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

			v := newFakeByoHostValidator(t, nil)

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
