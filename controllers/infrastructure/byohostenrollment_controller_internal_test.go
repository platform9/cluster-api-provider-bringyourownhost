// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// The credential-generating helpers under test are unexported, so these tests
// live in the package itself rather than in the external test package.
package controllers //nolint: testpackage // exercises unexported credential-generating helpers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	testEnrollmentName      = "web-01"
	testEnrollmentNamespace = "tenant-a"
	testAPIServerURL        = "https://vcluster-cp.example.test:443"
)

// testCAPEM returns a self-signed certificate in PEM form, so the CA
// validation in ValidateCABundle sees something it can actually parse.
func testCAPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NotNil(t, encoded)
	return string(encoded)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)
	err = infrav1.AddToScheme(scheme)
	require.NoError(t, err)
	err = clusterv1.AddToScheme(scheme)
	require.NoError(t, err)
	return scheme
}

func testEnrollment() *infrav1.ByoHostEnrollment {
	return &infrav1.ByoHostEnrollment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testEnrollmentName,
			Namespace:  testEnrollmentNamespace,
			UID:        types.UID("enrollment-uid"),
			Generation: 1,
		},
		Spec: infrav1.ByoHostEnrollmentSpec{
			TokenTTL: &metav1.Duration{Duration: 30 * time.Minute},
		},
	}
}

// testRootCAConfigMap builds the kube-root-ca.crt ConfigMap the root CA
// publisher maintains, carrying a certificate the controller can parse.
func testRootCAConfigMap(t *testing.T) *corev1.ConfigMap {
	t.Helper()

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rootCAConfigMapName,
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{rootCAConfigMapKey: testCAPEM(t)},
	}
}

// testCAFile writes data to a PEM file the test owns and returns its path.
func testCAFile(t *testing.T, data string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ca.crt")
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)
	return path
}

// newTestReconciler builds a reconciler over a fake client seeded with objs.
func newTestReconciler(t *testing.T, objs ...client.Object) (*ByoHostEnrollmentReconciler, client.Client) {
	t.Helper()

	return newTestReconcilerWithInterceptor(t, nil, objs...)
}

// newTestReconcilerWithInterceptor builds a reconciler whose client routes the
// given calls through funcs, so a test can observe write order or force a
// failure the fake client would never produce. A nil funcs leaves the fake
// client untouched.
func newTestReconcilerWithInterceptor(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*ByoHostEnrollmentReconciler, client.Client) {
	t.Helper()

	scheme := testScheme(t)
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&infrav1.ByoHostEnrollment{})
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	fakeClient := builder.Build()

	return &ByoHostEnrollmentReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Transport: BootstrapTransport{APIServerURL: testAPIServerURL},
	}, fakeClient
}

func enrollmentRequest() ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Namespace: testEnrollmentNamespace,
			Name:      testEnrollmentName,
		},
	}
}

// getEnrollment reads the enrollment back from the client.
func getEnrollment(t *testing.T, c client.Client) *infrav1.ByoHostEnrollment {
	t.Helper()

	enrollment := &infrav1.ByoHostEnrollment{}
	err := c.Get(context.Background(), enrollmentRequest().NamespacedName, enrollment)
	require.NoError(t, err)
	return enrollment
}

// getTokenSecret reads the bootstrap token Secret for tokenID.
func getTokenSecret(t *testing.T, c client.Client, tokenID string) (*corev1.Secret, error) {
	t.Helper()

	secret := &corev1.Secret{}
	key := client.ObjectKey{
		Namespace: metav1.NamespaceSystem,
		Name:      bootstraputil.BootstrapTokenSecretName(tokenID),
	}
	err := c.Get(context.Background(), key, secret)
	return secret, err
}

// getCredentialSecret reads the credential Secret ref names.
func getCredentialSecret(t *testing.T, c client.Client, namespace string, ref *corev1.LocalObjectReference) *corev1.Secret {
	t.Helper()

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
	err := c.Get(context.Background(), key, secret)
	require.NoError(t, err)
	return secret
}

// assertBootstrapTokenSecret checks the token Secret a reconcile writes for
// tokenID: its type, the fields byohctl relies on, and its enrollment label.
func assertBootstrapTokenSecret(t *testing.T, secret *corev1.Secret, tokenID string) {
	t.Helper()

	assert.Equal(t, bootstrapapi.SecretTypeBootstrapToken, secret.Type)
	assert.Equal(t, tokenID, string(secret.Data[bootstrapapi.BootstrapTokenIDKey]))
	assert.Equal(t, infrav1.BootstrapTokenExtraGroups, string(secret.Data[bootstrapapi.BootstrapTokenExtraGroupsKey]))
	assert.Equal(t, "true", string(secret.Data[bootstrapapi.BootstrapTokenUsageAuthentication]))
	assert.NotContains(t, secret.Data, bootstrapapi.BootstrapTokenUsageSigningKey)
	assert.Equal(t, "Bootstrap credential for BYOH host "+testEnrollmentName,
		string(secret.Data[bootstrapapi.BootstrapTokenDescriptionKey]))
	assert.NotEmpty(t, secret.Labels[infrav1.EnrollmentLabel])
}

