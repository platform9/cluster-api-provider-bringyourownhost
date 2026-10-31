// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	b64 "encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
)

const (
	bkTestNamespace    = "dg-d-default-service"
	bkTestName         = "host-a1b2c"
	bkTestHostTemplate = "vcluster-cp.k8s.{customerFqdn}"
	bkTestFQDN         = "dg-d.platform9.horse"
	bkTestServer       = "https://vcluster-cp.k8s.dg-d.platform9.horse:443"
	bkTestRootCA       = "-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----\n"
	bkTestSpecCA       = "spec-ca-pem"
	bkTestTokenID      = "abcdef"
	bkTestTokenSecret  = "0123456789abcdef"
	bkTestTokenPrefix  = "bootstrap-token-"
)

// bkHarness bundles a reconciler with the fake API server and event recorder
// behind it.
type bkHarness struct {
	reconciler *controllers.BootstrapKubeconfigReconciler
	client     client.Client
	events     *record.FakeRecorder

	// key is the BootstrapKubeconfig among the objects the harness was built
	// with.
	key types.NamespacedName
}

// newBKHarness builds a reconciler over a fake API server holding objs.
// apiServerURL is the controller-wide override, and funcs intercepts calls on
// the fake client.
func newBKHarness(t *testing.T, apiServerURL string, funcs *interceptor.Funcs, objs ...client.Object) *bkHarness {
	t.Helper()

	testScheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(testScheme))
	require.NoError(t, infrastructurev1beta1.AddToScheme(testScheme))

	builder := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objs...).
		WithStatusSubresource(&infrastructurev1beta1.BootstrapKubeconfig{})
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	fakeClient := builder.Build()

	var key types.NamespacedName
	for _, obj := range objs {
		if _, ok := obj.(*infrastructurev1beta1.BootstrapKubeconfig); ok {
			key = client.ObjectKeyFromObject(obj)
		}
	}

	events := record.NewFakeRecorder(10)
	return &bkHarness{
		key: key,
		reconciler: &controllers.BootstrapKubeconfigReconciler{
			Client:                fakeClient,
			APIReader:             fakeClient,
			Scheme:                testScheme,
			Recorder:              events,
			APIServerURL:          apiServerURL,
			APIServerHostTemplate: bkTestHostTemplate,
		},
		client: fakeClient,
		events: events,
	}
}

// reconcile runs one reconcile for the test object.
func (h *bkHarness) reconcile(t *testing.T) (ctrl.Result, error) {
	t.Helper()

	return h.reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: h.key,
	})
}

// getObject returns the test object as the fake API server holds it.
func (h *bkHarness) getObject(t *testing.T) *infrastructurev1beta1.BootstrapKubeconfig {
	t.Helper()

	got := &infrastructurev1beta1.BootstrapKubeconfig{}
	err := h.client.Get(context.Background(), h.key, got)
	require.NoError(t, err)
	return got
}

// tokenSecrets returns the bootstrap token Secrets in kube-system.
func (h *bkHarness) tokenSecrets(t *testing.T) []corev1.Secret {
	t.Helper()

	secrets := &corev1.SecretList{}
	err := h.client.List(context.Background(), secrets, client.InNamespace(metav1.NamespaceSystem))
	require.NoError(t, err)

	var tokens []corev1.Secret
	for i := range secrets.Items {
		if strings.HasPrefix(secrets.Items[i].Name, bkTestTokenPrefix) {
			tokens = append(tokens, secrets.Items[i])
		}
	}
	return tokens
}

// credentialKubeconfig returns the kubeconfig held in the credential Secret
// of the test object.
func (h *bkHarness) credentialKubeconfig(t *testing.T) []byte {
	t.Helper()

	credential := &corev1.Secret{}
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: h.key.Namespace, Name: h.key.Name + "-cred"}, credential)
	require.NoError(t, err)
	return credential.Data["kubeconfig"]
}

func newBKObject(spec infrastructurev1beta1.BootstrapKubeconfigSpec, status infrastructurev1beta1.BootstrapKubeconfigStatus) *infrastructurev1beta1.BootstrapKubeconfig {
	spec.HostName = "host-a"
	return &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: bkTestNamespace,
			Name:      bkTestName,
			UID:       types.UID("bk-uid"),
		},
		Spec:   spec,
		Status: status,
	}
}

func newBKConfigMap(namespace, name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
	}
}

func newBKKaapiConfig(namespace, fqdn string) *corev1.ConfigMap {
	return newBKConfigMap(namespace, "kaapi-config", map[string]string{"customerFqdn": fqdn})
}

func newBKRootCA() *corev1.ConfigMap {
	return newBKConfigMap(metav1.NamespaceSystem, "kube-root-ca.crt", map[string]string{"ca.crt": bkTestRootCA})
}

