// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	certcsr "k8s.io/client-go/util/certificate/csr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/certificate"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
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

	// admissionMaxLifetime is the cap the tests set on the approver, and the
	// lifetime admissionCSR requests.
	admissionMaxLifetime int32 = 3600
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
	csr.Spec.Request = certificateRequestPEM(t, cn, []string{"byoh:hosts", "byoh:hosts:dg-d-default-service"})
	csr.Spec.Groups = []string{"system:bootstrappers", "system:bootstrappers:byoh", "system:authenticated"}
	csr.Spec.ExpirationSeconds = ptr.To(admissionMaxLifetime)

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

// reconcileAdmission reconciles csr against a fake API server holding issuers,
// and returns the reconcile result and error with the conditions the CSR ends
// with. listFails makes every list of BootstrapKubeconfigs fail.
func reconcileAdmission(t *testing.T, csr *certv1.CertificateSigningRequest, issuers []client.Object, listFails bool) (ctrl.Result, []certv1.CertificateSigningRequestCondition, error) {
	t.Helper()

	funcs := interceptor.Funcs{}
	if listFails {
		funcs.List = func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			return errors.New("cache not started")
		}
	}
	cl := newAdmissionClient(t, &funcs, issuers...)
	clientSet := fake.NewSimpleClientset(csr)

	return reconcileAdmissionWith(t, clientSet, cl, csr.Name)
}

// newAdmissionClient returns a fake client that holds objs and indexes
// BootstrapKubeconfigs by token id, like the manager's cache does.
func newAdmissionClient(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, infrastructurev1beta1.AddToScheme(scheme))

	return ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&infrastructurev1beta1.BootstrapKubeconfig{}).
		WithIndex(&infrastructurev1beta1.BootstrapKubeconfig{}, controllers.BootstrapKubeconfigTokenIDIndex, controllers.IndexBootstrapKubeconfigByTokenID).
		WithInterceptorFuncs(*funcs).
		Build()
}

// reconcileAdmissionWith reconciles the CSR named csrName and returns the
// result and error with the conditions the CSR ends with.
func reconcileAdmissionWith(t *testing.T, clientSet *fake.Clientset, cl client.Client, csrName string) (ctrl.Result, []certv1.CertificateSigningRequestCondition, error) {
	t.Helper()

	reconciler := &controllers.ByoAdmissionReconciler{
		ClientSet:                     clientSet,
		Client:                        cl,
		APIReader:                     cl,
		MaxCertificateLifetimeSeconds: admissionMaxLifetime,
	}

	result, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: csrName}})

	got, getErr := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), csrName, metav1.GetOptions{})
	require.NoError(t, getErr)

	return result, got.Status.Conditions, err
}

func TestByoAdmissionReconciler_Reconcile_IssuerLookup(t *testing.T) {
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
		listFails      bool
		wantResult     ctrl.Result
		wantConditions []certv1.CertificateSigningRequestCondition
		wantErr        bool
	}{
		{
			name: "listing the BootstrapKubeconfigs fails",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				return admissionCSR(t, admissionUsername, admissionIdentity)
			},
			issuers:        []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			listFails:      true,
			wantConditions: nil,
			wantErr:        true,
		},
		{
			name: "token matches no object on a request created just now",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.CreationTimestamp = metav1.Now()
				return csr
			},
			issuers:        []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, "zzzzzz")},
			wantResult:     ctrl.Result{RequeueAfter: 5 * time.Second},
			wantConditions: nil,
			wantErr:        false,
		},
		{
			name: "token matches no object on a request older than the lookup window",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, "zzzzzz")},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`exactly one BootstrapKubeconfig must have token id "abcdef", found 0`),
			},
			wantErr: false,
		},
		{
			name: "token on two objects on a request created just now",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.CreationTimestamp = metav1.Now()
				return csr
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
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, conditions, err := reconcileAdmission(t, tc.csr(t), tc.issuers, tc.listFails)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantResult, result)
			assert.Equal(t, tc.wantConditions, conditions)
		})
	}
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
			name: "requester without the byoh bootstrap group",
			csr: func(t *testing.T) *certv1.CertificateSigningRequest {
				csr := admissionCSR(t, admissionUsername, admissionIdentity)
				csr.Spec.Groups = []string{"system:bootstrappers", "system:authenticated"}
				return csr
			},
			issuers: []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied(`requester groups must include system:bootstrappers:byoh, got [system:bootstrappers system:authenticated]`),
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
			_, conditions, err := reconcileAdmission(t, tc.csr(t), tc.issuers, false)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantConditions, conditions)
		})
	}
}