// assertCredentialSecretOwnerReference checks the credential Secret is owned
// by the enrollment it was generated for. Every field of the reference is
// deterministic, so the whole slice is compared in one shot rather than
// field by field.
func assertCredentialSecretOwnerReference(t *testing.T, secret *corev1.Secret, enrollmentName string, ownerUID types.UID) {
	t.Helper()

	want := []metav1.OwnerReference{{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "ByoHostEnrollment",
		Name:       enrollmentName,
		UID:        ownerUID,
		Controller: new(true),
	}}
	assert.Equal(t, want, secret.OwnerReferences)
}

// assertKubeconfigTransport checks the rendered kubeconfig names the
// expected server and CA, and carries the enrollment's bootstrap token.
func assertKubeconfigTransport(t *testing.T, config *clientcmdapi.Config, wantCA, tokenID string) {
	t.Helper()

	assert.Equal(t, infrav1.DefaultContext, config.CurrentContext)
	assert.Equal(t, testAPIServerURL, config.Clusters[infrav1.DefaultClusterName].Server)
	assert.Equal(t, []byte(wantCA),
		config.Clusters[infrav1.DefaultClusterName].CertificateAuthorityData)
	assert.False(t, config.Clusters[infrav1.DefaultClusterName].InsecureSkipTLSVerify)
	assert.True(t, strings.HasPrefix(config.AuthInfos[infrav1.DefaultAuth].Token, tokenID+"."))
	assert.Equal(t, infrav1.DefaultClusterName, config.Contexts[infrav1.DefaultContext].Cluster)
	assert.Equal(t, infrav1.DefaultAuth, config.Contexts[infrav1.DefaultContext].AuthInfo)
	assert.Equal(t, infrav1.DefaultNamespace, config.Contexts[infrav1.DefaultContext].Namespace)
}

// TestReconcileHappyPath reconciles a freshly created enrollment once, then
// walks the result in subtests that build on each other in order: the
// reconcile outcome, the bootstrap token Secret, the enrollment status, the
// credential Secret and its kubeconfig, and finally the CredentialReady
// condition.
func TestReconcileHappyPath(t *testing.T) {
	rootCAConfigMap := testRootCAConfigMap(t)
	r, c := newTestReconciler(t, testEnrollment(), rootCAConfigMap)

	result, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	enrollment := getEnrollment(t, c)

	var credentialSecret *corev1.Secret

	t.Run("succeeds without requeue", func(t *testing.T) {
		assert.Zero(t, result.RequeueAfter)
	})

	t.Run("sets the enrollment finalizer", func(t *testing.T) {
		assert.True(t, controllerutil.ContainsFinalizer(enrollment, infrav1.EnrollmentFinalizer))
	})

	t.Run("creates the bootstrap token secret in kube-system", func(t *testing.T) {
		tokenSecret, err := getTokenSecret(t, c, enrollment.Status.TokenID)
		require.NoError(t, err)
		assertBootstrapTokenSecret(t, tokenSecret, enrollment.Status.TokenID)
	})

	t.Run("records the token id and expiry in status", func(t *testing.T) {
		assert.NotEmpty(t, enrollment.Status.TokenID)
		require.NotNil(t, enrollment.Status.ExpiresAt)

		// Consumed stays undriven until consumption detection lands.
		assert.Nil(t, conditions.Get(enrollment, infrav1.Consumed))
	})

	t.Run("creates the credential secret with the expected identity and owner", func(t *testing.T) {
		require.NotNil(t, enrollment.Status.CredentialSecretRef)
		credentialSecret = getCredentialSecret(t, c, testEnrollmentNamespace, enrollment.Status.CredentialSecretRef)

		assert.Equal(t, testEnrollmentName+infrav1.CredentialSecretNameSuffix, credentialSecret.Name)
		assert.Equal(t, testEnrollmentNamespace, credentialSecret.Namespace)
		assert.Equal(t, infrav1.CredentialSecretType, credentialSecret.Type)
		assertCredentialSecretOwnerReference(t, credentialSecret, enrollment.Name, enrollment.UID)
	})

	t.Run("kubeconfig names the flag's server and the cluster root CA", func(t *testing.T) {
		require.NotEmpty(t, credentialSecret.Data[infrav1.CredentialSecretKubeconfigKey])
		config, err := clientcmd.Load(credentialSecret.Data[infrav1.CredentialSecretKubeconfigKey])
		require.NoError(t, err)
		assertKubeconfigTransport(t, config, rootCAConfigMap.Data[rootCAConfigMapKey], enrollment.Status.TokenID)
	})

	t.Run("hostName key matches the enrollment", func(t *testing.T) {
		assert.Equal(t, testEnrollmentName, string(credentialSecret.Data[infrav1.CredentialSecretHostNameKey]))
	})

	t.Run("CredentialReady is true with observedGeneration set", func(t *testing.T) {
		assert.True(t, conditions.IsTrue(enrollment, infrav1.CredentialReady))
		assert.Equal(t, int64(1), enrollment.Status.ObservedGeneration)
	})
}

