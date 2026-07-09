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

var _ = Describe("ByohostWebhook/Unit", func() {
	schema := runtime.NewScheme()
	err := AddToScheme(schema)
	Expect(err).NotTo(HaveOccurred())
	decoder, _ := admission.NewDecoder(schema)
	byoMachine := &ByoMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "byomachine1", Namespace: "default"},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(schema).WithObjects(byoMachine).Build()
	v := &ByoHostValidator{
		Client:  fakeClient,
		decoder: decoder,
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
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should reject create request from invalid user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: "unauthorized-user"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal(fmt.Sprintf("%s is not a valid agent username", "unauthorized-user")))
		})
		It("Should reject request from another agent user in the group", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: "byoh:host:host2"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal(fmt.Sprintf("%s cannot create/update resource %s", "byoh:host:host2", "host1")))
		})
		It("Should allow request from the valid agent user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Create,
				UserInfo:  v1.UserInfo{Username: "byoh:host:host1"},
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
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
				},
				Spec: ByoHostSpec{},
			}
			byoHostRaw, err = json.Marshal(byoHost)
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("Should reject update request from invalid user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: "unauthorized-user"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal(fmt.Sprintf("%s is not a valid agent username", "unauthorized-user")))
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
				UserInfo:  v1.UserInfo{Username: "unauthorized-user"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal(fmt.Sprintf("%s is not a valid agent username", "unauthorized-user")))
		})

		It("Should reject request from another agent user in the group", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: "byoh:host:host2"},
				Object: runtime.RawExtension{
					Raw:    byoHostRaw,
					Object: byoHost,
				},
			}
			resp := v.Handle(ctx, admission.Request{AdmissionRequest: admissionRequest})
			Expect(resp.AdmissionResponse.Allowed).To(Equal(false))
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal(fmt.Sprintf("%s cannot create/update resource %s", "byoh:host:host2", "host1")))
		})
		It("Should allow request from the valid agent user", func() {
			admissionRequest := admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				UserInfo:  v1.UserInfo{Username: "byoh:host:host1"},
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
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "host1",
					Namespace: "default",
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
				Namespace:  "default",
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
			Expect(string(resp.AdmissionResponse.Result.Reason)).To(Equal("cannot delete ByoHost when MachineRef is assigned"))
		})
	})
})

func TestByoHostValidator_handleCreateUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	require.NoError(t, err)
	decoder, err := admission.NewDecoder(scheme)
	require.NoError(t, err)

	v := &ByoHostValidator{decoder: decoder}

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
			userName:  "unauthorized-user",
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
			userName:  "byoh:host:host2",
			wantAllow: false,
			wantMsg:   "byoh:host:host2 cannot create/update resource host1",
		},
		{
			name:      "agent encoding the target host is allowed",
			userName:  "byoh:host:host1",
			wantAllow: true,
		},
		{
			name:      "ownership check matches by substring containment, not exact equality",
			userName:  "byoh:host:host1",
			hostName:  "host12",
			wantAllow: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hostName := tc.hostName
			if hostName == "" {
				hostName = "host1"
			}
			byoHost := &ByoHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostName,
					Namespace: "default",
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

			require.Equal(t, tc.wantAllow, resp.AdmissionResponse.Allowed)
			if !tc.wantAllow {
				require.Equal(t, tc.wantMsg, string(resp.AdmissionResponse.Result.Reason))
			}
		})
	}
}