// TestByoAdmissionReconciler_Reconcile_Organization checks the organizations of
// the request against the tenant of the issuer.
func TestByoAdmissionReconciler_Reconcile_Organization(t *testing.T) {
	approved := certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Status: corev1.ConditionTrue,
		Reason: "Approved by ByoAdmission Controller",
	}
	denied := func(got string) certv1.CertificateSigningRequestCondition {
		return certv1.CertificateSigningRequestCondition{
			Type:    certv1.CertificateDenied,
			Status:  corev1.ConditionTrue,
			Reason:  "DeniedByByoAdmissionController",
			Message: "organization must be exactly [byoh:hosts byoh:hosts:dg-d-default-service] in any order, got " + got,
		}
	}

	testCases := []struct {
		name           string
		organization   []string
		wantConditions []certv1.CertificateSigningRequestCondition
	}{
		{
			name:           "hosts group and tenant group",
			organization:   []string{"byoh:hosts", "byoh:hosts:dg-d-default-service"},
			wantConditions: []certv1.CertificateSigningRequestCondition{approved},
		},
		{
			name:           "tenant group of another tenant",
			organization:   []string{"byoh:hosts", "byoh:hosts:dg-d-r1"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[byoh:hosts byoh:hosts:dg-d-r1]")},
		},
		{
			name:           "old organization only",
			organization:   []string{"byoh:hosts"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[byoh:hosts]")},
		},
		{
			name:           "empty organization",
			organization:   nil,
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[]")},
		},
		{
			name:           "extra organization",
			organization:   []string{"byoh:hosts", "byoh:hosts:dg-d-default-service", "extra"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[extra byoh:hosts byoh:hosts:dg-d-default-service]")},
		},
		{
			name:           "duplicate hosts group",
			organization:   []string{"byoh:hosts", "byoh:hosts"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[byoh:hosts byoh:hosts]")},
		},
		{
			name:           "tenant group only",
			organization:   []string{"byoh:hosts:dg-d-default-service"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[byoh:hosts:dg-d-default-service]")},
		},
		{
			name:           "system:masters",
			organization:   []string{"system:masters"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[system:masters]")},
		},
		{
			name:           "hosts group and system:masters",
			organization:   []string{"byoh:hosts", "system:masters"},
			wantConditions: []certv1.CertificateSigningRequestCondition{denied("[byoh:hosts system:masters]")},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := admissionCSR(t, admissionUsername, admissionIdentity)
			csr.Spec.Request = certificateRequestPEM(t, admissionIdentity, tc.organization)
			issuers := []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)}

			_, conditions, err := reconcileAdmission(t, csr, issuers, false)
			require.NoError(t, err)
			assert.Equal(t, tc.wantConditions, conditions)
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
		signer  string
		want    bool
	}{
		{
			name:    "byoh-csr- name and kube-apiserver-client signer",
			csrName: "byoh-csr-worker-1",
			signer:  certv1.KubeAPIServerClientSignerName,
			want:    true,
		},
		{
			name:    "byoh-csr- name and kubelet client signer",
			csrName: "byoh-csr-worker-1",
			signer:  certv1.KubeAPIServerClientKubeletSignerName,
			want:    false,
		},
		{
			name:    "other name and kube-apiserver-client signer",
			csrName: "csr-worker-1",
			signer:  certv1.KubeAPIServerClientSignerName,
			want:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := &certv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{Name: tc.csrName},
				Spec: certv1.CertificateSigningRequestSpec{
					SignerName: tc.signer,
				},
			}
			predicate := controllers.ByoCSRPredicate()

			gotCreate := predicate.Create(event.CreateEvent{Object: csr})
			gotUpdate := predicate.Update(event.UpdateEvent{ObjectOld: csr, ObjectNew: csr})

			assert.Equal(t, tc.want, gotCreate)
			assert.Equal(t, tc.want, gotUpdate)
		})
	}
}

// approvedIssuer returns an issuer whose status records an approved CSR.
func approvedIssuer(name, tokenID, csrName, commonName string) *infrastructurev1beta1.BootstrapKubeconfig {
	issuer := admissionIssuer(name, admissionNamespace, tokenID)
	issuer.Status.CSRApproval = infrastructurev1beta1.CSRApproved
	issuer.Status.ApprovedCSRName = csrName
	issuer.Status.ApprovedCommonName = commonName

	return issuer
}

func TestByoAdmissionReconciler_Reconcile_OneCSRPerToken(t *testing.T) {
	const (
		otherCSRName  = "byoh-csr-worker-1-second"
		newSuffixCN   = "byoh:host:dg-d-default-service:worker-1-xyz99"
		otherTokenID  = "fedcba"
		otherTokenUsr = "system:bootstrap:fedcba"
	)

	testCases := []struct {
		name         string
		csrName      string
		username     string
		commonName   string
		objects      []client.Object
		wantDenied   bool
		wantMessage  string
		wantApproved bool
		wantStatus   infrastructurev1beta1.BootstrapKubeconfigStatus
	}{
		{
			name:         "first CSR of a token",
			csrName:      admissionCSRName,
			username:     admissionUsername,
			commonName:   admissionIdentity,
			objects:      []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)},
			wantApproved: true,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:       "second CSR with the same common name",
			csrName:    otherCSRName,
			username:   admissionUsername,
			commonName: admissionIdentity,
			objects:    []client.Object{approvedIssuer("worker-1-a1b2c", admissionTokenID, admissionCSRName, admissionIdentity)},
			wantDenied: true,
			wantMessage: `token "abcdef" already has approved CSR "byoh-csr-worker-1", ` +
				`so CSR "byoh-csr-worker-1-second" is denied`,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:       "second CSR with a new suffix",
			csrName:    otherCSRName,
			username:   admissionUsername,
			commonName: newSuffixCN,
			objects:    []client.Object{approvedIssuer("worker-1-a1b2c", admissionTokenID, admissionCSRName, admissionIdentity)},
			wantDenied: true,
			wantMessage: `token "abcdef" already has approved CSR "byoh-csr-worker-1", ` +
				`so CSR "byoh-csr-worker-1-second" is denied`,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:         "same CSR again after its status was written",
			csrName:      admissionCSRName,
			username:     admissionUsername,
			commonName:   admissionIdentity,
			objects:      []client.Object{approvedIssuer("worker-1-a1b2c", admissionTokenID, admissionCSRName, admissionIdentity)},
			wantApproved: true,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:       "CSR name recorded while the state is NotApproved",
			csrName:    otherCSRName,
			username:   admissionUsername,
			commonName: admissionIdentity,
			objects: []client.Object{
				func() client.Object {
					issuer := approvedIssuer("worker-1-a1b2c", admissionTokenID, admissionCSRName, admissionIdentity)
					issuer.Status.CSRApproval = infrastructurev1beta1.CSRNotApproved
					return issuer
				}(),
			},
			wantApproved: true,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    otherCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:       "common name held in another tenant",
			csrName:    admissionCSRName,
			username:   admissionUsername,
			commonName: admissionIdentity,
			objects: []client.Object{
				admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID),
				func() client.Object {
					other := approvedIssuer("worker-1-other", otherTokenID, otherCSRName, admissionIdentity)
					other.Namespace = "dg-d-r1"
					return other
				}(),
				&infrastructurev1beta1.ByoHost{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-1-a1b2c", Namespace: "dg-d-r1"},
					Spec:       infrastructurev1beta1.ByoHostSpec{Identity: admissionIdentity},
				},
			},
			wantApproved: true,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: admissionIdentity,
			},
		},
		{
			name:       "common name held by another token in the tenant",
			csrName:    admissionCSRName,
			username:   otherTokenUsr,
			commonName: admissionIdentity,
			objects: []client.Object{
				admissionIssuer("worker-1-other", admissionNamespace, otherTokenID),
				approvedIssuer("worker-1-a1b2c", admissionTokenID, otherCSRName, admissionIdentity),
			},
			wantDenied:  true,
			wantMessage: `common name "` + admissionIdentity + `" is already held by BootstrapKubeconfig "worker-1-a1b2c"`,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID: otherTokenID,
			},
		},
		{
			name:       "common name held by a ByoHost in the tenant",
			csrName:    admissionCSRName,
			username:   admissionUsername,
			commonName: admissionIdentity,
			objects: []client.Object{
				admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID),
				&infrastructurev1beta1.ByoHost{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-1-a1b2c", Namespace: admissionNamespace},
					Spec:       infrastructurev1beta1.ByoHostSpec{Identity: admissionIdentity},
				},
			},
			wantDenied:  true,
			wantMessage: `common name "` + admissionIdentity + `" is already held by ByoHost "worker-1-a1b2c"`,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID: admissionTokenID,
			},
		},
		{
			name:       "new suffix for a host that has no ByoHost and no approved CSR",
			csrName:    admissionCSRName,
			username:   admissionUsername,
			commonName: newSuffixCN,
			objects: []client.Object{
				admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID),
				&infrastructurev1beta1.ByoHost{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-1-a1b2c", Namespace: admissionNamespace},
					Spec:       infrastructurev1beta1.ByoHostSpec{Identity: admissionIdentity},
				},
			},
			wantApproved: true,
			wantStatus: infrastructurev1beta1.BootstrapKubeconfigStatus{
				TokenID:            admissionTokenID,
				CSRApproval:        infrastructurev1beta1.CSRApproved,
				ApprovedCSRName:    admissionCSRName,
				ApprovedCommonName: newSuffixCN,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			csr := admissionCSR(t, tc.username, tc.commonName)
			csr.Name = tc.csrName
			cl := newAdmissionClient(t, &interceptor.Funcs{}, tc.objects...)

			_, conditions, err := reconcileAdmissionWith(t, fake.NewSimpleClientset(csr), cl, tc.csrName)
			require.NoError(t, err)

			if tc.wantDenied {
				require.Len(t, conditions, 1)
				assert.Equal(t, certv1.CertificateDenied, conditions[0].Type)
				assert.Equal(t, tc.wantMessage, conditions[0].Message)
			}
			if tc.wantApproved {
				require.Len(t, conditions, 1)
				assert.Equal(t, certv1.CertificateApproved, conditions[0].Type)
			}

			list := &infrastructurev1beta1.BootstrapKubeconfigList{}
			err = cl.List(t.Context(), list, client.MatchingFields{controllers.BootstrapKubeconfigTokenIDIndex: strings.TrimPrefix(tc.username, "system:bootstrap:")})
			require.NoError(t, err)
			require.Len(t, list.Items, 1)
			assert.Equal(t, tc.wantStatus, list.Items[0].Status)
		})
	}
}

