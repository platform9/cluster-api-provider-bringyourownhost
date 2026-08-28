// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// The validation helpers under test are unexported, so these tests live in
// the package itself rather than in the external test package.
package controllers //nolint: testpackage // exercises unexported validation helpers

import (
	"context"
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

func TestHasGroup(t *testing.T) {
	testCases := []struct {
		name   string
		groups []string
		want   bool
	}{
		{
			name:   "group is present among others",
			groups: []string{"system:authenticated", infrastructurev1beta1.BootstrapTokenExtraGroups},
			want:   true,
		},
		{
			name:   "group is the only one",
			groups: []string{infrastructurev1beta1.BootstrapTokenExtraGroups},
			want:   true,
		},
		{
			name:   "group is absent",
			groups: []string{"system:authenticated", "byoh:hosts"},
			want:   false,
		},
		{
			name:   "no groups at all",
			groups: nil,
			want:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasGroup(tc.groups, infrastructurev1beta1.BootstrapTokenExtraGroups)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateUsages(t *testing.T) {
	testCases := []struct {
		name   string
		usages []certv1.KeyUsage
		want   bool
	}{
		{
			name:   "client auth only",
			usages: []certv1.KeyUsage{certv1.UsageClientAuth},
			want:   true,
		},
		{
			name:   "client auth plus an extra usage",
			usages: []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth},
			want:   false,
		},
		{
			name:   "a different usage",
			usages: []certv1.KeyUsage{certv1.UsageServerAuth},
			want:   false,
		},
		{
			name:   "client auth listed twice",
			usages: []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageClientAuth},
			want:   false,
		},
		{
			name:   "no usages",
			usages: nil,
			want:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			denial := validateUsages(tc.usages)
			if tc.want {
				assert.Nil(t, denial)
				return
			}
			require.NotNil(t, denial)
			assert.Equal(t, reasonUnexpectedUsages, denial.reason)
			assert.NotEmpty(t, denial.message)
		})
	}
}

func TestValidateExpirationSeconds(t *testing.T) {
	overCeiling := maxCSRExpirationSeconds + 1
	atCeiling := maxCSRExpirationSeconds
	shortLived := int32(3600)

	testCases := []struct {
		name       string
		expiration *int32
		want       bool
	}{
		{
			name:       "no lifetime requested",
			expiration: nil,
			want:       true,
		},
		{
			name:       "shorter than the ceiling",
			expiration: &shortLived,
			want:       true,
		},
		{
			name:       "exactly the ceiling",
			expiration: &atCeiling,
			want:       true,
		},
		{
			name:       "one second over the ceiling",
			expiration: &overCeiling,
			want:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			denial := validateExpirationSeconds(tc.expiration)
			if tc.want {
				assert.Nil(t, denial)
				return
			}
			require.NotNil(t, denial)
			assert.Equal(t, reasonExpirationTooLong, denial.reason)
			assert.NotEmpty(t, denial.message)
		})
	}
}

func TestValidateByohCSR(t *testing.T) {
	overCeiling := maxCSRExpirationSeconds + 1

	testCases := []struct {
		name       string
		spoil      func(csr *certv1.CertificateSigningRequest)
		wantReason string
	}{
		{
			name:  "every check passes",
			spoil: func(csr *certv1.CertificateSigningRequest) {},
		},
		{
			name: "requester is not a byoh bootstrapper",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"system:authenticated", "byoh:hosts"}
			},
			wantReason: reasonRequesterNotBootstrapper,
		},
		{
			name: "signer is not the kube apiserver client signer",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.SignerName = certv1.KubeletServingSignerName
			},
			wantReason: reasonUnexpectedSignerName,
		},
		{
			name: "usages carry more than client auth",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth}
			},
			wantReason: reasonUnexpectedUsages,
		},
		{
			name: "requested lifetime is over the ceiling",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = &overCeiling
			},
			wantReason: reasonExpirationTooLong,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR("byoh-csr-my-host-abcde")
			tc.spoil(csr)

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

