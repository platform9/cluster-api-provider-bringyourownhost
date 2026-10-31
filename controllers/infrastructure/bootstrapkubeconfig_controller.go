// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"strings"
	"time"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/client-go/tools/record"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// credentialSecretSuffix is appended to the object name to name the Secret
	// holding its credential.
	credentialSecretSuffix = "-cred"

	// credentialSecretKubeconfigKey is the key in the credential Secret that
	// holds the rendered kubeconfig.
	credentialSecretKubeconfigKey = "kubeconfig"

	// kaapiConfigName is the name of the ConfigMap, in the customer namespace,
	// that names the customer's FQDN.
	kaapiConfigName = "kaapi-config"

	// kaapiConfigFQDNKey is the key in the kaapi-config ConfigMap that holds
	// the customer FQDN.
	kaapiConfigFQDNKey = "customerFqdn"

	// customerFQDNPlaceholder marks where the customer FQDN goes in the
	// apiserver host template.
	customerFQDNPlaceholder = "{customerFqdn}"

	// apiServerPort is the port of the derived apiserver URL.
	apiServerPort = 443

	// rootCAConfigMapName is the name of the ConfigMap, in every namespace,
	// that holds the cluster's root CA.
	rootCAConfigMapName = "kube-root-ca.crt"

	// rootCAConfigMapKey is the key in that ConfigMap holding the PEM CA.
	rootCAConfigMapKey = "ca.crt"

	// endpointRetryInterval is how long to wait before resolving the apiserver
	// endpoint again after a failure.
	endpointRetryInterval = 30 * time.Second

	// endpointFailedReason is the Event reason recorded when the apiserver
	// endpoint cannot be resolved.
	endpointFailedReason = "EndpointResolutionFailed"
)