func TestReconcileInvalidHostName(t *testing.T) {
	enrollment := testEnrollment()
	enrollment.Name = "web$01"
	r, c := newTestReconciler(t, enrollment, testRootCAConfigMap(t))

	key := types.NamespacedName{Namespace: testEnrollmentNamespace, Name: enrollment.Name}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter, "a name that cannot normalize is not worth retrying")

	got := &infrav1.ByoHostEnrollment{}
	err = c.Get(context.Background(), key, got)
	require.NoError(t, err)
	assert.Empty(t, got.Status.TokenID)
	assert.Nil(t, got.Status.CredentialSecretRef)
	assert.True(t, conditions.IsFalse(got, infrav1.CredentialReady))
	assert.Equal(t, infrav1.InvalidHostNameReason, conditions.GetReason(got, infrav1.CredentialReady))
}

func TestReconcileMissingRootCAConfigMap(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment())

	result, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	assert.Equal(t, transportRetryInterval, result.RequeueAfter)

	enrollment := getEnrollment(t, c)
	assert.Empty(t, enrollment.Status.TokenID)
	assert.Nil(t, enrollment.Status.CredentialSecretRef)
	assert.True(t, conditions.IsFalse(enrollment, infrav1.CredentialReady))
	assert.Equal(t, infrav1.TransportUnavailableReason, conditions.GetReason(enrollment, infrav1.CredentialReady))

	secrets := &corev1.SecretList{}
	err = c.List(context.Background(), secrets)
	require.NoError(t, err)
	assert.Empty(t, secrets.Items, "no credential may be created without a CA")
}

func TestReconcileMalformedRootCAConfigMap(t *testing.T) {
	testCases := []struct {
		name string
		data map[string]string
	}{
		{
			name: "no ca key",
			data: map[string]string{"other": "value"},
		},
		{
			name: "ca key is empty",
			data: map[string]string{rootCAConfigMapKey: ""},
		},
		{
			name: "ca data is not pem",
			data: map[string]string{rootCAConfigMapKey: "not a certificate"},
		},
		{
			name: "ca data is pem but not a certificate",
			data: map[string]string{rootCAConfigMapKey: "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rootCAConfigMapName,
					Namespace: metav1.NamespaceSystem,
				},
				Data: tc.data,
			}
			r, c := newTestReconciler(t, testEnrollment(), configMap)

			result, err := r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			assert.Equal(t, transportRetryInterval, result.RequeueAfter)

			enrollment := getEnrollment(t, c)
			assert.Empty(t, enrollment.Status.TokenID)
			assert.Equal(t, infrav1.TransportUnavailableReason, conditions.GetReason(enrollment, infrav1.CredentialReady))
		})
	}
}

// TestReconcileCAOverrideWins covers the flag override: an operator whose
// hosts reach an external endpoint supplies their own bundle, and the
// cluster's kube-root-ca.crt must not be used instead.
func TestReconcileCAOverrideWins(t *testing.T) {
	rootCAConfigMap := testRootCAConfigMap(t)
	overrideCA := testCAPEM(t)
	require.NotEqual(t, rootCAConfigMap.Data[rootCAConfigMapKey], overrideCA)

	r, c := newTestReconciler(t, testEnrollment(), rootCAConfigMap)
	r.Transport.CAData = []byte(overrideCA)

	_, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)

	enrollment := getEnrollment(t, c)
	require.NotNil(t, enrollment.Status.CredentialSecretRef)
	credentialSecret := getCredentialSecret(t, c, testEnrollmentNamespace, enrollment.Status.CredentialSecretRef)

	config, err := clientcmd.Load(credentialSecret.Data[infrav1.CredentialSecretKubeconfigKey])
	require.NoError(t, err)
	assertKubeconfigTransport(t, config, overrideCA, enrollment.Status.TokenID)
}

// TestReconcileWithoutAPIServerURL covers a reconciler built without the
// startup constructor. It must refuse rather than hand a host a kubeconfig
// with no server in it.
func TestReconcileWithoutAPIServerURL(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment(), testRootCAConfigMap(t))
	r.Transport.APIServerURL = ""

	result, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	assert.Equal(t, transportRetryInterval, result.RequeueAfter)

	enrollment := getEnrollment(t, c)
	assert.Empty(t, enrollment.Status.TokenID)
	assert.Equal(t, infrav1.TransportUnavailableReason, conditions.GetReason(enrollment, infrav1.CredentialReady))
}

func TestReconcileIsIdempotent(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment(), testRootCAConfigMap(t))

	_, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	first := getEnrollment(t, c)

	_, err = r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	second := getEnrollment(t, c)

	assert.Equal(t, first.Status.TokenID, second.Status.TokenID, "a second pass must not create a second token")

	tokenSecrets := &corev1.SecretList{}
	err = c.List(context.Background(), tokenSecrets, client.InNamespace(metav1.NamespaceSystem))
	require.NoError(t, err)
	assert.Len(t, tokenSecrets.Items, 1)
}

