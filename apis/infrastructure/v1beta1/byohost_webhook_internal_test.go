// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// notAnIdentityMsg is the denial for a username that carries no host
	// identity. It names the parse failure so the message says which rule the
	// request broke.
	notAnIdentityMsg := func(userName string) string {
		return fmt.Sprintf("%s is not a valid agent username: identity %q does not start with %q",
			userName, userName, hostIdentityPrefix)
	}

	testCases := []struct {
		name      string
		operation admissionv1.Operation
		userName  string
		hostName  string
		identity  string
		wantAllow bool
		wantMsg   string
	}{
		{
			name:      "create allowed from a valid agent username",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			identity:  byohHostOneUser,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			// Create requires the object to already carry the requester's
			// identity. Without it the stamping webhook was bypassed.
			name:      "create denied when the object carries no identity",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			identity:  "",
			wantAllow: false,
			wantMsg:   fmt.Sprintf("%s cannot create resource %s with identity %q", byohHostOneUser, defaultHostName, ""),
		},
		{
			// Update does not check the stamp, only that the identity names
			// this host.
			name:      "update allowed from a valid agent username",
			operation: admissionv1.Update,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "create denied from a username that is not a host identity",
			operation: admissionv1.Create,
			userName:  unauthorizedUser,
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg(unauthorizedUser),
		},
		{
			name:      "update denied from a username that is not a host identity",
			operation: admissionv1.Update,
			userName:  unauthorizedUser,
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg(unauthorizedUser),
		},
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
			wantMsg:   notAnIdentityMsg("user@localhost"),
		},
		{
			name:      "username missing the host identity segments is denied",
			operation: admissionv1.Create,
			userName:  "byoh:host",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg("byoh:host"),
		},
		{
			name:      "agent encoding a different host is denied",
			operation: admissionv1.Create,
			userName:  byohHostTwoUser,
			hostName:  defaultHostName,
			identity:  byohHostTwoUser,
			wantAllow: false,
			wantMsg:   byohHostTwoUser + " cannot create/update resource " + defaultHostName,
		},
		{
			// Host names are compared whole, so "host12" is not "host1".
			name:      "host name that only contains the encoded host is denied",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  "host12",
			identity:  byohHostOneUser,
			wantAllow: false,
			wantMsg:   byohHostOneUser + " cannot create/update resource host12",
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
				Spec: ByoHostSpec{
					Identity: tc.identity,
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

func TestByoHostIdentityStamper_Handle(t *testing.T) {
	byoHost := func(identity string) *ByoHost {
		return &ByoHost{
			TypeMeta: metav1.TypeMeta{
				Kind:       testByoHostKind,
				APIVersion: testAPIVersion,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      defaultHostName,
				Namespace: testNamespace,
			},
			Spec: ByoHostSpec{
				Identity: identity,
			},
		}
	}

	testCases := []struct {
		name          string
		operation     admissionv1.Operation
		userName      string
		rawObject     []byte
		existingIdent string
		wantAllow     bool
		wantCode      int32
		wantIdentity  string
		wantOp        string
		wantNoPatches bool
	}{
		{
			name:          "update request is allowed with no patches",
			operation:     admissionv1.Update,
			userName:      byohHostOneUser,
			existingIdent: "",
			wantAllow:     true,
			wantNoPatches: true,
		},
		{
			name:      "create request with malformed raw bytes is errored",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			rawObject: []byte("{not json"),
			wantAllow: false,
			wantCode:  http.StatusBadRequest,
		},
		{
			name:          "create from a username that is not a host identity is allowed with no patches",
			operation:     admissionv1.Create,
			userName:      testRequester,
			existingIdent: "",
			wantAllow:     true,
			wantNoPatches: true,
		},
		{
			name:          "create from a four-segment host identity is stamped",
			operation:     admissionv1.Create,
			userName:      byohHostOneUser,
			existingIdent: "",
			wantAllow:     true,
			wantIdentity:  byohHostOneUser,
			wantOp:        "add",
		},
		{
			name:          "create from a three-segment host identity is stamped",
			operation:     admissionv1.Create,
			userName:      "byoh:host:host1",
			existingIdent: "",
			wantAllow:     true,
			wantIdentity:  "byoh:host:host1",
			wantOp:        "add",
		},
		{
			name:          "create replaces the old suffix of the same host with the requester's new suffix",
			operation:     admissionv1.Create,
			userName:      "byoh:host:host1:new02",
			existingIdent: "byoh:host:host1:old01",
			wantAllow:     true,
			wantIdentity:  "byoh:host:host1:new02",
			wantOp:        "replace",
		},
	}

	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	require.NoError(t, err)
	decoder := admission.NewDecoder(scheme)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.rawObject
			if raw == nil {
				obj := byoHost(tc.existingIdent)
				marshaled, err := json.Marshal(obj)
				require.NoError(t, err)
				raw = marshaled
			}

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tc.operation,
					UserInfo:  v1.UserInfo{Username: tc.userName},
					Object: runtime.RawExtension{
						Raw: raw,
					},
				},
			}

			s := &ByoHostIdentityStamper{Decoder: decoder}
			resp := s.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			if tc.wantCode != 0 {
				require.NotNil(t, resp.Result)
				assert.Equal(t, tc.wantCode, resp.Result.Code)
				return
			}

			if tc.wantNoPatches {
				assert.Empty(t, resp.Patches)
				return
			}

			require.Len(t, resp.Patches, 1)
			patch := resp.Patches[0]
			assert.Equal(t, tc.wantOp, patch.Operation)
			assert.Equal(t, "/spec/identity", patch.Path)
			assert.Equal(t, tc.wantIdentity, patch.Value)
		})
	}
}

func TestHostNameFromIdentity(t *testing.T) {
	testCases := []struct {
		name     string
		identity string
		want     string
		wantErr  string
	}{
		{
			name:     "valid identity",
			identity: "byoh:host:example-worker-1:x7k2p",
			want:     "example-worker-1",
		},
		{
			name:     "identity without the prefix is rejected",
			identity: "someone@example.com",
			wantErr:  "does not start with",
		},
		{
			name:     "identity without suffix is allowed",
			identity: "byoh:host:example-worker-1",
			want:     "example-worker-1",
		},
		{
			name:     "identity with extra segments is rejected",
			identity: "byoh:host:example-worker-1:x7k2p:extra",
			wantErr:  "is not of the form",
		},
		{
			name:     "empty host name is rejected",
			identity: "byoh:host::x7k2p",
			wantErr:  "empty host name",
		},
		{
			name:     "empty host name without suffix is rejected",
			identity: "byoh:host:",
			wantErr:  "empty host name",
		},
		{
			name:     "identity with only two segments is rejected",
			identity: "byoh:host",
			wantErr:  "does not start with",
		},
		{
			name:     "identity with six segments is rejected",
			identity: "byoh:host:example-worker-1:x7k2p:extra:more",
			wantErr:  "is not of the form",
		},
		{
			name:     "empty suffix is rejected",
			identity: "byoh:host:example-worker-1:",
			wantErr:  "empty suffix",
		},
		{
			name:     "hostname has another host as substring (worker-1 vs worker-12)",
			identity: "byoh:host:example-worker-12:x7k2p",
			want:     "example-worker-12",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HostNameFromIdentity(tc.identity)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
