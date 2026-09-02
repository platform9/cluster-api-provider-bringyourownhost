// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// The minting helpers under test are unexported, so these tests live in the
// package itself rather than in the external test package.
package controllers //nolint: testpackage // exercises unexported minting helpers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
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
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	testEnrollmentName      = "web-01"
	testEnrollmentNamespace = "tenant-a"
	testAPIServerURL        = "https://vcluster-cp.example.test:443"
	testTLSServerName       = "kubernetes.default"
)

// testCAPEM returns a self-signed certificate in PEM form, so the CA
// validation in parseTransportConfig sees something it can actually parse.
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

func testTransportConfigMap(t *testing.T) *corev1.ConfigMap {
	t.Helper()

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DefaultTransportConfigMapName,
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{
			TransportConfigMapAPIServerURLKey:  testAPIServerURL,
			TransportConfigMapTLSServerNameKey: testTLSServerName,
			TransportConfigMapCADataKey:        testCAPEM(t),
		},
	}
}

// newTestReconciler builds a reconciler over a fake client seeded with objs.
func newTestReconciler(t *testing.T, objs ...client.Object) (*ByoHostEnrollmentReconciler, client.Client) {
	t.Helper()

	scheme := testScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&infrav1.ByoHostEnrollment{}).
		Build()

	return &ByoHostEnrollmentReconciler{
		Client: fakeClient,
		Scheme: scheme,
		TransportConfigMap: types.NamespacedName{
			Namespace: metav1.NamespaceSystem,
			Name:      DefaultTransportConfigMapName,
		},
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

func TestReconcileHappyPath(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment(), testTransportConfigMap(t))

	result, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	enrollment := getEnrollment(t, c)
	assert.True(t, controllerutil.ContainsFinalizer(enrollment, infrav1.EnrollmentFinalizer))
	assert.NotEmpty(t, enrollment.Status.TokenID)
	assert.Equal(t, int64(1), enrollment.Status.ObservedGeneration)
	require.NotNil(t, enrollment.Status.ExpiresAt)
	require.NotNil(t, enrollment.Status.CredentialSecretRef)
	assert.Equal(t, testEnrollmentName+infrav1.CredentialSecretNameSuffix, enrollment.Status.CredentialSecretRef.Name)
	assert.True(t, conditions.IsTrue(enrollment, infrav1.CredentialReady))

	// Consumed stays undriven until consumption detection lands.
	assert.Nil(t, conditions.Get(enrollment, infrav1.Consumed))

	tokenSecret, err := getTokenSecret(t, c, enrollment.Status.TokenID)
	require.NoError(t, err)
	assert.Equal(t, bootstrapapi.SecretTypeBootstrapToken, tokenSecret.Type)
	assert.Equal(t, enrollment.Status.TokenID, string(tokenSecret.Data[bootstrapapi.BootstrapTokenIDKey]))
	assert.Equal(t, infrav1.BootstrapTokenExtraGroups, string(tokenSecret.Data[bootstrapapi.BootstrapTokenExtraGroupsKey]))
	assert.Equal(t, "true", string(tokenSecret.Data[bootstrapapi.BootstrapTokenUsageAuthentication]))
	assert.NotContains(t, tokenSecret.Data, bootstrapapi.BootstrapTokenUsageSigningKey)
	assert.Contains(t, string(tokenSecret.Data[bootstrapapi.BootstrapTokenDescriptionKey]), testEnrollmentName)
	assert.NotEmpty(t, tokenSecret.Labels[infrav1.EnrollmentLabel])

	credentialSecret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: testEnrollmentNamespace, Name: enrollment.Status.CredentialSecretRef.Name}
	err = c.Get(context.Background(), key, credentialSecret)
	require.NoError(t, err)
	assert.Equal(t, infrav1.CredentialSecretType, credentialSecret.Type)
	assert.Equal(t, testEnrollmentName, string(credentialSecret.Data[infrav1.CredentialSecretHostNameKey]))

	require.Len(t, credentialSecret.OwnerReferences, 1)
	owner := credentialSecret.OwnerReferences[0]
	assert.Equal(t, "ByoHostEnrollment", owner.Kind)
	assert.Equal(t, testEnrollmentName, owner.Name)
	require.NotNil(t, owner.Controller)
	assert.True(t, *owner.Controller)

	// The rendered kubeconfig must be loadable and must carry the token.
	config, err := clientcmd.Load(credentialSecret.Data[infrav1.CredentialSecretKubeconfigKey])
	require.NoError(t, err)
	assert.Equal(t, testAPIServerURL, config.Clusters[infrav1.DefaultClusterName].Server)
	assert.Equal(t, testTLSServerName, config.Clusters[infrav1.DefaultClusterName].TLSServerName)
	assert.True(t, strings.HasPrefix(config.AuthInfos[infrav1.DefaultAuth].Token, enrollment.Status.TokenID+"."))
}