// TestReconcileResumesFromIntermediateState walks the states a reconcile can
// die in and checks the next pass finishes rather than stranding a live token.
func TestReconcileResumesFromIntermediateState(t *testing.T) {
	testCases := []struct {
		name string
		// spoil mutates the cluster to look like the reconcile stopped part
		// way through, and returns the token ID that must not survive.
		spoil          func(t *testing.T, c client.Client, enrollment *infrav1.ByoHostEnrollment)
		wantSameTokeID bool
	}{
		{
			name: "died after recording the token id, before writing the token secret",
			spoil: func(t *testing.T, c client.Client, enrollment *infrav1.ByoHostEnrollment) {
				t.Helper()
				secret, err := getTokenSecret(t, c, enrollment.Status.TokenID)
				require.NoError(t, err)
				err = c.Delete(context.Background(), secret)
				require.NoError(t, err)
			},
			wantSameTokeID: false,
		},
		{
			name: "died after writing the token secret, before writing the credential secret",
			spoil: func(t *testing.T, c client.Client, enrollment *infrav1.ByoHostEnrollment) {
				t.Helper()
				secret := &corev1.Secret{}
				key := client.ObjectKey{
					Namespace: enrollment.Namespace,
					Name:      credentialSecretName(enrollment.Name),
				}
				err := c.Get(context.Background(), key, secret)
				require.NoError(t, err)
				err = c.Delete(context.Background(), secret)
				require.NoError(t, err)
			},
			wantSameTokeID: true,
		},
		{
			name: "died before the final status patch",
			spoil: func(t *testing.T, c client.Client, enrollment *infrav1.ByoHostEnrollment) {
				t.Helper()
				enrollment.Status.CredentialSecretRef = nil
				enrollment.Status.ObservedGeneration = 0
				conditions.Delete(enrollment, infrav1.CredentialReady)
				err := c.Status().Update(context.Background(), enrollment)
				require.NoError(t, err)
			},
			wantSameTokeID: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newTestReconciler(t, testEnrollment(), testRootCAConfigMap(t))

			_, err := r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			before := getEnrollment(t, c)
			require.NotEmpty(t, before.Status.TokenID)

			tc.spoil(t, c, before)

			_, err = r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			after := getEnrollment(t, c)

			if tc.wantSameTokeID {
				assert.Equal(t, before.Status.TokenID, after.Status.TokenID)
			} else {
				assert.NotEqual(t, before.Status.TokenID, after.Status.TokenID)
			}

			assert.True(t, conditions.IsTrue(after, infrav1.CredentialReady))
			require.NotNil(t, after.Status.CredentialSecretRef)

			credential := &corev1.Secret{}
			key := client.ObjectKey{Namespace: testEnrollmentNamespace, Name: after.Status.CredentialSecretRef.Name}
			err = c.Get(context.Background(), key, credential)
			require.NoError(t, err)

			// Whatever happened, exactly one live token exists.
			tokenSecrets := &corev1.SecretList{}
			err = c.List(context.Background(), tokenSecrets, client.InNamespace(metav1.NamespaceSystem))
			require.NoError(t, err)
			assert.Len(t, tokenSecrets.Items, 1)

			// And every created token carries an expiration.
			assert.NotEmpty(t, tokenSecrets.Items[0].Data[bootstrapapi.BootstrapTokenExpirationKey])
		})
	}
}

// TestReconcileReplacesTokenSecretWithoutExpiration covers the case that makes
// a token immortal: a token Secret with no expiration key must never be reused.
func TestReconcileReplacesUnusableTokenSecret(t *testing.T) {
	testCases := []struct {
		name  string
		spoil func(secret *corev1.Secret)
	}{
		{
			name: "expiration key is missing",
			spoil: func(secret *corev1.Secret) {
				delete(secret.Data, bootstrapapi.BootstrapTokenExpirationKey)
			},
		},
		{
			name: "expiration is not a timestamp",
			spoil: func(secret *corev1.Secret) {
				secret.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte("soon")
			},
		},
		{
			name: "expiration is in the past",
			spoil: func(secret *corev1.Secret) {
				past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
				secret.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(past)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newTestReconciler(t, testEnrollment(), testRootCAConfigMap(t))

			_, err := r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			before := getEnrollment(t, c)

			secret, err := getTokenSecret(t, c, before.Status.TokenID)
			require.NoError(t, err)
			tc.spoil(secret)
			err = c.Update(context.Background(), secret)
			require.NoError(t, err)

			_, err = r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			after := getEnrollment(t, c)

			assert.NotEqual(t, before.Status.TokenID, after.Status.TokenID)

			_, err = getTokenSecret(t, c, before.Status.TokenID)
			assert.True(t, apierrors.IsNotFound(err), "the unusable token must be deleted, not left live")

			replacement, err := getTokenSecret(t, c, after.Status.TokenID)
			require.NoError(t, err)
			expiration := string(replacement.Data[bootstrapapi.BootstrapTokenExpirationKey])
			assert.NotEmpty(t, expiration)
			parsed, err := time.Parse(time.RFC3339, expiration)
			require.NoError(t, err)
			assert.True(t, parsed.After(time.Now()))
		})
	}
}

