// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// The validation helpers under test are unexported, so these tests live in
// the package itself rather than in the external test package.
package controllers //nolint: testpackage // exercises unexported validation helpers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// validCSR returns a request that passes every check, so a test can spoil
// exactly one field and know that field is what the denial is about.
func validCSR(name string) *certv1.CertificateSigningRequest {
	expiration := maxCSRExpirationSeconds
	return &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: certv1.CertificateSigningRequestSpec{
			Groups:            []string{"system:authenticated", infrastructurev1beta1.BootstrapTokenExtraGroups},
			SignerName:        certv1.KubeAPIServerClientSignerName,
			Usages:            []certv1.KeyUsage{certv1.UsageClientAuth},
			ExpirationSeconds: &expiration,
		},
	}
}

func TestValidateByohCSR(t *testing.T) {
	testCases := []struct {
		name       string
		spoilFn    func(csr *certv1.CertificateSigningRequest)
		wantReason string
	}{
		{
			name:    "every check passes",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {},
		},
		{
			name: "requester is not a byoh bootstrapper",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"system:authenticated", "byoh:hosts"}
			},
			wantReason: reasonRequesterNotBootstrapper,
		},
		{
			name: "bootstrap group is the only group",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{infrastructurev1beta1.BootstrapTokenExtraGroups}
			},
			wantReason: "",
		},
		{
			name: "no groups at all",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = nil
			},
			wantReason: reasonRequesterNotBootstrapper,
		},
		{
			name: "bootstrap group only",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"system:authenticated", infrastructurev1beta1.BootstrapTokenExtraGroups}
			},
			wantReason: "",
		},
		{
			name: "signer is not the kube apiserver client signer",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.SignerName = certv1.KubeletServingSignerName
			},
			wantReason: reasonUnexpectedSignerName,
		},
		{
			name: "usages has more than client auth",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth}
			},
			wantReason: reasonUnexpectedUsages,
		},
		{
			name: "usages does not have client auth",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageServerAuth}
			},
			wantReason: reasonUnexpectedUsages,
		},
		{
			name: "usages list client auth twice",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageClientAuth}
			},
			wantReason: reasonUnexpectedUsages,
		},
		{
			name: "usages has client auth only",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth}
			},
			wantReason: "",
		},
		{
			name: "no usages at all",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = nil
			},
			wantReason: reasonUnexpectedUsages,
		},
		{
			name: "no lifetime requested",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = nil
			},
			wantReason: "",
		},
		{
			name: "requested lifetime is shorter than the ceiling",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = new(int32(3600))
			},
			wantReason: "",
		},
		{
			name: "requested lifetime is exactly at the ceiling",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = new(maxCSRExpirationSeconds)
			},
			wantReason: "",
		},
		{
			name: "requested lifetime is over the ceiling",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = new(maxCSRExpirationSeconds + 1)
			},
			wantReason: reasonExpirationTooLong,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR("byoh-csr-my-host-abcde")
			tc.spoilFn(csr)

			denial := validateByohCSR(csr)
			if tc.wantReason == "" {
				assert.Nil(t, denial)
				return
			}
			require.NotNil(t, denial)
			assert.Equal(t, tc.wantReason, denial.reason)
			assert.NotEmpty(t, denial.message)
		})
	}
}

func TestByoAdmissionReconciler_Reconcile(t *testing.T) {
	testCases := []struct {
		name    string
		spoilFn func(csr *certv1.CertificateSigningRequest)
		want    *csrDenial
	}{
		{
			name:    "a valid byoh request is approved",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {},
			want:    nil,
		},
		{
			name: "a request from outside the bootstrap group is denied",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"byoh:hosts"}
			},
			want: &csrDenial{reason: reasonRequesterNotBootstrapper},
		},
		{
			name: "a request for a foreign signer is denied",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.SignerName = certv1.KubeletServingSignerName
			},
			want: &csrDenial{reason: reasonUnexpectedSignerName},
		},
		{
			name: "a request asking for extra usages is denied",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth}
			},
			want: &csrDenial{reason: reasonUnexpectedUsages},
		},
		{
			name: "a request asking for too long a lifetime is denied",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = new(maxCSRExpirationSeconds + 1)
			},
			want: &csrDenial{reason: reasonExpirationTooLong},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			name := "byoh-csr-my-host-abcde"
			csr := validCSR(name)
			tc.spoilFn(csr)

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{ClientSet: clientSet}

			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name},
			})
			require.NoError(t, err)

			wantType := certv1.CertificateApproved
			wantReason := approvedReason
			if tc.want != nil {
				wantType = certv1.CertificateDenied
				wantReason = tc.want.reason
			}

			csrClient := clientSet.CertificatesV1().CertificateSigningRequests()
			updated, err := csrClient.Get(t.Context(), name, metav1.GetOptions{})
			require.NoError(t, err)

			require.Len(t, updated.Status.Conditions, 1)
			condition := updated.Status.Conditions[0]
			assert.Equal(t, wantType, condition.Type)
			assert.Equal(t, wantReason, condition.Reason)
			assert.Equal(t, corev1.ConditionTrue, condition.Status)
		})
	}
}

func TestByoAdmissionReconciler_ReconcileSkipsCSRItShouldNotTouch(t *testing.T) {
	settledCondition := func(conditionType certv1.RequestConditionType) []certv1.CertificateSigningRequestCondition {
		return []certv1.CertificateSigningRequestCondition{
			{
				Type:   conditionType,
				Status: corev1.ConditionTrue,
				Reason: "SetByAnotherActor",
			},
		}
	}

	testCases := []struct {
		name    string
		csrName string
		spoilFn func(csr *certv1.CertificateSigningRequest)
	}{
		{
			name:    "csr name does not belong to byoh",
			csrName: "some-other-csr",
		},
		{
			name:    "csr already approved",
			csrName: "byoh-csr-my-host-abcde",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				// Deliberately drop the requester from the bootstrap group, which should deny the
				// CSR. But since this CSR was already reconciled successfully, we expect the
				// reconciler to skip this.
				csr.Spec.Groups = []string{"byoh:hosts"}
				csr.Status.Conditions = settledCondition(certv1.CertificateApproved)
			},
		},
		{
			name:    "csr already denied",
			csrName: "byoh-csr-my-host-abcde",
			spoilFn: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"byoh:hosts"}
				csr.Status.Conditions = settledCondition(certv1.CertificateDenied)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR(tc.csrName)
			tc.spoilFn(csr)
			wantConditions := csr.Status.Conditions

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{ClientSet: clientSet}

			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tc.csrName},
			})
			require.NoError(t, err)

			actions := clientSet.Actions()
			require.Len(t, actions, 1)
			assert.Equal(t, "get", actions[0].GetVerb())

			updated, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), tc.csrName, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, wantConditions, updated.Status.Conditions)
		})
	}
}