// TestByoAdmissionReconciler_Reconcile_ConcurrentCSRs makes a second CSR claim
// the token between the first CSR's read of the issuer and its status write.
// The write must fail on the stale resource version, so the first CSR is not
// approved, and its retry is denied.
func TestByoAdmissionReconciler_Reconcile_ConcurrentCSRs(t *testing.T) {
	const rivalCSRName = "byoh-csr-worker-1-rival"

	raced := false
	funcs := interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if !raced {
				raced = true
				rival := &infrastructurev1beta1.BootstrapKubeconfig{}
				err := cl.Get(ctx, client.ObjectKeyFromObject(obj), rival)
				require.NoError(t, err)
				rival.Status.CSRApproval = infrastructurev1beta1.CSRApproved
				rival.Status.ApprovedCSRName = rivalCSRName
				rival.Status.ApprovedCommonName = admissionIdentity
				err = cl.Status().Update(ctx, rival)
				require.NoError(t, err)
			}

			return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
	cl := newAdmissionClient(t, &funcs, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
	csr := admissionCSR(t, admissionUsername, admissionIdentity)
	clientSet := fake.NewSimpleClientset(csr)

	_, conditions, err := reconcileAdmissionWith(t, clientSet, cl, csr.Name)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err))
	assert.Empty(t, conditions)

	_, conditions, err = reconcileAdmissionWith(t, clientSet, cl, csr.Name)
	require.NoError(t, err)
	require.Len(t, conditions, 1)
	assert.Equal(t, certv1.CertificateDenied, conditions[0].Type)
}

