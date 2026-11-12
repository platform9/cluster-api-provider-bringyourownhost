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

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

const (
	testByoHostKind  = "ByoHost"
	testAPIVersion   = "infrastructure.cluster.x-k8s.io/v1beta1"
	defaultHostName  = "host1-x7k2p"
	unauthorizedUser = "unauthorized-user"
)

// Valid host identities used as the requesting user.
var (
	byohHostOneUser = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "host1",
		Suffix:             "x7k2p",
	}.String()
	byohHostTwoUser = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "host2",
		Suffix:             "x7k2p",
	}.String()
	worker1User = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "worker-1",
		Suffix:             "abcde",
	}.String()
	worker10User = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "worker-10",
		Suffix:             "abcde",
	}.String()
	worker1OtherSuffixUser = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "worker-1",
		Suffix:             "zzzzz",
	}.String()
	suffixLikeUser = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "web-abcde",
		Suffix:             "xyzwq",
	}.String()
	hyphenTenantUser = identity.HostIdentity{
		TenantNamespace:    "a-b-c",
		NormalizedHostName: "host1",
		Suffix:             "x7k2p",
	}.String()
	otherTenantUser = identity.HostIdentity{
		TenantNamespace:    "other",
		NormalizedHostName: "host1",
		Suffix:             "x7k2p",
	}.String()
	newSuffixUser = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "host1",
		Suffix:             "new02",
	}.String()
	oldSuffixIdentity = identity.HostIdentity{
		TenantNamespace:    "default",
		NormalizedHostName: "host1",
		Suffix:             "old01",
	}.String()
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
		return fmt.Sprintf("%s is not a valid agent username: identity %q is invalid: does not start with %q",
			userName, userName, identity.Prefix)
	}

	testCases := []struct {
		name      string
		operation admissionv1.Operation
		userName  string
		hostName  string
		identity  string
		// oldIdentity is the identity stored on the existing host, used on update.
		oldIdentity string
		namespace   string // namespace of the admission request, testNamespace when empty
		wantAllow   bool
		wantMsg     string
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
			// Update compares the identity stored on the existing host.
			name:        "update allowed from a valid agent username",
			operation:   admissionv1.Update,
			userName:    byohHostOneUser,
			hostName:    defaultHostName,
			oldIdentity: byohHostOneUser,
			wantAllow:   true,
			wantMsg:     "",
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
			name:      "du-prefixed email username is allowed",
			operation: admissionv1.Update,
			userName:  "test-du-ciprsmoke-5267121:pf9@platform9.com",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "du-prefixed email username without a TLD is denied",
			operation: admissionv1.Update,
			userName:  "some-du:user@localhost",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg("some-du:user@localhost"),
		},
		{
			name:      "email username with more than one prefix segment is denied",
			operation: admissionv1.Update,
			userName:  "a:b:user@example.com",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg("a:b:user@example.com"),
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
			// Existing agents authenticate as the byoh ServiceAccount of the
			// namespace their ByoHost lives in.
			name:      "byoh service account in the host's namespace is allowed",
			operation: admissionv1.Create,
			userName:  "system:serviceaccount:" + testNamespace + ":byoh",
			hostName:  defaultHostName,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "byoh service account from another namespace is denied",
			operation: admissionv1.Create,
			userName:  "system:serviceaccount:other:byoh",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg("system:serviceaccount:other:byoh"),
		},
		{
			name:      "another service account in the host's namespace is denied",
			operation: admissionv1.Create,
			userName:  "system:serviceaccount:" + testNamespace + ":default",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   notAnIdentityMsg("system:serviceaccount:" + testNamespace + ":default"),
		},
		{
			name:      "agent encoding a different host is denied",
			operation: admissionv1.Create,
			userName:  byohHostTwoUser,
			hostName:  defaultHostName,
			identity:  byohHostTwoUser,
			wantAllow: false,
			wantMsg:   fmt.Sprintf("%s cannot create resource %s", byohHostTwoUser, defaultHostName),
		},
		{
			// Names are compared whole, so "host1-x7k2p0" is not "host1-x7k2p".
			name:      "name that only starts with the encoded name is denied",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  "host1-x7k2p0",
			identity:  byohHostOneUser,
			wantAllow: false,
			wantMsg:   fmt.Sprintf("%s cannot create resource %s", byohHostOneUser, "host1-x7k2p0"),
		},
		{
			name:      "name lacking the suffix is denied",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  "host1",
			identity:  byohHostOneUser,
			wantAllow: false,
			wantMsg:   fmt.Sprintf("%s cannot create resource %s", byohHostOneUser, "host1"),
		},
		{
			name:        "worker-1 certificate updating a host stored under worker-10 is denied",
			operation:   admissionv1.Update,
			userName:    worker1User,
			hostName:    "worker-10-abcde",
			oldIdentity: worker10User,
			wantAllow:   false,
			wantMsg:     worker1User + " cannot update resource worker-10-abcde",
		},
		{
			name:        "worker-1 certificate updating a host stored under another worker-1 suffix is denied",
			operation:   admissionv1.Update,
			userName:    worker1User,
			hostName:    "worker-1-zzzzz",
			oldIdentity: worker1OtherSuffixUser,
			wantAllow:   false,
			wantMsg:     worker1User + " cannot update resource worker-1-zzzzz",
		},
		{
			name:        "hostname that ends like a suffix is split from the right and allowed",
			operation:   admissionv1.Update,
			userName:    suffixLikeUser,
			hostName:    "web-abcde-xyzwq",
			oldIdentity: suffixLikeUser,
			wantAllow:   true,
		},
		{
			name:        "tenant with hyphens is allowed in its own namespace",
			operation:   admissionv1.Update,
			userName:    hyphenTenantUser,
			hostName:    defaultHostName,
			oldIdentity: hyphenTenantUser,
			namespace:   "a-b-c",
			wantAllow:   true,
		},
		{
			// The tenant is compared with the namespace of the request, not
			// the namespace in the object body.
			name:      "same host name in a different tenant is denied",
			operation: admissionv1.Update,
			userName:  otherTenantUser,
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   otherTenantUser + " cannot create/update resource " + defaultHostName,
		},
		{
			name:      "legacy host identity without a tenant is denied",
			operation: admissionv1.Update,
			userName:  "byoh:host:host1:x7k2p",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   "byoh:host:host1:x7k2p is not a valid agent username: identity \"byoh:host:host1:x7k2p\" is invalid: has a name \"x7k2p\" too short to end in -<suffix>",
		},
		{
			// A host certificate must never reach the email bypass.
			name:      "host identity with an email-looking tail is not allowed through the email bypass",
			operation: admissionv1.Update,
			userName:  "byoh:host:user@example.com",
			hostName:  defaultHostName,
			wantAllow: false,
			wantMsg:   "byoh:host:user@example.com is not a valid agent username: identity \"byoh:host:user@example.com\" is invalid: is not of the form byoh:host:<tenant>:<hostName>-<suffix>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.operation, "operation must be set, otherwise Handle falls through to its default allow branch")

			v := newFakeByoHostValidator(t, nil)

			namespace := tc.namespace
			if namespace == "" {
				namespace = testNamespace
			}

			byoHost := &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      tc.hostName,
					Namespace: namespace,
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
					Namespace: namespace,
					UserInfo:  v1.UserInfo{Username: tc.userName},
					Object: runtime.RawExtension{
						Raw:    byoHostRaw,
						Object: byoHost,
					},
				},
			}

			if tc.operation == admissionv1.Update {
				oldHost := byoHost.DeepCopy()
				oldHost.Spec.Identity = tc.oldIdentity
				oldHostRaw, marshalErr := json.Marshal(oldHost)
				require.NoError(t, marshalErr)
				req.OldObject = runtime.RawExtension{Raw: oldHostRaw}
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
		wantIdentity  any
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
			name:          "create from a username that is not a host identity, with no identity set, is allowed with no patches",
			operation:     admissionv1.Create,
			userName:      testRequester,
			existingIdent: "",
			wantAllow:     true,
			wantNoPatches: true,
		},
		{
			name:          "create from an email-like user clears an identity planted in the body",
			operation:     admissionv1.Create,
			userName:      testRequester,
			existingIdent: byohHostOneUser,
			wantAllow:     true,
			wantIdentity:  nil,
			wantOp:        "remove",
		},
		{
			name:          "create from a service account clears an identity planted in the body",
			operation:     admissionv1.Create,
			userName:      "system:serviceaccount:" + testNamespace + ":byoh",
			existingIdent: byohHostOneUser,
			wantAllow:     true,
			wantIdentity:  nil,
			wantOp:        "remove",
		},
		{
			name:          "create from a host identity is stamped",
			operation:     admissionv1.Create,
			userName:      byohHostOneUser,
			existingIdent: "",
			wantAllow:     true,
			wantIdentity:  byohHostOneUser,
			wantOp:        "add",
		},
		{
			name:          "create from a legacy host identity without a tenant is not stamped",
			operation:     admissionv1.Create,
			userName:      "byoh:host:host1",
			existingIdent: "",
			wantAllow:     true,
			wantNoPatches: true,
		},
		{
			name:          "create replaces the old suffix of the same host with the requester's new suffix",
			operation:     admissionv1.Create,
			userName:      newSuffixUser,
			existingIdent: oldSuffixIdentity,
			wantAllow:     true,
			wantIdentity:  newSuffixUser,
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

func TestByoHostValidator_handleDelete_hostCertificate(t *testing.T) {
	testCases := []struct {
		name      string
		userName  string
		namespace string
		objName   string
		identity  string
		wantAllow bool
		wantMsg   string
	}{
		{
			name:      "certificate deleting its own host is allowed",
			userName:  byohHostOneUser,
			namespace: testNamespace,
			objName:   defaultHostName,
			identity:  byohHostOneUser,
			wantAllow: true,
		},
		{
			name:      "certificate for another host is denied",
			userName:  byohHostTwoUser,
			namespace: testNamespace,
			objName:   defaultHostName,
			identity:  byohHostOneUser,
			wantAllow: false,
			wantMsg:   byohHostTwoUser + " cannot delete resource " + defaultHostName,
		},
		{
			name:      "certificate for another tenant is denied",
			userName:  otherTenantUser,
			namespace: testNamespace,
			objName:   defaultHostName,
			wantAllow: false,
			wantMsg:   otherTenantUser + " cannot delete resource " + defaultHostName,
		},
		{
			name:      "worker-1 certificate deleting a host stored under worker-10 is denied",
			userName:  worker1User,
			namespace: testNamespace,
			objName:   "worker-10-abcde",
			identity:  worker10User,
			wantAllow: false,
			wantMsg:   worker1User + " cannot delete resource worker-10-abcde",
		},
		{
			name:      "worker-1 certificate deleting a host stored under another worker-1 suffix is denied",
			userName:  worker1User,
			namespace: testNamespace,
			objName:   "worker-1-zzzzz",
			identity:  worker1OtherSuffixUser,
			wantAllow: false,
			wantMsg:   worker1User + " cannot delete resource worker-1-zzzzz",
		},
		{
			name:      "malformed host certificate is denied",
			userName:  "byoh:host:default:host1",
			namespace: testNamespace,
			objName:   defaultHostName,
			wantAllow: false,
			wantMsg:   "byoh:host:default:host1 is not a valid agent username: identity \"byoh:host:default:host1\" is invalid: has a name \"host1\" too short to end in -<suffix>",
		},
		{
			name:      "caller that is not a host certificate is unchanged",
			userName:  "random-user",
			namespace: testNamespace,
			objName:   defaultHostName,
			wantAllow: true,
		},
		{
			name:      "manager service account is unchanged",
			userName:  byohSystemManagerServiceAccount,
			namespace: testNamespace,
			objName:   defaultHostName,
			wantAllow: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeByoHostValidator(t, nil)

			byoHost := &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      tc.objName,
					Namespace: tc.namespace,
				},
				Spec: ByoHostSpec{
					Identity: tc.identity,
				},
			}
			byoHostRaw, err := json.Marshal(byoHost)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					Namespace: tc.namespace,
					UserInfo:  v1.UserInfo{Username: tc.userName},
					OldObject: runtime.RawExtension{Raw: byoHostRaw},
				},
			}

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}