func TestReconcileMissingTransportConfigMap(t *testing.T) {
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
	assert.Empty(t, secrets.Items, "no credential may be minted without a transport")
}

func TestReconcileMalformedTransportConfigMap(t *testing.T) {
	testCases := []struct {
		name string
		data map[string]string
	}{
		{
			name: "no api server url",
			data: map[string]string{TransportConfigMapCADataKey: "ca"},
		},
		{
			name: "api server url is not https",
			data: map[string]string{
				TransportConfigMapAPIServerURLKey: "http://vcluster-cp.example.test:443",
				TransportConfigMapCADataKey:       "ca",
			},
		},
		{
			name: "api server url has no host",
			data: map[string]string{
				TransportConfigMapAPIServerURLKey: "https://",
				TransportConfigMapCADataKey:       "ca",
			},
		},
		{
			name: "no ca data",
			data: map[string]string{TransportConfigMapAPIServerURLKey: testAPIServerURL},
		},
		{
			name: "ca data is not pem",
			data: map[string]string{
				TransportConfigMapAPIServerURLKey: testAPIServerURL,
				TransportConfigMapCADataKey:       "not a certificate",
			},
		},
		{
			name: "ca data is pem but not a certificate",
			data: map[string]string{
				TransportConfigMapAPIServerURLKey: testAPIServerURL,
				TransportConfigMapCADataKey:       "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      DefaultTransportConfigMapName,
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

func TestReconcileIsIdempotent(t *testing.T) {
	r, c := newTestReconciler(t, testEnrollment(), testTransportConfigMap(t))

	_, err := r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	first := getEnrollment(t, c)

	_, err = r.Reconcile(context.Background(), enrollmentRequest())
	require.NoError(t, err)
	second := getEnrollment(t, c)

	assert.Equal(t, first.Status.TokenID, second.Status.TokenID, "a second pass must not mint a second token")

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
			r, c := newTestReconciler(t, testEnrollment(), testTransportConfigMap(t))

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

			// And every minted token carries an expiration.
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
			r, c := newTestReconciler(t, testEnrollment(), testTransportConfigMap(t))

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
			r, c := newTestReconciler(t, testEnrollment(), testTransportConfigMap(t))

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

func TestUsableToken(t *testing.T) {
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
		name   string
		secret *corev1.Secret
		wantOK bool
	}{
		{
			name:   "live token",
			secret: secretWith(nil),
			wantOK: true,
		},
		{
			name: "no expiration",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenExpirationKey)
			}),
			wantOK: false,
		},
		{
			name: "unparseable expiration",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte("tomorrow")
			}),
			wantOK: false,
		},
		{
			name: "expired",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(now.Add(-time.Minute).Format(time.RFC3339))
			}),
			wantOK: false,
		},
		{
			name: "expires inside the renewal margin",
			secret: secretWith(func(data map[string][]byte) {
				data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(now.Add(time.Second).Format(time.RFC3339))
			}),
			wantOK: false,
		},
		{
			name: "no token id",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenIDKey)
			}),
			wantOK: false,
		},
		{
			name: "no token secret",
			secret: secretWith(func(data map[string][]byte) {
				delete(data, bootstrapapi.BootstrapTokenSecretKey)
			}),
			wantOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tokenStr, expiresAt, ok := usableToken(tc.secret, now)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				assert.Empty(t, tokenStr)
				return
			}
			assert.Equal(t, "abcdef.0123456789abcdef", tokenStr)
			assert.True(t, expiresAt.After(now))
		})
	}
}