func TestReconcileDelete(t *testing.T) {
	testCases := []struct {
		name                string
		removeTokenSecret   bool
		wantTokenSecretGone bool
	}{
		{
			name:                "token secret present",
			removeTokenSecret:   false,
			wantTokenSecretGone: true,
		},
		{
			name:                "token secret already gone",
			removeTokenSecret:   true,
			wantTokenSecretGone: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newTestReconciler(t, testEnrollment(), testRootCAConfigMap(t))

			_, err := r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)
			enrollment := getEnrollment(t, c)
			tokenID := enrollment.Status.TokenID

			if tc.removeTokenSecret {
				secret, getErr := getTokenSecret(t, c, tokenID)
				require.NoError(t, getErr)
				err = c.Delete(context.Background(), secret)
				require.NoError(t, err)
			}

			err = c.Delete(context.Background(), enrollment)
			require.NoError(t, err)

			_, err = r.Reconcile(context.Background(), enrollmentRequest())
			require.NoError(t, err)

			_, err = getTokenSecret(t, c, tokenID)
			assert.Equal(t, tc.wantTokenSecretGone, apierrors.IsNotFound(err))

			// Removing the finalizer lets the API server finish the delete, so
			// the object is gone rather than merely unfinalized.
			remaining := &infrav1.ByoHostEnrollment{}
			err = c.Get(context.Background(), enrollmentRequest().NamespacedName, remaining)
			assert.True(t, apierrors.IsNotFound(err))
		})
	}
}

func TestCredentialSecretName(t *testing.T) {
	longName := strings.Repeat("a", infrav1.MaxK8sObjectNameLength)
	otherLongName := strings.Repeat("a", infrav1.MaxK8sObjectNameLength-1) + "b"

	testCases := []struct {
		name           string
		enrollmentName string
		want           string
	}{
		{
			name:           "short name keeps the plain suffix",
			enrollmentName: "web-01",
			want:           "web-01" + infrav1.CredentialSecretNameSuffix,
		},
		{
			name:           "name that exactly fills the budget keeps the plain suffix",
			enrollmentName: strings.Repeat("a", infrav1.MaxK8sObjectNameLength-len(infrav1.CredentialSecretNameSuffix)),
			want: strings.Repeat("a", infrav1.MaxK8sObjectNameLength-len(infrav1.CredentialSecretNameSuffix)) +
				infrav1.CredentialSecretNameSuffix,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := credentialSecretName(tc.enrollmentName)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), infrav1.MaxK8sObjectNameLength)
		})
	}

	t.Run("maximum length name is truncated and hashed", func(t *testing.T) {
		got := credentialSecretName(longName)
		assert.Len(t, got, infrav1.MaxK8sObjectNameLength)
		assert.True(t, strings.HasSuffix(got, infrav1.CredentialSecretNameSuffix))

		errs := validation.IsDNS1123Subdomain(got)
		assert.Empty(t, errs)
	})

	t.Run("two long names that share a prefix do not collide", func(t *testing.T) {
		first := credentialSecretName(longName)
		second := credentialSecretName(otherLongName)
		assert.NotEqual(t, first, second)
	})

	t.Run("the same name always produces the same secret name", func(t *testing.T) {
		assert.Equal(t, credentialSecretName(longName), credentialSecretName(longName))
	})
}

func TestBuildTokenSecret(t *testing.T) {
	const tokenStr = "abcdef.0123456789abcdef"
	expiresAt := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)

	t.Run("expiration is always written", func(t *testing.T) {
		secret, err := buildTokenSecret(tokenStr, testEnrollmentName, infrav1.BootstrapTokenExtraGroups, expiresAt)
		require.NoError(t, err)
		assert.Equal(t, "2026-08-28T12:00:00Z", string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
		assert.Equal(t, "bootstrap-token-abcdef", secret.Name)
		assert.Equal(t, metav1.NamespaceSystem, secret.Namespace)
	})

	t.Run("a local expiry is written as UTC", func(t *testing.T) {
		local := expiresAt.In(time.FixedZone("test", 5*3600))
		secret, err := buildTokenSecret(tokenStr, testEnrollmentName, infrav1.BootstrapTokenExtraGroups, local)
		require.NoError(t, err)
		assert.Equal(t, "2026-08-28T12:00:00Z", string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
	})

	t.Run("a zero expiry is refused", func(t *testing.T) {
		_, err := buildTokenSecret(tokenStr, testEnrollmentName, infrav1.BootstrapTokenExtraGroups, time.Time{})
		require.Error(t, err)
	})

	groupCases := []struct {
		name    string
		group   string
		wantErr bool
	}{
		{name: "the shipped group is accepted", group: infrav1.BootstrapTokenExtraGroups, wantErr: false},
		{name: "a group outside the bootstrap prefix is refused", group: "byoh:hosts", wantErr: true},
		{name: "the bare bootstrappers group is refused", group: "system:bootstrappers", wantErr: true},
		{name: "an empty group is refused", group: "", wantErr: true},
	}

	for _, tc := range groupCases {
		t.Run(tc.name, func(t *testing.T) {
			secret, err := buildTokenSecret(tokenStr, testEnrollmentName, tc.group, expiresAt)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, secret, "an unusable group must not produce a Secret")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.group, string(secret.Data[bootstrapapi.BootstrapTokenExtraGroupsKey]))
		})
	}

	t.Run("a malformed token is refused", func(t *testing.T) {
		_, err := buildTokenSecret("nope", testEnrollmentName, infrav1.BootstrapTokenExtraGroups, expiresAt)
		require.Error(t, err)
	})
}