// BootstrapKubeconfigReconciler reconciles a BootstrapKubeconfig object
type BootstrapKubeconfigReconciler struct {
	client.Client

	// APIReader reads ConfigMaps and token Secrets straight from the API
	// server, so the manager does not start a cluster-wide informer for them.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder

	// APIServerURL, when set, is the apiserver URL for every object that does
	// not name one in its spec.
	APIServerURL string

	// APIServerHostTemplate is the apiserver host for derived URLs, with
	// {customerFqdn} standing for the customer FQDN.
	APIServerHostTemplate string
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;create
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BootstrapKubeconfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{}
	err := r.Get(ctx, req.NamespacedName, bootstrapKubeconfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// The credential reference is written last, so its presence means the
	// credential is complete.
	if bootstrapKubeconfig.Status.CredentialSecretRef != "" {
		return ctrl.Result{}, nil
	}

	// Resolve the endpoint before issuing a token, so a failure leaves no
	// token behind.
	apiServer, caData, err := r.resolveEndpoint(ctx, bootstrapKubeconfig)
	if err != nil {
		logger.Error(err, "Cannot resolve the apiserver endpoint, will retry", "retryAfter", endpointRetryInterval)
		r.Recorder.Event(bootstrapKubeconfig, corev1.EventTypeWarning, endpointFailedReason, err.Error())
		return ctrl.Result{RequeueAfter: endpointRetryInterval}, nil
	}

	tokenStr, expiresAt, err := r.issueToken(ctx, bootstrapKubeconfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The rendered copy carries the derived values. The object itself is never
	// written with them.
	resolved := bootstrapKubeconfig.DeepCopy()
	resolved.Spec.APIServer = apiServer
	resolved.Spec.CertificateAuthorityData = caData
	renderedKubeconfig, err := renderBootstrapKubeconfig(tokenStr, resolved)
	if err != nil {
		return ctrl.Result{}, err
	}

	credentialSecret, err := r.credentialSecret(bootstrapKubeconfig, renderedKubeconfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.Create(ctx, credentialSecret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create the credential secret for %q: %w", bootstrapKubeconfig.Name, err)
	}

	helper, err := patch.NewHelper(bootstrapKubeconfig, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build a patch helper for %q: %w", bootstrapKubeconfig.Name, err)
	}

	renderedKubeconfigStr := string(renderedKubeconfig)
	bootstrapKubeconfig.Status.BootstrapKubeconfigData = &renderedKubeconfigStr //nolint: staticcheck // SA1019: deprecated field, readers go away with the controller changes
	bootstrapKubeconfig.Status.CredentialSecretRef = credentialSecret.Name
	tokenExpiresAt := metav1.NewTime(expiresAt)
	bootstrapKubeconfig.Status.TokenExpiresAt = &tokenExpiresAt

	return ctrl.Result{}, helper.Patch(ctx, bootstrapKubeconfig)
}

// issueToken returns the bootstrap token for the object and when it expires.
// It records the token ID in the object status before creating anything, so a
// crash after the token Secret exists leaves a way to find it again. A token
// ID already in the status means an earlier attempt got that far: the token
// Secret is reused if it still exists, otherwise a new secret half is issued
// under the same ID.
func (r *BootstrapKubeconfigReconciler) issueToken(ctx context.Context, bootstrapKubeconfig *infrastructurev1beta1.BootstrapKubeconfig) (string, time.Time, error) {
	tokenID := bootstrapKubeconfig.Status.TokenID
	if tokenID != "" {
		existing := &corev1.Secret{}
		key := types.NamespacedName{Namespace: metav1.NamespaceSystem, Name: bootstraputil.BootstrapTokenSecretName(tokenID)}
		err := r.APIReader.Get(ctx, key, existing)
		if err == nil {
			return tokenFromSecret(tokenID, existing)
		}
		if !apierrors.IsNotFound(err) {
			return "", time.Time{}, fmt.Errorf("get the token secret %q: %w", key.Name, err)
		}
	}

	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate a bootstrap token for %q: %w", bootstrapKubeconfig.Name, err)
	}

	newTokenID, tokenSecret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tokenStr)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read the generated bootstrap token for %q: %w", bootstrapKubeconfig.Name, err)
	}

	if tokenID == "" {
		helper, helperErr := patch.NewHelper(bootstrapKubeconfig, r.Client)
		if helperErr != nil {
			return "", time.Time{}, fmt.Errorf("build a patch helper for %q: %w", bootstrapKubeconfig.Name, helperErr)
		}
		bootstrapKubeconfig.Status.TokenID = newTokenID
		err = helper.Patch(ctx, bootstrapKubeconfig)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("record the token ID for %q: %w", bootstrapKubeconfig.Name, err)
		}
		tokenID = newTokenID
	}

	tokenStr = tokenID + "." + tokenSecret
	// The token Secret stores the expiry in RFC 3339, which has second
	// precision. Truncating here keeps the status equal to what the Secret says.
	expiresAt := time.Now().UTC().Add(infrastructurev1beta1.DefaultTokenExpiry).Truncate(time.Second)
	tokenSecretObj, err := bootstraptoken.GenerateSecretFromBootstrapToken(tokenStr, expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build the token secret for %q: %w", bootstrapKubeconfig.Name, err)
	}

	err = r.Create(ctx, tokenSecretObj)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", time.Time{}, fmt.Errorf("create the token secret for %q: %w", bootstrapKubeconfig.Name, err)
	}

	return tokenStr, expiresAt, nil
}

// tokenFromSecret reads the token string and expiry back out of an existing
// bootstrap token Secret.
func tokenFromSecret(tokenID string, secret *corev1.Secret) (string, time.Time, error) {
	tokenSecret := string(secret.Data[bootstrapapi.BootstrapTokenSecretKey])
	if tokenSecret == "" {
		return "", time.Time{}, fmt.Errorf("token secret %q has no %q", secret.Name, bootstrapapi.BootstrapTokenSecretKey)
	}

	expiration := string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey])
	expiresAt, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse the expiration %q of token secret %q: %w", expiration, secret.Name, err)
	}

	return tokenID + "." + tokenSecret, expiresAt, nil
}