// newBKTokenSecret builds the kube-system token Secret of an earlier attempt.
func newBKTokenSecret(t *testing.T, expiresAt time.Time) *corev1.Secret {
	t.Helper()

	secret, err := bootstraptoken.GenerateSecretFromBootstrapToken(bkTestTokenID+"."+bkTestTokenSecret, expiresAt)
	require.NoError(t, err)
	return secret
}

func TestBootstrapKubeconfigReconciler_Endpoint(t *testing.T) {
	specCA := b64.StdEncoding.EncodeToString([]byte(bkTestSpecCA))

	testCases := []struct {
		name         string
		namespace    string
		spec         infrastructurev1beta1.BootstrapKubeconfigSpec
		apiServerURL string
		objects      []client.Object
		wantServer   string
		wantCA       string
	}{
		{
			name:       "derive the URL from the customer namespace",
			namespace:  "dg-d-default-service",
			objects:    []client.Object{newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA()},
			wantServer: bkTestServer,
			wantCA:     bkTestRootCA,
		},
		{
			name:      "longest prefix wins",
			namespace: "dg-d-default-service",
			objects: []client.Object{
				newBKKaapiConfig("dg", "other.example.com"),
				newBKKaapiConfig("dg-d", bkTestFQDN),
				newBKRootCA(),
			},
			wantServer: bkTestServer,
			wantCA:     bkTestRootCA,
		},
		{
			name:      "sibling tenants and partial segments are ignored",
			namespace: "dg-d-default-dev",
			objects: []client.Object{
				newBKKaapiConfig("dg-d", bkTestFQDN),
				newBKKaapiConfig("dg-d-default-service", "service.example.com"),
				newBKKaapiConfig("dg-d-default-admin", "admin.example.com"),
				newBKKaapiConfig("dg-d-default-de", "partial.example.com"),
				newBKRootCA(),
			},
			wantServer: bkTestServer,
			wantCA:     bkTestRootCA,
		},
		{
			name:       "ID-based namespace",
			namespace:  "dg-d-1a2b3c4d",
			objects:    []client.Object{newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA()},
			wantServer: bkTestServer,
			wantCA:     bkTestRootCA,
		},
		{
			name:         "override flag skips the derivation",
			namespace:    "dg-d-default-service",
			apiServerURL: "https://override.example.com:6443",
			objects:      []client.Object{newBKRootCA()},
			wantServer:   "https://override.example.com:6443",
			wantCA:       bkTestRootCA,
		},
		{
			name:      "spec wins and no ConfigMap is needed",
			namespace: "dg-d-default-service",
			spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
				APIServer:                "https://spec.example.com:6443",
				CertificateAuthorityData: specCA,
			},
			apiServerURL: "https://override.example.com:6443",
			wantServer:   "https://spec.example.com:6443",
			wantCA:       bkTestSpecCA,
		},
		{
			name:      "spec CA wins over the cluster CA",
			namespace: "dg-d-default-service",
			spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
				CertificateAuthorityData: specCA,
			},
			objects:    []client.Object{newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA()},
			wantServer: bkTestServer,
			wantCA:     bkTestSpecCA,
		},
		{
			name:      "CA from the cluster when the spec has none",
			namespace: "dg-d-default-service",
			spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
				APIServer: "https://spec.example.com:6443",
			},
			objects:    []client.Object{newBKRootCA()},
			wantServer: "https://spec.example.com:6443",
			wantCA:     bkTestRootCA,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bk := newBKObject(tc.spec, infrastructurev1beta1.BootstrapKubeconfigStatus{})
			bk.Namespace = tc.namespace
			h := newBKHarness(t, tc.apiServerURL, nil, append(tc.objects, bk)...)

			result, err := h.reconcile(t)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)

			kubeconfig, err := clientcmd.Load(h.credentialKubeconfig(t))
			require.NoError(t, err)
			cluster := kubeconfig.Clusters[infrastructurev1beta1.DefaultClusterName]
			require.NotNil(t, cluster)
			assert.Equal(t, tc.wantServer, cluster.Server)
			assert.Equal(t, tc.wantCA, string(cluster.CertificateAuthorityData))
		})
	}
}

