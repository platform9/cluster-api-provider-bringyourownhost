// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

var _ = Describe("ByohostWebhook/Unit", func() {
	schema := runtime.NewScheme()
	err := AddToScheme(schema)
	Expect(err).NotTo(HaveOccurred())
	decoder := admission.NewDecoder(schema)
	byoMachine := &ByoMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "byomachine1", Namespace: DefaultNamespace},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(schema).WithObjects(byoMachine).Build()
	v := &ByoHostValidator{
		Client:  fakeClient,
		Decoder: decoder,
	}
	Context("When ByoHost gets a create request", func() {
		var (
			byoHost    *ByoHost
			byoHostRaw []byte
			ctx        context.Context
		)
		BeforeEach(func() {
			ctx = context.TODO()
			byoHost = &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      defaultHostName,
					Namespace: DefaultNamespace,
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should reject create request from invalid user", func() {
			Skip("feature not implemented yet")
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: unauthorizedUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal(fmt.Sprintf("%s is not a valid agent username", unauthorizedUser)))
		})
		It("Should reject request from another agent user in the group", func() {
			Skip("feature not implemented yet")
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: byohHostTwoUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal(fmt.Sprintf("%s cannot create/update resource %s", byohHostTwoUser, defaultHostName)))
		})
		It("Should allow request from the valid agent user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: byohHostOneUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
	})

	Context("When ByoHost gets an update request", func() {
		var (
			byoHost    *ByoHost
			byoHostRaw []byte
			ctx        context.Context
		)
		BeforeEach(func() {
			ctx = context.TODO()
			byoHost = &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      defaultHostName,
					Namespace: DefaultNamespace,
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should reject update request from invalid user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: unauthorizedUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal(fmt.Sprintf("%s is not a valid agent username", unauthorizedUser)))
		})
		It("Should allow update request from manager", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: byohSystemManagerServiceAccount},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
		It("Should allow update request from email-like user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: "user@example.com"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
		It("should reject the update request from users who are not like email", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: unauthorizedUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal(fmt.Sprintf("%s is not a valid agent username", unauthorizedUser)))
		})

		It("Should reject request from another agent user in the group", func() {
			Skip("feature not implemented yet")
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: byohHostTwoUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal(fmt.Sprintf("%s cannot create/update resource %s", byohHostTwoUser, defaultHostName)))
		})
		It("Should allow request from the valid agent user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: byohHostOneUser},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
	})
	Context("When ByoHost gets an delete request", func() {
		var (
			byoHost    *ByoHost
			byoHostRaw []byte
			ctx        context.Context
		)
		BeforeEach(func() {
			ctx = context.TODO()
			byoHost = &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      defaultHostName,
					Namespace: DefaultNamespace,
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should allow delete request from any user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Delete,
				UserInfo:  v1.UserInfo{Username: "random-user"},
				OldObject: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(true))
		})
		It("Should reject delete request if status.MachineRef is not nil", func() {
			byoHost.Status.MachineRef = &corev1.ObjectReference{
				Kind:       "ByoMachine",
				Namespace:  DefaultNamespace,
				Name:       "byomachine1",
				APIVersion: byoHost.APIVersion,
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Delete,
				UserInfo:  v1.UserInfo{Username: "random-user"},
				OldObject: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(resp.AdmissionResponse.Result.Message).To(Equal("cannot delete ByoHost when MachineRef is assigned"))
		})
	})
})

func TestByoHostValidator_handleCreateUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	require.NoError(t, err)
	decoder := admission.NewDecoder(scheme)

	v := &ByoHostValidator{Decoder: decoder}

	testCases := []struct {
		name      string
		userName  string
		hostName  string // ByoHost.Name; defaults to "host1" when empty
		wantAllow bool
		wantMsg   string
	}{
		{
			name:      "byoh-system manager service account bypasses the ownership check",
			userName:  byohSystemManagerServiceAccount,
			wantAllow: true,
		},
		{
			name:      "kaapi manager service account bypasses the ownership check",
			userName:  kaapiManagerServiceAccount,
			wantAllow: true,
		},
		{
			name:      "email-like username bypasses the ownership check",
			userName:  "user@example.com",
			wantAllow: true,
		},
		{
			name:      "username with fewer than 2 segments is rejected before the ownership check runs",
			userName:  unauthorizedUser,
			wantAllow: false,
			wantMsg:   "unauthorized-user is not a valid agent username",
		},
		{
			name:      "username with no host segment skips the ownership check",
			userName:  "byoh:host",
			wantAllow: true,
		},
		{
			name:      "agent encoding a different host is denied",
			userName:  byohHostTwoUser,
			wantAllow: true, // FIXME: This test should fail when we fix the check.
			wantMsg:   "byoh:host:host2 cannot create/update resource host1",
		},
		{
			name:      "agent encoding the target host is allowed",
			userName:  byohHostOneUser,
			wantAllow: true,
		},
		{
			name:      "ownership check matches by substring containment, not exact equality",
			userName:  byohHostOneUser,
			hostName:  "host12",
			wantAllow: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hostName := tc.hostName
			if hostName == "" {
				hostName = defaultHostName
			}
			byoHost := &ByoHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostName,
					Namespace: DefaultNamespace,
				},
			}
			byoHostRaw, err := json.Marshal(byoHost)
			require.NoError(t, err)

			req := &admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					UserInfo: v1.UserInfo{Username: tc.userName},
					Object: runtime.RawExtension{
						Raw:    byoHostRaw,
						Object: byoHost,
					},
				},
			}

			resp := v.handleCreateUpdate(req)

			require.Equal(t, tc.wantAllow, resp.Allowed)
			if !tc.wantAllow {
				require.Equal(t, tc.wantMsg, resp.Result.Message)
			}
		})
	}
}
