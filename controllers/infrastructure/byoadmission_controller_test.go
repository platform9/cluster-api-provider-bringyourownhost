// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"crypto/rand"
	"crypto/rsa"
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
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/platform9/cluster-api-provider-bringyourownhost/test/builder"
)

const (
	admissionNamespace = "dg-d-default-service"
	admissionHostName  = "worker-1"
	admissionTokenID   = "abcdef"
	admissionCSRName   = "byoh-csr-worker-1"
	admissionIdentity  = "byoh:host:dg-d-default-service:worker-1-a1b2c"
	admissionUsername  = "system:bootstrap:abcdef"
)

// admissionIssuer returns a BootstrapKubeconfig that issued token tokenID for
// the host admissionHostName.
func admissionIssuer(name, namespace, tokenID string) *infrastructurev1beta1.BootstrapKubeconfig {
	return &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       infrastructurev1beta1.BootstrapKubeconfigSpec{HostName: admissionHostName},
		Status:     infrastructurev1beta1.BootstrapKubeconfigStatus{TokenID: tokenID},
	}
}

// admissionCSR builds a CSR of a valid host request, with the requester
// username and common name given.
func admissionCSR(t *testing.T, username, cn string) *certv1.CertificateSigningRequest {
	t.Helper()

	csr, err := builder.CertificateSigningRequest(admissionCSRName, cn, "byoh:hosts", 2048).WithUsername(username).Build()
	require.NoError(t, err)

	return csr
}

// certificateRequestPEM returns a PEM certificate request with the given common
// name and organizations.
func certificateRequestPEM(t *testing.T, cn string, orgs []string) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn, Organization: orgs},
	}, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestByoAdmissionReconciler_Reconcile(t *testing.T) {
	approved := certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Status: corev1.ConditionTrue,
		Reason: "Approved by ByoAdmission Controller",
	}
	denied := func(message string) certv1.CertificateSigningRequestCondition {
		return certv1.CertificateSigningRequestCondition{
			Type:    certv1.CertificateDenied,
			Status:  corev1.ConditionTrue,
			Reason:  "DeniedByByoAdmissionController",
			Message: message,
		}
	}

	testCases := []struct {
		name           string
		csr            func(t *testing.T) *certv1.CertificateSigningRequest
		issuers        []client.Object
		wantConditions []certv1.CertificateSigningRequestCondition
		wantErr        bool
	}{
		{
			name: "matching request",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, admissionIdentity)
			},
			issuers:        []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{approved},
			wantErr:        false,
		},
		{
			name: "other host",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "byoh:host:dg-d-default-service:worker-10-a1b2c")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`token "abcdef" was issued for host "worker-1", but the request asks for host "worker-10"`),
			},
			wantErr: false,
		},
		{
			name: "other tenant",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "byoh:host:dg-d-r1:worker-1-a1b2c")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`token "abcdef" was issued in namespace "dg-d-default-service", but the request asks for tenant "dg-d-r1"`),
			},
			wantErr: false,
		},
		{
			name: "token matches no object",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, admissionIdentity)
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, "zzzzzz")},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`exactly one BootstrapKubeconfig must have token id "abcdef", found 0`),
			},
			wantErr: false,
		},
		{
			name: "token on two objects",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, admissionIdentity)
			},
			issuers: []client.Object{
				admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID),
				admissionIssuer("worker-1-d3e4f", "dg-d-r1", admissionTokenID),
			},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`exactly one BootstrapKubeconfig must have token id "abcdef", found 2`),
			},
			wantErr: false,
		},
		{
			name: "requester is not a bootstrap user",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, "alice", admissionIdentity)
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`requester "alice" did not authenticate with a bootstrap token`),
			},
			wantErr: false,
		},
		{
			name: "bootstrap user with an empty token id",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, "system:bootstrap:", admissionIdentity)
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, "")},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`requester "system:bootstrap:" did not authenticate with a bootstrap token`),
			},
			wantErr: false,
		},
		{
			name: "organization is system:masters",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.Request = certificateRequestPEM(t, admissionIdentity, []string{"system:masters"})
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`organization must be exactly [byoh:hosts], got [system:masters]`),
			},
			wantErr: false,
		},
		{
			name: "two organizations",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.Request = certificateRequestPEM(t, admissionIdentity, []string{"byoh:hosts", "system:masters"})
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`organization must be exactly [byoh:hosts], got [byoh:hosts system:masters]`),
			},
			wantErr: false,
		},
		{
			name: "common name is an email address",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "user@example.com")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`common name is not a host identity: identity "user@example.com" is invalid: does not start with "byoh:host:"`),
			},
			wantErr: false,
		},
		{
			name: "common name is a Dex identity",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "du:user@example.com")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`common name is not a host identity: identity "du:user@example.com" is invalid: does not start with "byoh:host:"`),
			},
			wantErr: false,
		},
		{
			name: "common name without the host prefix",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "dg-d-default-service:worker-1-a1b2c")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`common name is not a host identity: identity "dg-d-default-service:worker-1-a1b2c" is invalid: does not start with "byoh:host:"`),
			},
			wantErr: false,
		},
		{
			name: "common name with a bad suffix",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, "byoh:host:dg-d-default-service:worker-1-A1B2C")
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`common name is not a host identity: identity "byoh:host:dg-d-default-service:worker-1-A1B2C" is invalid: has an invalid suffix "A1B2C": want 5 characters in [a-z0-9]; has an invalid name "worker-1-A1B2C": a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`),
			},
			wantErr: false,
		},
		{
			name: "other signer",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.SignerName = certv1.KubeletServingSignerName
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`signer must be kubernetes.io/kube-apiserver-client, got "kubernetes.io/kubelet-serving"`),
			},
			wantErr: false,
		},
		{
			name: "extra usage",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.Usages = []certv1.KeyUsage{certv1.UsageClientAuth, certv1.UsageServerAuth}
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`usages must be exactly [client auth], got [client auth server auth]`),
			},
			wantErr: false,
		},
		{
			name: "request is not PEM",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.Request = []byte("garbage")
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`certificate request is not PEM encoded`),
			},
			wantErr: false,
		},
		{
			name: "already approved with a request that would be denied",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, "alice", "user@example.com")
				csr.Status.Conditions = []certv1.CertificateSigningRequestCondition{approved}
				return csr
			},
			wantConditions: []certv1.CertificateSigningRequestCondition{approved},
			wantErr:        false,
		},
		{
			name: "already denied with a request that would be approved",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Status.Conditions = []certv1.CertificateSigningRequestCondition{{Type: certv1.CertificateDenied}}
				return csr
			},
			issuers:        []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{{Type: certv1.CertificateDenied}},
			wantErr:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, infrastructurev1beta1.AddToScheme(scheme))

			cl := ctrlfake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.issuers...).
				WithIndex(&infrastructurev1beta1.BootstrapKubeconfig{}, controllers.BootstrapKubeconfigTokenIDIndex, controllers.IndexBootstrapKubeconfigByTokenID).
				Build()
			clientSet := fake.NewSimpleClientset(tc.csr(t))
			reconciler := &controllers.ByoAdmissionReconciler{ClientSet: clientSet, Client: cl}

			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: admissionCSRName}})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), admissionCSRName, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, tc.wantConditions, got.Status.Conditions)
		})
	}
}