func TestValidateToken(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)

	secretWith := func(mutate func(data map[string][]byte)) *corev1.Secret {
		data := map[string][]byte{
			bootstrapapi.BootstrapTokenIDKey:         []byte("abcdef"),
			bootstrapapi.BootstrapTokenSecretKey:     []byte("0123456789abcdef"),
			bootstrapapi.BootstrapTokenExpirationKey: []byte(now.Add(time.Hour).Format(time.RFC3339)),
		}
		if mutate != nil {
			mutate(data)
		}
		return &corev1.Secret{Data: data}
	}

	testCases := []struct {
		name      string
		secret    *corev1.Secret
		wantValid bool
	}{
		{
			name:      "live token",
			secret:    secretWith(nil),
			wantValid: true,
		},
		{
			name: "no expiration",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenExpirationKey)
			}),
			wantValid: false,
		},
		{
			name: "unparseable expiration",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte("tomorrow")
			}),
			wantValid: false,
		},
		{
			name: "expired",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(now.Add(-time.Minute).Format(time.RFC3339))
			}),
			wantValid: false,
		},
		{
			name: "expires inside the renewal margin",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(now.Add(time.Second).Format(time.RFC3339))
			}),
			wantValid: false,
		},
		{
			name: "no token id",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenIDKey)
			}),
			wantValid: false,
		},
		{
			name: "no token secret",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenSecretKey)
			}),
			wantValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			token := validateToken(tc.secret, now)
			if !tc.wantValid {
				assert.Nil(t, token, "an unusable Secret must not yield a token")
				return
			}
			require.NotNil(t, token)
			assert.Equal(t, "abcdef.0123456789abcdef", token.value)
			assert.True(t, token.expiresAt.After(now))
		})
	}
}

// TestNewBootstrapTransport covers the startup check. A bad flag must stop the
// manager, not leave every enrollment waiting on a condition nobody watches.
func TestNewBootstrapTransport(t *testing.T) {
	caPEM := testCAPEM(t)
	validCAFile := testCAFile(t, caPEM)
	malformedCAFile := testCAFile(t, "not a certificate")
	missingCAFile := filepath.Join(t.TempDir(), "absent.crt")

	testCases := []struct {
		name         string
		apiServerURL string
		caFile       string
		wantCAData   string
		wantErr      bool
	}{
		{
			name:         "a valid url with no ca file leaves the ca to the cluster",
			apiServerURL: testAPIServerURL,
		},
		{
			name:         "a valid ca file is loaded as the override",
			apiServerURL: testAPIServerURL,
			caFile:       validCAFile,
			wantCAData:   caPEM,
		},
		{
			name:    "an empty url is refused",
			wantErr: true,
		},
		{
			name:         "a url that is not https is refused",
			apiServerURL: "http://vcluster-cp.example.test:443",
			wantErr:      true,
		},
		{
			name:         "a url with no host is refused",
			apiServerURL: "https://",
			wantErr:      true,
		},
		{
			name:         "a url that does not parse is refused",
			apiServerURL: "https://[::1",
			wantErr:      true,
		},
		{
			name:         "a ca file that does not exist is refused",
			apiServerURL: testAPIServerURL,
			caFile:       missingCAFile,
			wantErr:      true,
		},
		{
			name:         "a ca file that is not pem is refused",
			apiServerURL: testAPIServerURL,
			caFile:       malformedCAFile,
			wantErr:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			transport, err := NewBootstrapTransport(tc.apiServerURL, tc.caFile)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, transport.APIServerURL, "a refused configuration must not yield a transport")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.apiServerURL, transport.APIServerURL)
			assert.Equal(t, tc.wantCAData, string(transport.CAData))
		})
	}
}