func TestByoAdmissionReconciler_Reconcile_HolderListFails(t *testing.T) {
	testCases := []struct {
		name string
		kind client.ObjectList
	}{
		{
			name: "BootstrapKubeconfigs of the tenant",
			kind: &infrastructurev1beta1.BootstrapKubeconfigList{},
		},
		{
			name: "ByoHosts of the tenant",
			kind: &infrastructurev1beta1.ByoHostList{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			funcs := interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					listOptions := &client.ListOptions{}
					listOptions.ApplyOptions(opts)
					isTenantList := listOptions.Namespace == admissionNamespace
					if isTenantList && reflect.TypeOf(list) == reflect.TypeOf(tc.kind) {
						return errors.New("cache not started")
					}

					return cl.List(ctx, list, opts...)
				},
			}
			cl := newAdmissionClient(t, &funcs, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
			csr := admissionCSR(t, admissionUsername, admissionIdentity)

			_, conditions, err := reconcileAdmissionWith(t, fake.NewSimpleClientset(csr), cl, csr.Name)
			require.Error(t, err)
			assert.Empty(t, conditions)
		})
	}
}

// TestByoAdmissionReconciler_Reconcile_ApprovalFailsAfterClaim fails the
// approval after the token was claimed. The retry must approve the CSR without
// writing the issuer status again.
func TestByoAdmissionReconciler_Reconcile_ApprovalFailsAfterClaim(t *testing.T) {
	statusWrites := 0
	funcs := interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			statusWrites++

			return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
	cl := newAdmissionClient(t, &funcs, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
	csr := admissionCSR(t, admissionUsername, admissionIdentity)
	clientSet := fake.NewSimpleClientset(csr)
	approvalFails := true
	clientSet.PrependReactor("update", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if approvalFails && action.GetSubresource() == "approval" {
			return true, nil, errors.New("api server unavailable")
		}

		return false, nil, nil
	})

	_, conditions, err := reconcileAdmissionWith(t, clientSet, cl, csr.Name)
	require.Error(t, err)
	assert.Empty(t, conditions)
	assert.Equal(t, 1, statusWrites)

	approvalFails = false
	_, conditions, err = reconcileAdmissionWith(t, clientSet, cl, csr.Name)
	require.NoError(t, err)
	require.Len(t, conditions, 1)
	assert.Equal(t, certv1.CertificateApproved, conditions[0].Type)
	assert.Equal(t, 1, statusWrites)
}