// FIXME CLAUDE: Rewrite with skill:writing-tests. Table driven tests.
func TestParseTransportConfig(t *testing.T) {
	caPEM := testCAPEM(t)

	t.Run("a complete config map parses", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			Data: map[string]string{
				TransportConfigMapAPIServerURLKey:  testAPIServerURL,
				TransportConfigMapTLSServerNameKey: testTLSServerName,
				TransportConfigMapCADataKey:        caPEM,
			},
		}
		transport, err := parseTransportConfig(configMap)
		require.NoError(t, err)
		assert.Equal(t, testAPIServerURL, transport.apiServerURL)
		assert.Equal(t, testTLSServerName, transport.tlsServerName)
		assert.Equal(t, []byte(caPEM), transport.caData)
	})

	t.Run("tls server name is optional", func(t *testing.T) {
		configMap := &corev1.ConfigMap{
			Data: map[string]string{
				TransportConfigMapAPIServerURLKey: testAPIServerURL,
				TransportConfigMapCADataKey:       caPEM,
			},
		}
		transport, err := parseTransportConfig(configMap)
		require.NoError(t, err)
		assert.Empty(t, transport.tlsServerName)
	})
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
			err := validateCABundle([]byte(tc.data))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRenderBootstrapKubeconfig(t *testing.T) {
	transport := &transportConfig{
		apiServerURL:  testAPIServerURL,
		tlsServerName: testTLSServerName,
		caData:        []byte(testCAPEM(t)),
	}

	encoded, err := renderBootstrapKubeconfig(transport, "abcdef.0123456789abcdef")
	require.NoError(t, err)

	config, err := clientcmd.Load(encoded)
	require.NoError(t, err)
	assert.Equal(t, infrav1.DefaultContext, config.CurrentContext)
	assert.Equal(t, transport.caData, config.Clusters[infrav1.DefaultClusterName].CertificateAuthorityData)
	assert.False(t, config.Clusters[infrav1.DefaultClusterName].InsecureSkipTLSVerify)
	assert.Equal(t, "abcdef.0123456789abcdef", config.AuthInfos[infrav1.DefaultAuth].Token)
	assert.Equal(t, infrav1.DefaultNamespace, config.Contexts[infrav1.DefaultContext].Namespace)
}

func TestBuildCredentialSecret(t *testing.T) {
	enrollment := testEnrollment()
	secret := buildCredentialSecret(enrollment, testEnrollmentName, []byte("kubeconfig-bytes"))

	assert.Equal(t, testEnrollmentName+infrav1.CredentialSecretNameSuffix, secret.Name)
	assert.Equal(t, testEnrollmentNamespace, secret.Namespace)
	assert.Equal(t, infrav1.CredentialSecretType, secret.Type)
	assert.Equal(t, []byte("kubeconfig-bytes"), secret.Data[infrav1.CredentialSecretKubeconfigKey])
	assert.Equal(t, []byte(testEnrollmentName), secret.Data[infrav1.CredentialSecretHostNameKey])

	require.Len(t, secret.OwnerReferences, 1)
	owner := secret.OwnerReferences[0]
	assert.Equal(t, enrollment.UID, owner.UID)
	assert.Equal(t, infrav1.GroupVersion.String(), owner.APIVersion)
	require.NotNil(t, owner.Controller)
	assert.True(t, *owner.Controller)
}