func TestValidateAPIServerURL(t *testing.T) {
	testCases := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{
			name:   "an https url with a host and port",
			rawURL: testAPIServerURL,
		},
		{
			name:   "an https url with no port",
			rawURL: "https://vcluster-cp.example.test",
		},
		{
			name:    "empty",
			wantErr: true,
		},
		{
			name:    "http",
			rawURL:  "http://vcluster-cp.example.test:443",
			wantErr: true,
		},
		{
			name:    "no scheme",
			rawURL:  "vcluster-cp.example.test:443",
			wantErr: true,
		},
		{
			name:    "no host",
			rawURL:  "https://",
			wantErr: true,
		},
		{
			name:    "not a url",
			rawURL:  "https://[::1",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAPIServerURL(tc.rawURL)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateCABundle(t *testing.T) {
	caPEM := testCAPEM(t)

	testCases := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{
			name:    "one certificate",
			data:    caPEM,
			wantErr: false,
		},
		{
			name:    "two certificates",
			data:    caPEM + caPEM,
			wantErr: false,
		},
		{
			name:    "empty",
			data:    "",
			wantErr: true,
		},
		{
			name:    "not pem",
			data:    "hello",
			wantErr: true,
		},
		{
			name:    "wrong block type",
			data:    "-----BEGIN RSA PRIVATE KEY-----\nZm9v\n-----END RSA PRIVATE KEY-----\n",
			wantErr: true,
		},
		{
			name:    "pem body is not a certificate",
			data:    "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCABundle([]byte(tc.data))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestReconcileDeleteWithoutTokenID(t *testing.T) {
	var deletedNames []string

	enrollment := testEnrollment()
	enrollment.Finalizers = []string{infrav1.EnrollmentFinalizer}

	r, c := newTestReconcilerWithInterceptor(t, &interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deletedNames = append(deletedNames, obj.GetName())
			return cl.Delete(ctx, obj, opts...)
		},
	}, enrollment)

	result, err := r.reconcileDelete(context.Background(), getEnrollment(t, c))
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.Empty(t, deletedNames, "an enrollment that never recorded a token must not delete a Secret")

	got := getEnrollment(t, c)
	assert.False(t, controllerutil.ContainsFinalizer(got, infrav1.EnrollmentFinalizer))
}

func TestReconcileDeleteSurfacesTokenSecretDeleteError(t *testing.T) {
	enrollment := testEnrollment()
	enrollment.Finalizers = []string{infrav1.EnrollmentFinalizer}
	enrollment.Status.TokenID = "abcdef"

	r, c := newTestReconcilerWithInterceptor(t, &interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, isSecret := obj.(*corev1.Secret); isSecret {
				return apierrors.NewInternalError(errors.New("api server refused the delete"))
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}, enrollment)

	_, err := r.reconcileDelete(context.Background(), getEnrollment(t, c))
	require.Error(t, err)
	assert.Contains(t, err.Error(), bootstraputil.BootstrapTokenSecretName("abcdef"))

	got := getEnrollment(t, c)
	assert.True(t, controllerutil.ContainsFinalizer(got, infrav1.EnrollmentFinalizer),
		"the finalizer must stay while the token Secret is still live")
}

func TestEnsureBootstrapTokenReusesLiveToken(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment())

	first, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
	require.NoError(t, err)
	require.NotNil(t, second)

	assert.Equal(t, first.value, second.value, "a token with life left must be reused")
	// The reused expiry is read back from an RFC 3339 string, so it has lost
	// its sub-second part.
	assert.WithinDuration(t, first.expiresAt, second.expiresAt, time.Second)

	tokenSecrets := &corev1.SecretList{}
	err = c.List(context.Background(), tokenSecrets, client.InNamespace(metav1.NamespaceSystem))
	require.NoError(t, err)
	assert.Len(t, tokenSecrets.Items, 1)
}

func TestEnsureBootstrapTokenReplacesUnusableRecordedToken(t *testing.T) {
	testCases := []struct {
		name  string
		spoil func(secret *corev1.Secret)
	}{
		{
			name: "expiration key is missing",
			spoil: func(secret *corev1.Secret) {
				delete(secret.Data, bootstrapapi.BootstrapTokenExpirationKey)
			},
		},
		{
			name: "expiration is not a timestamp",
			spoil: func(secret *corev1.Secret) {
				secret.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte("soon")
			},
		},
		{
			name: "expiration is in the past",
			spoil: func(secret *corev1.Secret) {
				past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
				secret.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(past)
			},
		},
		{
			name: "the token secret value is missing",
			spoil: func(secret *corev1.Secret) {
				delete(secret.Data, bootstrapapi.BootstrapTokenSecretKey)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := newTestReconciler(t, testEnrollment())

			first, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
			require.NoError(t, err)
			recordedID := getEnrollment(t, c).Status.TokenID
			require.NotEmpty(t, recordedID)

			secret, err := getTokenSecret(t, c, recordedID)
			require.NoError(t, err)
			tc.spoil(secret)
			err = c.Update(context.Background(), secret)
			require.NoError(t, err)

			second, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
			require.NoError(t, err)
			require.NotNil(t, second)
			assert.NotEqual(t, first.value, second.value)

			_, err = getTokenSecret(t, c, recordedID)
			assert.True(t, apierrors.IsNotFound(err), "the unusable Secret must be deleted, not left live")

			replacementID := getEnrollment(t, c).Status.TokenID
			assert.NotEqual(t, recordedID, replacementID)
			replacement, err := getTokenSecret(t, c, replacementID)
			require.NoError(t, err)
			assert.NotEmpty(t, replacement.Data[bootstrapapi.BootstrapTokenExpirationKey])
		})
	}
}

func TestEnsureBootstrapTokenWhenRecordedSecretIsMissing(t *testing.T) {
	enrollment := testEnrollment()
	enrollment.Status.TokenID = "abcdef"
	r, c := newTestReconciler(t, enrollment)

	token, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
	require.NoError(t, err)
	require.NotNil(t, token)

	replacementID := getEnrollment(t, c).Status.TokenID
	assert.NotEqual(t, "abcdef", replacementID, "a token id with no Secret behind it must be replaced")

	_, err = getTokenSecret(t, c, replacementID)
	require.NoError(t, err)
}