func TestByoAdmissionReconciler_Reconcile_CertificateLifetime(t *testing.T) {
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
		expiration     *int32
		wantConditions []certv1.CertificateSigningRequestCondition
	}{
		{
			name:           "lifetime equal to the maximum",
			expiration:     ptr.To(int32(3600)),
			wantConditions: []certv1.CertificateSigningRequestCondition{approved},
		},
		{
			name:       "lifetime one second above the maximum",
			expiration: ptr.To(int32(3601)),
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied("requested certificate lifetime 3601 seconds is above the maximum of 3600 seconds"),
			},
		},
		{
			name:           "lifetime below the maximum",
			expiration:     ptr.To(int32(600)),
			wantConditions: []certv1.CertificateSigningRequestCondition{approved},
		},
		{
			name:       "no lifetime requested",
			expiration: nil,
			wantConditions: []certv1.CertificateSigningRequestCondition{
				denied("spec.expirationSeconds must be set to at most 3600 seconds"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := admissionCSR(t, admissionUsername, admissionIdentity)
			request.Spec.ExpirationSeconds = tc.expiration
			issuers := []client.Object{admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID)}

			_, conditions, err := reconcileAdmission(t, request, issuers, false)

			require.NoError(t, err)
			assert.Equal(t, tc.wantConditions, conditions)
		})
	}
}

// TestByoAdmissionReconciler_Reconcile_AgentRequestLifetime sends the CSR the
// agent creates, with the shared default lifetime, to an approver that has the
// same maximum.
func TestByoAdmissionReconciler_Reconcile_AgentRequestLifetime(t *testing.T) {
	template := admissionCSR(t, admissionUsername, admissionIdentity)
	clientSet := fake.NewSimpleClientset()
	agentLifetime := time.Duration(certificate.DefaultLifetimeSeconds) * time.Second

	_, _, err := certcsr.RequestCertificateWithContext(t.Context(), clientSet, template.Spec.Request, admissionCSRName,
		certv1.KubeAPIServerClientSignerName, &agentLifetime, []certv1.KeyUsage{certv1.UsageClientAuth}, nil)
	require.NoError(t, err)

	created, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), admissionCSRName, metav1.GetOptions{})
	require.NoError(t, err)
	created.Spec.Username = admissionUsername
	created.Spec.Groups = template.Spec.Groups
	_, err = clientSet.CertificatesV1().CertificateSigningRequests().Update(t.Context(), created, metav1.UpdateOptions{})
	require.NoError(t, err)

	cl := newAdmissionClient(t, &interceptor.Funcs{}, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
	reconciler := &controllers.ByoAdmissionReconciler{
		ClientSet:                     clientSet,
		Client:                        cl,
		APIReader:                     cl,
		MaxCertificateLifetimeSeconds: certificate.DefaultLifetimeSeconds,
	}
	_, err = reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: admissionCSRName}})
	require.NoError(t, err)

	got, err := clientSet.CertificatesV1().CertificateSigningRequests().Get(t.Context(), admissionCSRName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, ptr.To(int32(certificate.DefaultLifetimeSeconds)), got.Spec.ExpirationSeconds)
	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, certv1.CertificateApproved, got.Status.Conditions[0].Type)
}