// TestByoHostValidator_Handle_storedIdentity covers how the validator compares
// the requester with the identity stored on the existing ByoHost.
func TestByoHostValidator_Handle_storedIdentity(t *testing.T) {
	// legacyLookalikeUser is a certificate whose encoded name equals the name
	// of a ByoHost that was created before identities were stamped.
	legacyLookalikeUser := identity.HostIdentity{
		TenantNamespace:    testNamespace,
		NormalizedHostName: "foo",
		Suffix:             "bar12",
	}.String()
	const legacyHostName = "foo-bar12"
	legacyServiceAccount := "system:serviceaccount:" + testNamespace + ":byoh"

	testCases := []struct {
		name      string
		operation admissionv1.Operation
		userName  string
		hostName  string
		// identity is the identity on the object being created or updated.
		identity string
		// oldIdentity is the identity stored on the existing host.
		oldIdentity string
		wantAllow   bool
		wantMsg     string
	}{
		{
			name:        "certificate named like a legacy host cannot update it",
			operation:   admissionv1.Update,
			userName:    legacyLookalikeUser,
			hostName:    legacyHostName,
			identity:    legacyLookalikeUser,
			oldIdentity: "",
			wantAllow:   false,
			wantMsg:     legacyLookalikeUser + " cannot update resource " + legacyHostName,
		},
		{
			name:        "certificate named like a legacy host cannot delete it",
			operation:   admissionv1.Delete,
			userName:    legacyLookalikeUser,
			hostName:    legacyHostName,
			oldIdentity: "",
			wantAllow:   false,
			wantMsg:     legacyLookalikeUser + " cannot delete resource " + legacyHostName,
		},
		{
			name:        "host updating its own ByoHost",
			operation:   admissionv1.Update,
			userName:    byohHostOneUser,
			hostName:    defaultHostName,
			identity:    byohHostOneUser,
			oldIdentity: byohHostOneUser,
			wantAllow:   true,
			wantMsg:     "",
		},
		{
			name:        "update that rewrites the identity of another host's ByoHost",
			operation:   admissionv1.Update,
			userName:    byohHostOneUser,
			hostName:    defaultHostName,
			identity:    byohHostOneUser,
			oldIdentity: byohHostTwoUser,
			wantAllow:   false,
			wantMsg:     byohHostOneUser + " cannot update resource " + defaultHostName,
		},
		{
			name:        "host deleting its own ByoHost",
			operation:   admissionv1.Delete,
			userName:    byohHostOneUser,
			hostName:    defaultHostName,
			oldIdentity: byohHostOneUser,
			wantAllow:   true,
			wantMsg:     "",
		},
		{
			name:      "create with matching name and stamped identity",
			operation: admissionv1.Create,
			userName:  byohHostOneUser,
			hostName:  defaultHostName,
			identity:  byohHostOneUser,
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:        "legacy service account updating a host without an identity",
			operation:   admissionv1.Update,
			userName:    legacyServiceAccount,
			hostName:    legacyHostName,
			identity:    "",
			oldIdentity: "",
			wantAllow:   true,
			wantMsg:     "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeByoHostValidator(t, nil)

			newHost := &ByoHost{
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
			newHostRaw, err := json.Marshal(newHost)
			require.NoError(t, err)

			oldHost := newHost.DeepCopy()
			oldHost.Spec.Identity = tc.oldIdentity
			oldHostRaw, err := json.Marshal(oldHost)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tc.operation,
					Namespace: testNamespace,
					UserInfo:  v1.UserInfo{Username: tc.userName},
				},
			}
			if tc.operation == admissionv1.Delete {
				req.OldObject = runtime.RawExtension{Raw: oldHostRaw}
			} else {
				req.Object = runtime.RawExtension{Raw: newHostRaw}
			}
			if tc.operation == admissionv1.Update {
				req.OldObject = runtime.RawExtension{Raw: oldHostRaw}
			}

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}