func TestEnsureBootstrapTokenTTL(t *testing.T) {
	testCases := []struct {
		name    string
		ttl     *metav1.Duration
		wantTTL time.Duration
	}{
		{
			name:    "the spec ttl is honored",
			ttl:     &metav1.Duration{Duration: 2 * time.Hour},
			wantTTL: 2 * time.Hour,
		},
		{
			name:    "an unset spec ttl falls back to the default",
			ttl:     nil,
			wantTTL: defaultTokenTTL,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			enrollment := testEnrollment()
			enrollment.Spec.TokenTTL = tc.ttl
			r, c := newTestReconciler(t, enrollment)

			before := time.Now().UTC()
			token, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
			require.NoError(t, err)
			require.NotNil(t, token)
			assert.WithinDuration(t, before.Add(tc.wantTTL), token.expiresAt, time.Minute)

			secret, err := getTokenSecret(t, c, getEnrollment(t, c).Status.TokenID)
			require.NoError(t, err)
			expiry, err := time.Parse(time.RFC3339, string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
			require.NoError(t, err)
			assert.WithinDuration(t, token.expiresAt, expiry, time.Second)
		})
	}
}

// TestEnsureBootstrapTokenRecordsTokenIDBeforeSecret pins the write order. A
// reconcile that dies between the two writes must leave a status pointing at
// the token, never a live token nothing refers to.
func TestEnsureBootstrapTokenRecordsTokenIDBeforeSecret(t *testing.T) {
	var tokenIDAtSecretCreate string

	r, c := newTestReconcilerWithInterceptor(t, &interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			secret, isSecret := obj.(*corev1.Secret)
			if isSecret && secret.Type == bootstrapapi.SecretTypeBootstrapToken {
				stored := &infrav1.ByoHostEnrollment{}
				getErr := cl.Get(ctx, enrollmentRequest().NamespacedName, stored)
				require.NoError(t, getErr)
				tokenIDAtSecretCreate = stored.Status.TokenID
			}
			return cl.Create(ctx, obj, opts...)
		},
	}, testEnrollment())

	token, err := r.ensureBootstrapToken(context.Background(), getEnrollment(t, c), testEnrollmentName)
	require.NoError(t, err)
	require.NotNil(t, token)

	assert.NotEmpty(t, tokenIDAtSecretCreate)
	assert.Equal(t, getEnrollment(t, c).Status.TokenID, tokenIDAtSecretCreate)
}

func TestPatchEnrollment(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment())
	enrollment := getEnrollment(t, c)

	err := r.patchEnrollment(context.Background(), enrollment, func() {
		controllerutil.AddFinalizer(enrollment, infrav1.EnrollmentFinalizer)
		enrollment.Status.TokenID = "abcdef"
	})
	require.NoError(t, err)

	got := getEnrollment(t, c)
	assert.True(t, controllerutil.ContainsFinalizer(got, infrav1.EnrollmentFinalizer))
	assert.Equal(t, "abcdef", got.Status.TokenID)
}

func TestPatchEnrollmentWrapsFailure(t *testing.T) {
	r, _ := newTestReconciler(t)
	enrollment := testEnrollment()

	err := r.patchEnrollment(context.Background(), enrollment, func() {
		controllerutil.AddFinalizer(enrollment, infrav1.EnrollmentFinalizer)
		enrollment.Status.TokenID = "abcdef"
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), testEnrollmentNamespace+"/"+testEnrollmentName)
}

// TestPatchEnrollmentKeepsOwnedConditions covers the reason patchEnrollment
// takes the owned-conditions option: one reconcile patches the same object
// several times, and a later stage must not drop a condition an earlier one
// set.
func TestPatchEnrollmentKeepsOwnedConditions(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment())
	enrollment := getEnrollment(t, c)

	err := r.patchEnrollment(context.Background(), enrollment,
		enrollmentNotReadyMutateFn(enrollment, infrav1.TransportUnavailableReason, "no transport yet"))
	require.NoError(t, err)

	err = r.patchEnrollment(context.Background(), enrollment, func() {
		enrollment.Status.TokenID = "abcdef"
	})
	require.NoError(t, err)

	got := getEnrollment(t, c)
	assert.Equal(t, "abcdef", got.Status.TokenID)
	require.True(t, conditions.IsFalse(got, infrav1.CredentialReady))
	assert.Equal(t, infrav1.TransportUnavailableReason, conditions.GetReason(got, infrav1.CredentialReady))
	assert.Equal(t, "no transport yet", conditions.GetMessage(got, infrav1.CredentialReady))
}

func TestEnrollmentNotReadyMutateFn(t *testing.T) {
	enrollment := testEnrollment()
	enrollment.Generation = 3

	enrollmentNotReadyMutateFn(enrollment, infrav1.InvalidHostNameReason, "web$01 does not normalize")()

	assert.Equal(t, int64(3), enrollment.Status.ObservedGeneration)
	require.True(t, conditions.IsFalse(enrollment, infrav1.CredentialReady))
	assert.Equal(t, infrav1.InvalidHostNameReason, conditions.GetReason(enrollment, infrav1.CredentialReady))
	assert.Equal(t, "web$01 does not normalize", conditions.GetMessage(enrollment, infrav1.CredentialReady))

	severity := conditions.GetSeverity(enrollment, infrav1.CredentialReady)
	require.NotNil(t, severity)
	assert.Equal(t, clusterv1.ConditionSeverityWarning, *severity)
}