func TestCheckCertificateLifetime(t *testing.T) {
	testCases := []struct {
		name       string
		max        int32
		expiration *int32
		wantErr    string
	}{
		{name: "equal to the maximum", max: 3600, expiration: ptr.To(int32(3600)), wantErr: ""},
		{name: "above the maximum", max: 3600, expiration: ptr.To(int32(3601)), wantErr: "above the maximum of 3600"},
		{name: "unset", max: 3600, expiration: nil, wantErr: "must be set"},
		{name: "zero maximum denies any lifetime", max: 0, expiration: ptr.To(int32(1)), wantErr: "above the maximum of 0"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := controllers.CheckCertificateLifetime(tc.expiration, tc.max)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// hostRBACName is the name of the Role and RoleBinding the approver creates for
// the host admissionIdentity.
const hostRBACName = "byoh-host-worker-1-a1b2c"

// wantHostRole returns the Role the approver creates for admissionIdentity.
func wantHostRole() *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:            hostRBACName,
			Namespace:       admissionNamespace,
			Labels:          map[string]string{identity.TenantLabel: admissionNamespace},
			OwnerReferences: []metav1.OwnerReference{wantIssuerOwner()},
			ResourceVersion: "1",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"infrastructure.cluster.x-k8s.io"},
				Resources:     []string{"byohosts"},
				ResourceNames: []string{"worker-1-a1b2c"},
				Verbs:         []string{"get", "list", "watch", "update", "patch", "delete"},
			},
			{
				APIGroups:     []string{"infrastructure.cluster.x-k8s.io"},
				Resources:     []string{"byohosts/status"},
				ResourceNames: []string{"worker-1-a1b2c"},
				Verbs:         []string{"get", "patch", "update"},
			},
		},
	}
}

// wantHostRoleBinding returns the RoleBinding the approver creates for
// admissionIdentity.
func wantHostRoleBinding() *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            hostRBACName,
			Namespace:       admissionNamespace,
			Labels:          map[string]string{identity.TenantLabel: admissionNamespace},
			OwnerReferences: []metav1.OwnerReference{wantIssuerOwner()},
			ResourceVersion: "1",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     rbacv1.UserKind,
				APIGroup: rbacv1.GroupName,
				Name:     admissionIdentity,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     hostRBACName,
		},
	}
}

func wantIssuerOwner() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
		Kind:       "BootstrapKubeconfig",
		Name:       "worker-1-a1b2c",
	}
}

