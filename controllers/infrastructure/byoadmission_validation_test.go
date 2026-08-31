// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// The validation helpers under test are unexported, so these tests live in
// the package itself rather than in the external test package.
package controllers //nolint: testpackage // exercises unexported validation helpers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// testTokenID is the bootstrap token the enrolled host in these tests
	// authenticates with.
	testTokenID = "abcdef"

	// testHostName is the enrolled host: the ByoHostEnrollment's name, and the
	// suffix of the common name that host is allowed to ask for.
	testHostName = "my-host"
)

// certificateRequestPEM builds a real PEM certificate signing request for
// commonName. The approver parses spec.request, so a hand-rolled blob would
// only exercise the parse failure.
func certificateRequestPEM(t *testing.T, commonName string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"byoh:hosts"},
		},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{
		Type:  certificateRequestPEMType,
		Bytes: der,
	})
}

// validCSR returns a request that passes every check, so a test can spoil
// exactly one field and know that field is what the denial is about.
func validCSR(t *testing.T, name string) *certv1.CertificateSigningRequest {
	t.Helper()

	expiration := maxCSRExpirationSeconds
	return &certv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: certv1.CertificateSigningRequestSpec{
			Username:          bootstrapUsernamePrefix + testTokenID,
			Groups:            []string{"system:authenticated", infrastructurev1beta1.BootstrapTokenExtraGroups},
			SignerName:        certv1.KubeAPIServerClientSignerName,
			Usages:            []certv1.KeyUsage{certv1.UsageClientAuth},
			ExpirationSeconds: &expiration,
			Request:           certificateRequestPEM(t, infrastructurev1beta1.HostCommonNamePrefix+testHostName),
		},
	}
}

// enrollment returns a ByoHostEnrollment for testHostName holding tokenID.
func enrollment(name, tokenID string) *infrastructurev1beta1.ByoHostEnrollment {
	return &infrastructurev1beta1.ByoHostEnrollment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testEnrollmentNamespace,
		},
		Status: infrastructurev1beta1.ByoHostEnrollmentStatus{
			TokenID: tokenID,
		},
	}
}

// enrollmentClient returns a client carrying the same token ID index the
// manager registers, so the approver's lookup is exercised rather than
// stubbed.
func enrollmentClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	testScheme := runtime.NewScheme()
	err := infrastructurev1beta1.AddToScheme(testScheme)
	require.NoError(t, err)

	return fake.NewClientBuilder().
		WithScheme(testScheme).
		WithIndex(&infrastructurev1beta1.ByoHostEnrollment{}, EnrollmentTokenIDIndex, enrollmentTokenIDIndexer).
		WithObjects(objects...).
		Build()
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
			csr := validCSR(t, "byoh-csr-my-host-abcde")
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
			csr := validCSR(t, name)
			tc.spoilFn(csr)

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{
				ClientSet: clientSet,
				Client:    enrollmentClient(t, enrollment(testHostName, testTokenID)),
			}

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

			updated, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), name, metav1.GetOptions{})
			require.NoError(t, err)

			require.Len(t, updated.Status.Conditions, 1)
			condition := updated.Status.Conditions[0]
			assert.Equal(t, wantType, condition.Type)
			assert.Equal(t, wantReason, condition.Reason)
			assert.Equal(t, corev1.ConditionTrue, condition.Status)
		})
	}
}

// csrCondition returns the condition of the given type on the named request,
// or nil when the request does not carry one.
func csrCondition(t *testing.T, clientSet *clientsetfake.Clientset, name string, conditionType certv1.RequestConditionType) *certv1.CertificateSigningRequestCondition {
	t.Helper()

	csr, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	for i := range csr.Status.Conditions {
		if csr.Status.Conditions[i].Type == conditionType {
			return &csr.Status.Conditions[i]
		}
	}
	return nil
}

// A request can pass every generic check and still be a request for somebody
// else's identity, which is what these cases cover.
func TestReconcileBindsRequestsToTheirEnrollment(t *testing.T) {
	const csrName = "byoh-csr-my-host-abcde"

	testCases := []struct {
		name       string
		spoil      func(csr *certv1.CertificateSigningRequest)
		wantType   certv1.RequestConditionType
		wantReason string
	}{
		{
			name: "a request naming another host is denied",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Request = certificateRequestPEM(t, infrastructurev1beta1.HostCommonNamePrefix+"someone-else")
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonCommonNameNotPermitted,
		},
		{
			name: "a request whose token has no enrollment is denied",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Username = bootstrapUsernamePrefix + "fedcba"
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonUnknownBootstrapToken,
		},
		{
			name: "a request from a non-bootstrap identity is denied",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Username = "system:serviceaccount:kube-system:byoh"
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonMalformedUsername,
		},
		{
			name: "a request carrying an unparseable certificate request is denied",
			spoil: func(csr *certv1.CertificateSigningRequest) {
				csr.Spec.Request = []byte("not pem at all")
			},
			wantType:   certv1.CertificateDenied,
			wantReason: reasonMalformedRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR(t, csrName)
			tc.spoil(csr)

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{
				ClientSet: clientSet,
				Client:    enrollmentClient(t, enrollment(testHostName, testTokenID)),
			}

			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: csrName},
			})
			require.NoError(t, err)

			condition := csrCondition(t, clientSet, csrName, tc.wantType)
			require.NotNil(t, condition)
			assert.Equal(t, tc.wantReason, condition.Reason)
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
			csr := validCSR(t, tc.csrName)
			if tc.spoilFn != nil {
				tc.spoilFn(csr)
			}
			wantConditions := csr.Status.Conditions

			clientSet := clientsetfake.NewSimpleClientset(csr)
			reconciler := &ByoAdmissionReconciler{
				ClientSet: clientSet,
				Client:    enrollmentClient(t, enrollment(testHostName, testTokenID)),
			}

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