// resolveEndpoint returns the apiserver URL and the base64 encoded CA the
// rendered kubeconfig should use. A value in the spec wins. Otherwise the URL
// is the controller override or derived from the namespace, and the CA is the
// cluster root CA.
func (r *BootstrapKubeconfigReconciler) resolveEndpoint(ctx context.Context, bootstrapKubeconfig *infrastructurev1beta1.BootstrapKubeconfig) (apiServer, caData string, err error) {
	apiServer = bootstrapKubeconfig.Spec.APIServer
	if apiServer == "" {
		apiServer = r.APIServerURL
	}
	if apiServer == "" {
		apiServer, err = r.deriveAPIServerURL(ctx, bootstrapKubeconfig.Namespace)
		if err != nil {
			return "", "", err
		}
	}

	caData = bootstrapKubeconfig.Spec.CertificateAuthorityData
	if caData == "" {
		rootCA := &corev1.ConfigMap{}
		key := types.NamespacedName{Namespace: metav1.NamespaceSystem, Name: rootCAConfigMapName}
		err = r.APIReader.Get(ctx, key, rootCA)
		if err != nil {
			return "", "", fmt.Errorf("get the cluster CA from configmap %q: %w", key, err)
		}
		pem := rootCA.Data[rootCAConfigMapKey]
		if pem == "" {
			return "", "", fmt.Errorf("configmap %q has no %q", key, rootCAConfigMapKey)
		}
		caData = b64.StdEncoding.EncodeToString([]byte(pem))
	}

	return apiServer, caData, nil
}

// deriveAPIServerURL builds the apiserver URL from the customer FQDN. The
// customer namespace is the longest proper prefix of the namespace, cut at
// dashes, that holds a kaapi-config ConfigMap. The namespace itself is never a
// candidate, so a tenant cannot choose the URL with a ConfigMap of its own.
func (r *BootstrapKubeconfigReconciler) deriveAPIServerURL(ctx context.Context, namespace string) (string, error) {
	segments := strings.Split(namespace, "-")
	for n := len(segments) - 1; n >= 1; n-- {
		customerNamespace := strings.Join(segments[:n], "-")
		config := &corev1.ConfigMap{}
		err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: customerNamespace, Name: kaapiConfigName}, config)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("get configmap %q in namespace %q: %w", kaapiConfigName, customerNamespace, err)
		}

		fqdn := config.Data[kaapiConfigFQDNKey]
		if fqdn == "" {
			return "", fmt.Errorf("configmap %q in namespace %q has no %q", kaapiConfigName, customerNamespace, kaapiConfigFQDNKey)
		}

		host := strings.ReplaceAll(r.APIServerHostTemplate, customerFQDNPlaceholder, fqdn)
		return fmt.Sprintf("%s://%s:%d", infrastructurev1beta1.APIServerURLScheme, host, apiServerPort), nil
	}

	return "", fmt.Errorf("no configmap %q found for namespace %q or any dash-separated prefix of it", kaapiConfigName, namespace)
}

// renderBootstrapKubeconfig turns a bootstrap token into the kubeconfig bytes a
// host uses to request its certificate. The CA data arrives base64 encoded and
// is decoded here, because the encoder encodes it again on the way out.
func renderBootstrapKubeconfig(tokenStr string, bootstrapKubeconfig *infrastructurev1beta1.BootstrapKubeconfig) ([]byte, error) {
	bootstrapKubeconfigData, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken(tokenStr, bootstrapKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("render the kubeconfig for %q: %w", bootstrapKubeconfig.Name, err)
	}

	caData := bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData
	decodedCAData, err := b64.StdEncoding.DecodeString(string(caData))
	if err != nil {
		return nil, fmt.Errorf("decode the CA data for %q: %w", bootstrapKubeconfig.Name, err)
	}

	bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData = decodedCAData
	encoded, err := runtime.Encode(clientcmdlatest.Codec, bootstrapKubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("encode the kubeconfig for %q: %w", bootstrapKubeconfig.Name, err)
	}

	return encoded, nil
}

// credentialSecret builds the Secret holding this object's credential. The
// BootstrapKubeconfig owns it, so deleting the object deletes the credential
// with it.
func (r *BootstrapKubeconfigReconciler) credentialSecret(bootstrapKubeconfig *infrastructurev1beta1.BootstrapKubeconfig, kubeconfig []byte) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootstrapKubeconfig.Name + credentialSecretSuffix,
			Namespace: bootstrapKubeconfig.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			credentialSecretKubeconfigKey: kubeconfig,
		},
	}

	err := controllerutil.SetControllerReference(bootstrapKubeconfig, secret, r.Scheme)
	if err != nil {
		return nil, fmt.Errorf("set the owner of the credential secret for %q: %w", bootstrapKubeconfig.Name, err)
	}

	return secret, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootstrapKubeconfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.BootstrapKubeconfig{}).
		Complete(r)
}