// TestByoAdmissionReconciler_Reconcile_HostRBAC checks the Role and RoleBinding
// that the approver creates for the host before it approves the request.
func TestByoAdmissionReconciler_Reconcile_HostRBAC(t *testing.T) {
	key := client.ObjectKey{Namespace: admissionNamespace, Name: hostRBACName}

	t.Run("approval creates the Role and RoleBinding", func(t *testing.T) {
		cl := newAdmissionClient(t, &interceptor.Funcs{}, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
		csr := admissionCSR(t, admissionUsername, admissionIdentity)

		_, conditions, err := reconcileAdmissionWith(t, fake.NewSimpleClientset(csr), cl, csr.Name)
		require.NoError(t, err)
		require.Len(t, conditions, 1)
		assert.Equal(t, certv1.CertificateApproved, conditions[0].Type)

		role := &rbacv1.Role{}
		err = cl.Get(t.Context(), key, role)
		require.NoError(t, err)
		role.TypeMeta = metav1.TypeMeta{}
		assert.Equal(t, wantHostRole(), role)

		binding := &rbacv1.RoleBinding{}
		err = cl.Get(t.Context(), key, binding)
		require.NoError(t, err)
		binding.TypeMeta = metav1.TypeMeta{}
		assert.Equal(t, wantHostRoleBinding(), binding)
	})

	t.Run("denied request creates nothing", func(t *testing.T) {
		cl := newAdmissionClient(t, &interceptor.Funcs{}, admissionIssuer("worker-1-a1b2c", admissionNamespace, "zzzzzz"))
		csr := admissionCSR(t, admissionUsername, admissionIdentity)

		_, conditions, err := reconcileAdmissionWith(t, fake.NewSimpleClientset(csr), cl, csr.Name)
		require.NoError(t, err)
		require.Len(t, conditions, 1)
		assert.Equal(t, certv1.CertificateDenied, conditions[0].Type)

		roles := &rbacv1.RoleList{}
		err = cl.List(t.Context(), roles)
		require.NoError(t, err)
		assert.Empty(t, roles.Items)

		bindings := &rbacv1.RoleBindingList{}
		err = cl.List(t.Context(), bindings)
		require.NoError(t, err)
		assert.Empty(t, bindings.Items)
	})

	failures := []struct {
		name string
		kind client.Object
	}{
		{
			name: "Role create fails",
			kind: &rbacv1.Role{},
		},
		{
			name: "RoleBinding create fails",
			kind: &rbacv1.RoleBinding{},
		},
	}
	for _, tc := range failures {
		t.Run(tc.name+" and the retry approves", func(t *testing.T) {
			createFails := true
			funcs := interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if createFails && reflect.TypeOf(obj) == reflect.TypeOf(tc.kind) {
						return errors.New("api server unavailable")
					}

					return cl.Create(ctx, obj, opts...)
				},
			}
			cl := newAdmissionClient(t, &funcs, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID))
			csr := admissionCSR(t, admissionUsername, admissionIdentity)
			clientSet := fake.NewSimpleClientset(csr)

			_, conditions, err := reconcileAdmissionWith(t, clientSet, cl, csr.Name)
			require.Error(t, err)
			assert.Empty(t, conditions)

			issuer := &infrastructurev1beta1.BootstrapKubeconfig{}
			err = cl.Get(t.Context(), client.ObjectKey{Namespace: admissionNamespace, Name: "worker-1-a1b2c"}, issuer)
			require.NoError(t, err)
			assert.Equal(t, csr.Name, issuer.Status.ApprovedCSRName, "the claim is written before the Role")

			createFails = false
			_, conditions, err = reconcileAdmissionWith(t, clientSet, cl, csr.Name)
			require.NoError(t, err)
			require.Len(t, conditions, 1)
			assert.Equal(t, certv1.CertificateApproved, conditions[0].Type)

			err = cl.Get(t.Context(), key, &rbacv1.Role{})
			require.NoError(t, err)
			err = cl.Get(t.Context(), key, &rbacv1.RoleBinding{})
			require.NoError(t, err)
		})
	}

	t.Run("existing Role and RoleBinding are not rewritten", func(t *testing.T) {
		existingRole := wantHostRole()
		existingRole.ResourceVersion = ""
		existingRole.Rules = append(existingRole.Rules, rbacv1.PolicyRule{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{"byoh-install-m1"},
			Verbs:         []string{"get"},
		})
		existingBinding := wantHostRoleBinding()
		existingBinding.ResourceVersion = ""

		writes := 0
		funcs := interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes++

				return cl.Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				writes++

				return cl.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				writes++

				return cl.Delete(ctx, obj, opts...)
			},
		}
		cl := newAdmissionClient(t, &funcs, admissionIssuer("worker-1-a1b2c", admissionNamespace, admissionTokenID), existingRole, existingBinding)
		csr := admissionCSR(t, admissionUsername, admissionIdentity)

		_, conditions, err := reconcileAdmissionWith(t, fake.NewSimpleClientset(csr), cl, csr.Name)
		require.NoError(t, err)
		require.Len(t, conditions, 1)
		assert.Equal(t, certv1.CertificateApproved, conditions[0].Type)

		role := &rbacv1.Role{}
		err = cl.Get(t.Context(), key, role)
		require.NoError(t, err)
		assert.Equal(t, "999", role.ResourceVersion)
		assert.Len(t, role.Rules, 3)
		assert.Equal(t, 0, writes)
	})
}