func TestBootstrapTokenID(t *testing.T) {
	testCases := []struct {
		name     string
		username string
		want     string
		wantOK   bool
	}{
		{
			name:     "a bootstrap token identity",
			username: "system:bootstrap:abcdef",
			want:     "abcdef",
			wantOK:   true,
		},
		{
			name:     "a service account identity",
			username: "system:serviceaccount:kube-system:byoh",
		},
		{
			name:     "the prefix with no token ID",
			username: "system:bootstrap:",
		},
		{
			name:     "an empty username",
			username: "",
		},
		{
			name:     "the prefix in the middle rather than at the start",
			username: "attacker:system:bootstrap:abcdef",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bootstrapTokenID(tc.username)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequestedCommonName(t *testing.T) {
	valid := certificateRequestPEM(t, "byoh:host:web-01")

	// A PEM block of the right shape holding something that is not a
	// certificate request, so the parse fails after the PEM decode succeeds.
	notARequest := pem.EncodeToMemory(&pem.Block{
		Type:  certificateRequestPEMType,
		Bytes: []byte("not DER"),
	})

	wrongBlockType := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("not DER"),
	})

	testCases := []struct {
		name    string
		request []byte
		want    string
		wantErr bool
	}{
		{
			name:    "a well formed request",
			request: valid,
			want:    "byoh:host:web-01",
		},
		{
			name:    "not PEM",
			request: []byte("hello"),
			wantErr: true,
		},
		{
			name:    "no request at all",
			request: nil,
			wantErr: true,
		},
		{
			name:    "a PEM block of the wrong type",
			request: wrongBlockType,
			wantErr: true,
		},
		{
			name:    "PEM that does not hold a certificate request",
			request: notARequest,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requestedCommonName(tc.request)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEnrollmentTokenIDIndexer(t *testing.T) {
	byoHost := &infrastructurev1beta1.ByoHost{
		ObjectMeta: metav1.ObjectMeta{Name: testHostName},
	}

	testCases := []struct {
		name   string
		object client.Object
		want   []string
	}{
		{
			name:   "an enrollment holding a token",
			object: enrollment(testHostName, testTokenID),
			want:   []string{testTokenID},
		},
		{
			name:   "an enrollment that has not minted a token yet",
			object: enrollment(testHostName, ""),
		},
		{
			name:   "an object of another kind",
			object: byoHost,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := enrollmentTokenIDIndexer(tc.object)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateEnrolledHost(t *testing.T) {
	testCases := []struct {
		name        string
		enrollments []client.Object
		spoil       func(t *testing.T, csr *certv1.CertificateSigningRequest)
		wantReason  string
	}{
		{
			name:        "the common name matches the enrollment that minted the token",
			enrollments: []client.Object{enrollment(testHostName, testTokenID)},
		},
		{
			name:        "the common name names a different host",
			enrollments: []client.Object{enrollment(testHostName, testTokenID)},
			spoil: func(t *testing.T, csr *certv1.CertificateSigningRequest) {
				t.Helper()
				csr.Spec.Request = certificateRequestPEM(t, infrastructurev1beta1.HostCommonNamePrefix+"someone-else")
			},
			wantReason: reasonCommonNameNotPermitted,
		},
		{
			name:        "the common name carries no byoh prefix",
			enrollments: []client.Object{enrollment(testHostName, testTokenID)},
			spoil: func(t *testing.T, csr *certv1.CertificateSigningRequest) {
				t.Helper()
				csr.Spec.Request = certificateRequestPEM(t, testHostName)
			},
			wantReason: reasonCommonNameNotPermitted,
		},
		{
			name:        "no enrollment holds the token",
			enrollments: nil,
			wantReason:  reasonUnknownBootstrapToken,
		},
		{
			name:        "the token belongs to another host's enrollment",
			enrollments: []client.Object{enrollment("someone-else", testTokenID)},
			wantReason:  reasonCommonNameNotPermitted,
		},
		{
			name: "two enrollments hold the same token",
			enrollments: []client.Object{
				enrollment(testHostName, testTokenID),
				enrollment("someone-else", testTokenID),
			},
			wantReason: reasonAmbiguousBootstrapToken,
		},
		{
			name:        "the requester is not a bootstrap token identity",
			enrollments: []client.Object{enrollment(testHostName, testTokenID)},
			spoil: func(t *testing.T, csr *certv1.CertificateSigningRequest) {
				t.Helper()
				csr.Spec.Username = "system:serviceaccount:kube-system:byoh"
			},
			wantReason: reasonMalformedUsername,
		},
		{
			name:        "the certificate request does not parse",
			enrollments: []client.Object{enrollment(testHostName, testTokenID)},
			spoil: func(t *testing.T, csr *certv1.CertificateSigningRequest) {
				t.Helper()
				csr.Spec.Request = []byte("not pem at all")
			},
			wantReason: reasonMalformedRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := validCSR(t, "byoh-csr-my-host-abcde")
			if tc.spoil != nil {
				tc.spoil(t, csr)
			}

			reconciler := &ByoAdmissionReconciler{Client: enrollmentClient(t, tc.enrollments...)}

			denial, err := reconciler.validateEnrolledHost(t.Context(), csr)
			require.NoError(t, err)
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