// csrCondition returns the condition of the given type on the named request,
// or nil when the request does not carry one.
func csrCondition(t *testing.T, clientSet *clientsetfake.Clientset, name string, conditionType certv1.RequestConditionType) *certv1.CertificateSigningRequestCondition {
	t.Helper()

	csr, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	for i := range csr.Status.Conditions {
		if csr.Status.Conditions[i].Type == conditionType {
			return &csr.Status.Conditions[i]
		}
	}
	return nil
}

func TestReconcileApprovesAndDenies(t *testing.T) {
	overCeiling := maxCSRExpirationSeconds + 1

	testCases := []struct {
		name       string
		csrName    string
		spoil      func(csr *certv1.CertificateSigningRequest)
		wantType   certv1.RequestConditionType
		wantReason string
	}{
		{
			name:       "a valid byoh request is approved",
			csrName:    "byoh-csr-my-host-abcde",
			spoil:      func(csr *certv1.CertificateSigningRequest) {},
			wantType:   certv1.CertificateApproved,
			wantReason: approvedReason,
		},
		{
			name:    "a request from outside the bootstrap group is denied",
			csrName: "byoh-csr-my-host-abcde",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Groups = []string{"byoh:hosts"}
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonRequesterNotBootstrapper,
		},
		{
			name:    "a request for a foreign signer is denied",
			csrName: "byoh-csr-my-host-abcde",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.SignerName = certv1.KubeletServingSignerName
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonUnexpectedSignerName,
		},
		{
			name:    "a request asking for extra usages is denied",
			csrName: "byoh-csr-my-host-abcde",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth}
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonUnexpectedUsages,
		},
		{
			name:    "a request asking for too long a lifetime is denied",
			csrName: "byoh-csr-my-host-abcde",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.ExpirationSeconds = &overCeiling
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonExpirationTooLong,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR(tc.csrName)
			tc.spoil(csr)

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{ClientSet: clientSet}

			_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: tc.csrName},
			})
			require.NoError(t, err)

			condition := csrCondition(t, clientSet, tc.csrName, tc.wantType)
			require.NotNil(t, condition)
			assert.Equal(t, tc.wantReason, condition.Reason)
			assert.Equal(t, corev1.ConditionTrue, condition.Status)
		})
	}
}

func TestReconcileLeavesForeignCSRAlone(t *testing.T) {
	const name = "some-other-csr"

	// The request would pass every validation. Only its name says it is not
	// ours, so the approver has to leave it untouched.
	csr := validCSR(name)
	clientSet := clientsetfake.NewSimpleClientset(csr)
	reconciler := &ByoAdmissionReconciler{ClientSet: clientSet}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
	require.NoError(t, err)

	approved := csrCondition(t, clientSet, name, certv1.CertificateApproved)
	assert.Nil(t, approved)
	denied := csrCondition(t, clientSet, name, certv1.CertificateDenied)
	assert.Nil(t, denied)
}

func TestReconcileSkipsSettledCSR(t *testing.T) {
	const name = "byoh-csr-my-host-abcde"

	testCases := []struct {
		name      string
		condition certv1.RequestConditionType
	}{
		{
			name:      "already approved",
			condition: certv1.CertificateApproved,
		},
		{
			name:      "already denied",
			condition: certv1.CertificateDenied,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Spoiled so that a second pass would deny it, which is how the
			// test can tell the settled request was left alone.
			csr := validCSR(name)
			csr.Spec.Groups = []string{"byoh:hosts"}
			csr.Status.Conditions = []certv1.CertificateSigningRequestCondition{
				{
					Type:   tc.condition,
					Status: corev1.ConditionTrue,
					Reason: "SetByAnotherActor",
				},
			}

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{ClientSet: clientSet}

			_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name},
			})
			require.NoError(t, err)

			updated, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(context.Background(), name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Len(t, updated.Status.Conditions, 1)
			assert.Equal(t, "SetByAnotherActor", updated.Status.Conditions[0].Reason)
		})
	}
}