func TestByoAdmissionReconciler_Reconcile_MissingCSR(t *testing.T) {
	reconciler := &controllers.ByoAdmissionReconciler{ClientSet: fake.NewSimpleClientset()}

	result, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: admissionCSRName}})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestIndexBootstrapKubeconfigByTokenID(t *testing.T) {
	testCases := []struct {
		name string
		obj  client.Object
		want []string
	}{
		{
			name: "object with a token",
			obj:  admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID),
			want: []string{admissionTokenID},
		},
		{
			name: "object without a token yet",
			obj:  admissionIssuer("worker-1-a1b2c", admissionNamespace, ""),
			want: nil,
		},
		{
			name: "object of another kind",
			obj:  &corev1.Secret{},
			want: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := controllers.IndexBootstrapKubeconfigByTokenID(tc.obj)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestByoCSRPredicate(t *testing.T) {
	testCases := []struct {
		name    string
		csrName string
		want    bool
	}{
		{
			name:    "name with the byoh-csr- prefix",
			csrName: "byoh-csr-worker-1",
			want:    true,
		},
		{
			name:    "name without the byoh-csr- prefix",
			csrName: "csr-worker-1",
			want:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := &certv1.CertificateSigningRequest{ObjectMeta: metav1.ObjectMeta{Name: tc.csrName}}
			predicate := controllers.ByoCSRPredicate()

			gotCreate := predicate.Create(event.CreateEvent{Object: csr})
			gotUpdate := predicate.Update(event.UpdateEvent{ObjectOld: csr, ObjectNew: csr})

			assert.Equal(t, tc.want, gotCreate)
			assert.Equal(t, tc.want, gotUpdate)
		})
	}
}