func TestBootstrapKubeconfigReconciler_EndpointFailure(t *testing.T) {
	testCases := []struct {
		name        string
		objects     []client.Object
		wantMessage string
	}{
		{
			name:        "no kaapi-config in any prefix",
			objects:     []client.Object{newBKRootCA()},
			wantMessage: `no configmap "kaapi-config" found for namespace "dg-d-default-service"`,
		},
		{
			name:        "kaapi-config only in the object's own namespace",
			objects:     []client.Object{newBKKaapiConfig(bkTestNamespace, "tenant.example.com"), newBKRootCA()},
			wantMessage: `no configmap "kaapi-config" found for namespace "dg-d-default-service"`,
		},
		{
			name: "kaapi-config without customerFqdn",
			objects: []client.Object{
				newBKConfigMap("dg-d", "kaapi-config", map[string]string{"other": "value"}),
				newBKRootCA(),
			},
			wantMessage: `has no "customerFqdn"`,
		},
		{
			name:        "no cluster CA",
			objects:     []client.Object{newBKKaapiConfig("dg-d", bkTestFQDN)},
			wantMessage: "kube-root-ca.crt",
		},
		{
			name: "cluster CA ConfigMap without ca.crt",
			objects: []client.Object{
				newBKKaapiConfig("dg-d", bkTestFQDN),
				newBKConfigMap(metav1.NamespaceSystem, "kube-root-ca.crt", map[string]string{"other": "value"}),
			},
			wantMessage: `has no "ca.crt"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, infrastructurev1beta1.BootstrapKubeconfigStatus{})
			h := newBKHarness(t, "", nil, append(tc.objects, bk)...)

			result, err := h.reconcile(t)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result)

			assert.Empty(t, h.tokenSecrets(t))
			got := h.getObject(t)
			assert.Equal(t, infrastructurev1beta1.BootstrapKubeconfigStatus{}, got.Status)

			require.Len(t, h.events.Events, 1)
			event := <-h.events.Events
			assert.Contains(t, event, "Warning EndpointResolutionFailed")
			assert.Contains(t, event, tc.wantMessage)
		})
	}
}

func TestBootstrapKubeconfigReconciler_FreshObject(t *testing.T) {
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, infrastructurev1beta1.BootstrapKubeconfigStatus{})
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), bk)

	result, err := h.reconcile(t)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	tokens := h.tokenSecrets(t)
	require.Len(t, tokens, 1)
	tokenID := string(tokens[0].Data["token-id"])
	tokenExpiration := string(tokens[0].Data["expiration"])

	credential := &corev1.Secret{}
	err = h.client.Get(context.Background(), types.NamespacedName{Namespace: h.key.Namespace, Name: h.key.Name + "-cred"}, credential)
	require.NoError(t, err)
	wantOwner := metav1.OwnerReference{
		APIVersion:         infrastructurev1beta1.GroupVersion.String(),
		Kind:               "BootstrapKubeconfig",
		Name:               bkTestName,
		UID:                types.UID("bk-uid"),
		Controller:         new(true),
		BlockOwnerDeletion: new(true),
	}
	assert.Equal(t, []metav1.OwnerReference{wantOwner}, credential.OwnerReferences)
	assert.Equal(t, corev1.SecretTypeOpaque, credential.Type)
	assert.Contains(t, string(credential.Data["kubeconfig"]), tokenID+"."+string(tokens[0].Data["token-secret"]))

	got := h.getObject(t)
	require.NotNil(t, got.Status.TokenExpiresAt)
	assert.Equal(t, tokenExpiration, got.Status.TokenExpiresAt.UTC().Format(time.RFC3339))
	assert.WithinDuration(t, time.Now().Add(infrastructurev1beta1.DefaultTokenExpiry), got.Status.TokenExpiresAt.Time, time.Minute)

	wantKubeconfig := string(credential.Data["kubeconfig"])
	wantStatus := infrastructurev1beta1.BootstrapKubeconfigStatus{
		BootstrapKubeconfigData: &wantKubeconfig, //nolint: staticcheck // SA1019: deprecated field, still written until its readers go away
		TokenID:                 tokenID,
		CredentialSecretRef:     bkTestName + "-cred",
		TokenExpiresAt:          got.Status.TokenExpiresAt,
	}
	assert.Equal(t, wantStatus, got.Status)

	// Derived values never reach the spec.
	assert.Equal(t, infrastructurev1beta1.BootstrapKubeconfigSpec{HostName: "host-a"}, got.Spec)
}

func TestBootstrapKubeconfigReconciler_SecondReconcile(t *testing.T) {
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, infrastructurev1beta1.BootstrapKubeconfigStatus{})
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), bk)

	_, err := h.reconcile(t)
	require.NoError(t, err)
	before := h.getObject(t)
	tokensBefore := h.tokenSecrets(t)

	result, err := h.reconcile(t)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	assert.Equal(t, before, h.getObject(t))
	assert.Equal(t, tokensBefore, h.tokenSecrets(t))
}

func TestBootstrapKubeconfigReconciler_CredentialRefSkipsReconcile(t *testing.T) {
	status := infrastructurev1beta1.BootstrapKubeconfigStatus{CredentialSecretRef: "issued-elsewhere"}
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, status)
	h := newBKHarness(t, "", nil, bk)

	result, err := h.reconcile(t)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	assert.Empty(t, h.tokenSecrets(t))
	assert.Empty(t, h.events.Events)
	assert.Equal(t, status, h.getObject(t).Status)
}

func TestBootstrapKubeconfigReconciler_DeprecatedDataDoesNotSkipReconcile(t *testing.T) {
	existing := "already present"
	status := infrastructurev1beta1.BootstrapKubeconfigStatus{BootstrapKubeconfigData: &existing} //nolint: staticcheck // SA1019: deprecated field, the guard on it is gone
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, status)
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), bk)

	_, err := h.reconcile(t)
	require.NoError(t, err)

	assert.Len(t, h.tokenSecrets(t), 1)
	assert.Equal(t, bkTestName+"-cred", h.getObject(t).Status.CredentialSecretRef)
}

func TestBootstrapKubeconfigReconciler_TokenIDRecordedFirst(t *testing.T) {
	errCreate := errors.New("create refused")
	funcs := &interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if strings.HasPrefix(obj.GetName(), bkTestTokenPrefix) {
				return errCreate
			}
			return c.Create(ctx, obj, opts...)
		},
	}
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, infrastructurev1beta1.BootstrapKubeconfigStatus{})
	h := newBKHarness(t, "", funcs, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), bk)

	_, err := h.reconcile(t)
	require.ErrorIs(t, err, errCreate)

	got := h.getObject(t)
	assert.NotEmpty(t, got.Status.TokenID)
	assert.Empty(t, got.Status.CredentialSecretRef)
	assert.Empty(t, h.tokenSecrets(t))
}

func TestBootstrapKubeconfigReconciler_ResumeWithExistingToken(t *testing.T) {
	expiresAt := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	status := infrastructurev1beta1.BootstrapKubeconfigStatus{TokenID: bkTestTokenID}
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, status)
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), newBKTokenSecret(t, expiresAt), bk)

	_, err := h.reconcile(t)
	require.NoError(t, err)

	tokens := h.tokenSecrets(t)
	require.Len(t, tokens, 1)
	assert.Equal(t, bkTestTokenSecret, string(tokens[0].Data["token-secret"]))

	kubeconfig, err := clientcmd.Load(h.credentialKubeconfig(t))
	require.NoError(t, err)
	assert.Equal(t, bkTestTokenID+"."+bkTestTokenSecret, kubeconfig.AuthInfos[infrastructurev1beta1.DefaultAuth].Token)

	got := h.getObject(t)
	assert.Equal(t, bkTestTokenID, got.Status.TokenID)
	assert.Equal(t, bkTestName+"-cred", got.Status.CredentialSecretRef)
	require.NotNil(t, got.Status.TokenExpiresAt)
	assert.Equal(t, expiresAt.Format(time.RFC3339), got.Status.TokenExpiresAt.UTC().Format(time.RFC3339))
}

func TestBootstrapKubeconfigReconciler_ResumeWithoutToken(t *testing.T) {
	status := infrastructurev1beta1.BootstrapKubeconfigStatus{TokenID: bkTestTokenID}
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, status)
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), bk)

	_, err := h.reconcile(t)
	require.NoError(t, err)

	tokens := h.tokenSecrets(t)
	require.Len(t, tokens, 1)
	assert.Equal(t, bkTestTokenPrefix+bkTestTokenID, tokens[0].Name)
	assert.Equal(t, bkTestTokenID, string(tokens[0].Data["token-id"]))

	kubeconfig, err := clientcmd.Load(h.credentialKubeconfig(t))
	require.NoError(t, err)
	wantToken := bkTestTokenID + "." + string(tokens[0].Data["token-secret"])
	assert.Equal(t, wantToken, kubeconfig.AuthInfos[infrastructurev1beta1.DefaultAuth].Token)
	assert.Equal(t, bkTestTokenID, h.getObject(t).Status.TokenID)
}

func TestBootstrapKubeconfigReconciler_ExistingCredentialIsTolerated(t *testing.T) {
	credential := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: bkTestNamespace, Name: bkTestName + "-cred"},
		Data:       map[string][]byte{"kubeconfig": []byte("left by an earlier attempt")},
	}
	bk := newBKObject(infrastructurev1beta1.BootstrapKubeconfigSpec{}, infrastructurev1beta1.BootstrapKubeconfigStatus{})
	h := newBKHarness(t, "", nil, newBKKaapiConfig("dg-d", bkTestFQDN), newBKRootCA(), credential, bk)

	_, err := h.reconcile(t)
	require.NoError(t, err)

	got := h.getObject(t)
	assert.Equal(t, bkTestName+"-cred", got.Status.CredentialSecretRef)
	assert.NotNil(t, got.Status.TokenExpiresAt)
	assert.Equal(t, "left by an earlier attempt", string(h.credentialKubeconfig(t)))
}
