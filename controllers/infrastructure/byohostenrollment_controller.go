// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

const (
	// DefaultTransportConfigMapName is the name of the ConfigMap describing how
	// a BYO host reaches the management cluster's API server. It is one object
	// for the whole deployment: the caller creating an enrollment cannot know
	// the API server URL, the SNI name or the CA, and must not be trusted to
	// supply them.
	//
	// Keys:
	//
	//	apiserverURL             https://<host>:<port>, required. Must be an
	//	                         endpoint that passes TLS through, because a
	//	                         proxy that terminates TLS strips the client
	//	                         certificate the host later presents.
	//	tlsServerName            SNI name to verify the server certificate
	//	                         against, optional. Set it when apiserverURL's
	//	                         host is not in that certificate's SANs.
	//	certificateAuthorityData PEM-encoded CA bundle for the API server,
	//	                         required. Raw PEM, not base64 of PEM.
	DefaultTransportConfigMapName = "byoh-bootstrap-transport"

	// TransportConfigMapAPIServerURLKey names the API server endpoint.
	TransportConfigMapAPIServerURLKey = "apiserverURL"

	// TransportConfigMapTLSServerNameKey names the SNI name to verify against.
	TransportConfigMapTLSServerNameKey = "tlsServerName"

	// TransportConfigMapCADataKey holds the PEM-encoded CA bundle.
	TransportConfigMapCADataKey = "certificateAuthorityData"

	// defaultTokenTTL is used when the enrollment does not set spec.tokenTTL.
	// The CRD defaults that field, so this only covers an object written
	// before the default existed.
	defaultTokenTTL = 30 * time.Minute

	// transportRetryInterval is how long to wait before looking for the
	// transport ConfigMap again. Its absence is a deployment step that has not
	// happened yet, so retrying is worthwhile but not urgent.
	transportRetryInterval = time.Minute

	// tokenRenewalMargin is how much of a token's remaining life is treated as
	// too little to hand to a host. A token about to expire is replaced rather
	// than reused.
	tokenRenewalMargin = time.Minute
)

// ByoHostEnrollmentReconciler mints the bootstrap credential for one BYO host
// and delivers it as a Secret in the enrollment's own namespace.
type ByoHostEnrollmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// TransportConfigMap locates the deployment-level transport description.
	// Defaults to DefaultTransportConfigMapName in kube-system.
	TransportConfigMap types.NamespacedName
}

// transportConfig is the validated content of the transport ConfigMap.
type transportConfig struct {
	apiServerURL  string
	tlsServerName string
	caData        []byte
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile mints and delivers the bootstrap credential for one host.
//
// The write order is load-bearing. The token ID reaches status before the
// token Secret exists, so a reconcile that dies partway leaves a record of the
// token it minted. The next pass finds that token and either reuses it or
// deletes it, instead of minting a second live credential and abandoning the
// first for the rest of its lifetime.
func (r *ByoHostEnrollmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	enrollment := &infrav1.ByoHostEnrollment{}
	if err := r.Get(ctx, req.NamespacedName, enrollment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get ByoHostEnrollment %s: %w", req.NamespacedName, err)
	}

	if !enrollment.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, enrollment)
	}

	// The finalizer is persisted before anything is created, so there is no
	// window in which a token Secret exists with nothing obliged to remove it.
	if !controllerutil.ContainsFinalizer(enrollment, infrav1.EnrollmentFinalizer) {
		if err := r.patchEnrollment(ctx, enrollment, func() {
			controllerutil.AddFinalizer(enrollment, infrav1.EnrollmentFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	return r.reconcileNormal(ctx, enrollment)
}

func (r *ByoHostEnrollmentReconciler) reconcileNormal(ctx context.Context, enrollment *infrav1.ByoHostEnrollment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hostName, err := hostname.Normalize(enrollment.Name)
	if err != nil {
		return ctrl.Result{}, r.markNotReady(ctx, enrollment, infrav1.CredentialMintFailedReason, err.Error())
	}

	transport, err := r.readTransportConfig(ctx)
	if err != nil {
		logger.Info("Transport configuration is unusable, will retry", "error", err.Error())
		if markErr := r.markNotReady(ctx, enrollment, infrav1.TransportUnavailableReason, err.Error()); markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{RequeueAfter: transportRetryInterval}, nil
	}

	tokenStr, expiresAt, err := r.ensureBootstrapToken(ctx, enrollment, hostName)
	if err != nil {
		if markErr := r.markNotReady(ctx, enrollment, infrav1.CredentialMintFailedReason, err.Error()); markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	kubeconfig, err := renderBootstrapKubeconfig(transport, tokenStr)
	if err != nil {
		if markErr := r.markNotReady(ctx, enrollment, infrav1.CredentialMintFailedReason, err.Error()); markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	credentialSecret := buildCredentialSecret(enrollment, hostName, kubeconfig)
	if err = r.createOrUpdateSecret(ctx, credentialSecret); err != nil {
		if markErr := r.markNotReady(ctx, enrollment, infrav1.CredentialMintFailedReason, err.Error()); markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	err = r.patchEnrollment(ctx, enrollment, func() {
		enrollment.Status.CredentialSecretRef = &corev1.LocalObjectReference{Name: credentialSecret.Name}
		enrollment.Status.ExpiresAt = ptr.To(metav1.NewTime(expiresAt))
		enrollment.Status.ObservedGeneration = enrollment.Generation
		conditions.MarkTrue(enrollment, infrav1.CredentialReady)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Bootstrap credential is ready", "secret", credentialSecret.Name, "expiresAt", expiresAt)
	return ctrl.Result{}, nil
}

// reconcileDelete removes the bootstrap token Secret this enrollment minted.
// The credential Secret is owned by the enrollment and cascades on its own; the
// token Secret lives in kube-system, which a namespaced object cannot own.
func (r *ByoHostEnrollmentReconciler) reconcileDelete(ctx context.Context, enrollment *infrav1.ByoHostEnrollment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if enrollment.Status.TokenID != "" {
		tokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstraputil.BootstrapTokenSecretName(enrollment.Status.TokenID),
				Namespace: metav1.NamespaceSystem,
			},
		}
		if err := r.Delete(ctx, tokenSecret); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete bootstrap token Secret %s: %w", tokenSecret.Name, err)
		}
		logger.Info("Bootstrap token Secret removed", "secret", tokenSecret.Name)
	}

	err := r.patchEnrollment(ctx, enrollment, func() {
		controllerutil.RemoveFinalizer(enrollment, infrav1.EnrollmentFinalizer)
	})
	return ctrl.Result{}, err
}

// ensureBootstrapToken returns a live bootstrap token for this enrollment,
// reusing the one status already names when that token still exists and has
// useful life left. It returns the full token string and the moment it expires.
func (r *ByoHostEnrollmentReconciler) ensureBootstrapToken(ctx context.Context, enrollment *infrav1.ByoHostEnrollment, hostName string) (string, time.Time, error) {
	logger := log.FromContext(ctx)
	now := time.Now().UTC()

	if enrollment.Status.TokenID != "" {
		existing := &corev1.Secret{}
		key := client.ObjectKey{
			Namespace: metav1.NamespaceSystem,
			Name:      bootstraputil.BootstrapTokenSecretName(enrollment.Status.TokenID),
		}
		err := r.Get(ctx, key, existing)
		switch {
		case err == nil:
			if tokenStr, expiresAt, ok := usableToken(existing, now); ok {
				return tokenStr, expiresAt, nil
			}
			// The recorded token is expired, malformed, or carries no
			// expiration at all. A token with no expiration never stops
			// working, so it is deleted rather than left behind.
			logger.Info("Discarding unusable bootstrap token Secret", "secret", key.Name)
			if deleteErr := r.Delete(ctx, existing); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return "", time.Time{}, fmt.Errorf("delete unusable bootstrap token Secret %s: %w", key.Name, deleteErr)
			}
		case apierrors.IsNotFound(err):
			// The token ID was recorded but the Secret was never written, or
			// was removed. Minting a replacement strands nothing.
		default:
			return "", time.Time{}, fmt.Errorf("get bootstrap token Secret %s: %w", key.Name, err)
		}
	}

	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate bootstrap token: %w", err)
	}
	tokenID, _, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tokenStr)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("split bootstrap token: %w", err)
	}

	ttl := defaultTokenTTL
	if enrollment.Spec.TokenTTL != nil {
		ttl = enrollment.Spec.TokenTTL.Duration
	}
	expiresAt := now.Add(ttl)

	tokenSecret, err := buildTokenSecret(tokenStr, hostName, infrav1.BootstrapTokenExtraGroups, expiresAt)
	if err != nil {
		return "", time.Time{}, err
	}
	tokenSecret.Labels = map[string]string{
		infrav1.EnrollmentLabel: generateSafeLabelValue(enrollment.Namespace, enrollment.Name),
	}

	// The token ID is recorded before the Secret exists. Losing the process
	// between these two writes leaves a status pointing at a token that was
	// never created, which the next pass simply replaces. The reverse order
	// would leave a live token nothing refers to.
	err = r.patchEnrollment(ctx, enrollment, func() {
		enrollment.Status.TokenID = tokenID
		enrollment.Status.ExpiresAt = ptr.To(metav1.NewTime(expiresAt))
	})
	if err != nil {
		return "", time.Time{}, err
	}

	if err := r.createOrUpdateSecret(ctx, tokenSecret); err != nil {
		return "", time.Time{}, err
	}

	logger.Info("Minted bootstrap token", "tokenID", tokenID, "expiresAt", expiresAt)
	return tokenStr, expiresAt, nil
}

// createOrUpdateSecret creates secret, falling back to an update when a
// previous reconcile already wrote it.
func (r *ByoHostEnrollmentReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	err := r.Create(ctx, secret)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	if err := r.Update(ctx, secret); err != nil {
		return fmt.Errorf("update Secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	return nil
}

// markNotReady records why no usable credential exists yet.
func (r *ByoHostEnrollmentReconciler) markNotReady(ctx context.Context, enrollment *infrav1.ByoHostEnrollment, reason, message string) error {
	return r.patchEnrollment(ctx, enrollment, func() {
		enrollment.Status.ObservedGeneration = enrollment.Generation
		conditions.MarkFalse(enrollment, infrav1.CredentialReady, reason, clusterv1.ConditionSeverityWarning, "%s", message)
	})
}

// patchEnrollment applies mutate to enrollment and persists the result. Each
// staged write takes its own helper so that the order the stages run in is the
// order they reach the API server.
func (r *ByoHostEnrollmentReconciler) patchEnrollment(ctx context.Context, enrollment *infrav1.ByoHostEnrollment, mutate func()) error {
	helper, err := patch.NewHelper(enrollment, r.Client)
	if err != nil {
		return fmt.Errorf("create patch helper for ByoHostEnrollment %s/%s: %w", enrollment.Namespace, enrollment.Name, err)
	}
	mutate()
	// The conditions on a ByoHostEnrollment are set by this controller alone,
	// so a stale snapshot must not turn into a merge conflict.
	options := patch.WithOwnedConditions{
		Conditions: []clusterv1.ConditionType{
			infrav1.CredentialReady,
			infrav1.Consumed,
		},
	}
	if err := helper.Patch(ctx, enrollment, options); err != nil {
		return fmt.Errorf("patch ByoHostEnrollment %s/%s: %w", enrollment.Namespace, enrollment.Name, err)
	}
	return nil
}

// readTransportConfig loads and validates the deployment-level transport
// ConfigMap. A missing or malformed ConfigMap is an error, never a partial
// credential.
func (r *ByoHostEnrollmentReconciler) readTransportConfig(ctx context.Context) (*transportConfig, error) {
	key := r.TransportConfigMap
	if key.Name == "" {
		key.Name = DefaultTransportConfigMapName
	}
	if key.Namespace == "" {
		key.Namespace = metav1.NamespaceSystem
	}

	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, key, configMap); err != nil {
		return nil, fmt.Errorf("get transport ConfigMap %s: %w", key, err)
	}
	return parseTransportConfig(configMap)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostEnrollmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.ByoHostEnrollment{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// parseTransportConfig validates the transport ConfigMap's keys.
func parseTransportConfig(configMap *corev1.ConfigMap) (*transportConfig, error) {
	apiServerURL := configMap.Data[TransportConfigMapAPIServerURLKey]
	if apiServerURL == "" {
		return nil, fmt.Errorf("transport ConfigMap key %q is empty", TransportConfigMapAPIServerURLKey)
	}
	parsed, err := url.Parse(apiServerURL)
	if err != nil {
		return nil, fmt.Errorf("transport ConfigMap key %q is not a URL: %w", TransportConfigMapAPIServerURLKey, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("transport ConfigMap key %q must be an https URL with a host, got %q",
			TransportConfigMapAPIServerURLKey, apiServerURL)
	}

	caData := configMap.Data[TransportConfigMapCADataKey]
	if caData == "" {
		return nil, fmt.Errorf("transport ConfigMap key %q is empty", TransportConfigMapCADataKey)
	}
	if err := validateCABundle([]byte(caData)); err != nil {
		return nil, fmt.Errorf("transport ConfigMap key %q: %w", TransportConfigMapCADataKey, err)
	}

	return &transportConfig{
		apiServerURL:  apiServerURL,
		tlsServerName: configMap.Data[TransportConfigMapTLSServerNameKey],
		caData:        []byte(caData),
	}, nil
}

// validateCABundle checks that data is PEM holding at least one parseable
// certificate. A bundle that only looks like PEM fails on the host, long after
// it was written, as a TLS error with no obvious cause.
func validateCABundle(data []byte) error {
	found := false
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("expected a CERTIFICATE block, got %q", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("parse certificate: %w", err)
		}
		found = true
	}
	if !found {
		return errors.New("no PEM certificate found")
	}
	return nil
}

// buildTokenSecret assembles the bootstrap.kubernetes.io/token Secret the API
// server's bootstrap authenticator reads.
//
// Two of these fields fail silently when wrong, on a host, long after the write.
// A missing expiration makes the token immortal, and an immortal token in a
// group that may create certificate requests is a permanent cluster credential.
// A group outside the API server's bootstrap group pattern is rejected at
// token-use time as a 401, so it is checked here instead.
func buildTokenSecret(tokenStr, hostName, group string, expiresAt time.Time) (*corev1.Secret, error) {
	tokenID, tokenSecret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("split bootstrap token: %w", err)
	}
	if err := bootstraputil.ValidateBootstrapGroupName(group); err != nil {
		return nil, fmt.Errorf("bootstrap group %q is not usable: %w", group, err)
	}
	if expiresAt.IsZero() {
		return nil, errors.New("bootstrap token expiry is not set")
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootstraputil.BootstrapTokenSecretName(tokenID),
			Namespace: metav1.NamespaceSystem,
		},
		Type: bootstrapapi.SecretTypeBootstrapToken,
		Data: map[string][]byte{
			bootstrapapi.BootstrapTokenIDKey:     []byte(tokenID),
			bootstrapapi.BootstrapTokenSecretKey: []byte(tokenSecret),
			bootstrapapi.BootstrapTokenExpirationKey: []byte(
				expiresAt.UTC().Format(time.RFC3339)),
			bootstrapapi.BootstrapTokenUsageAuthentication: []byte("true"),
			bootstrapapi.BootstrapTokenDescriptionKey: []byte(
				fmt.Sprintf("Bootstrap credential for BYOH host %s", hostName)),
			bootstrapapi.BootstrapTokenExtraGroupsKey: []byte(group),
		},
	}, nil
}

// usableToken reports whether an existing token Secret can still be handed to a
// host, and returns the token string and expiry when it can. A Secret with no
// expiration is never usable: it would authenticate forever.
func usableToken(secret *corev1.Secret, now time.Time) (tokenStr string, expiresAt time.Time, ok bool) {
	tokenID := string(secret.Data[bootstrapapi.BootstrapTokenIDKey])
	tokenSecret := string(secret.Data[bootstrapapi.BootstrapTokenSecretKey])
	if tokenID == "" || tokenSecret == "" {
		return "", time.Time{}, false
	}

	rawExpiration, found := secret.Data[bootstrapapi.BootstrapTokenExpirationKey]
	if !found || len(rawExpiration) == 0 {
		return "", time.Time{}, false
	}
	expiry, err := time.Parse(time.RFC3339, string(rawExpiration))
	if err != nil {
		return "", time.Time{}, false
	}
	if !expiry.After(now.Add(tokenRenewalMargin)) {
		return "", time.Time{}, false
	}

	return tokenID + "." + tokenSecret, expiry, true
}

// renderBootstrapKubeconfig produces the kubeconfig the agent uses to request
// its own client certificate.
func renderBootstrapKubeconfig(transport *transportConfig, tokenStr string) ([]byte, error) {
	config := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			infrav1.DefaultClusterName: {
				Server:                   transport.apiServerURL,
				TLSServerName:            transport.tlsServerName,
				CertificateAuthorityData: transport.caData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			infrav1.DefaultAuth: {
				Token: tokenStr,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			infrav1.DefaultContext: {
				Cluster:   infrav1.DefaultClusterName,
				AuthInfo:  infrav1.DefaultAuth,
				Namespace: infrav1.DefaultNamespace,
			},
		},
		CurrentContext: infrav1.DefaultContext,
	}

	encoded, err := runtime.Encode(clientcmdlatest.Codec, config)
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap kubeconfig: %w", err)
	}
	return encoded, nil
}

// buildCredentialSecret assembles the Secret byohctl reads and pushes to the
// host. The owner reference makes deletion of the enrollment remove it.
func buildCredentialSecret(enrollment *infrav1.ByoHostEnrollment, hostName string, kubeconfig []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialSecretName(enrollment.Name),
			Namespace: enrollment.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: infrav1.GroupVersion.String(),
					Kind:       "ByoHostEnrollment",
					Name:       enrollment.Name,
					UID:        enrollment.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Type: infrav1.CredentialSecretType,
		Data: map[string][]byte{
			infrav1.CredentialSecretKubeconfigKey: kubeconfig,
			infrav1.CredentialSecretHostNameKey:   []byte(hostName),
		},
	}
}

// credentialSecretName names the credential Secret for an enrollment.
//
// An enrollment may already use the whole 253-character object name budget, so
// appending the suffix can overflow it. An over-long name keeps a readable
// prefix and a hash of the full name, the same shape this package uses for
// over-long label values, so two different enrollments do not collapse onto one
// Secret.
func credentialSecretName(enrollmentName string) string {
	name := enrollmentName + infrav1.CredentialSecretNameSuffix
	if len(name) <= infrav1.MaxK8sObjectNameLength {
		return name
	}

	sum := sha256.Sum256([]byte(enrollmentName))
	suffix := hex.EncodeToString(sum[:])[:infrav1.LabelHashLength]
	prefixLength := infrav1.MaxK8sObjectNameLength -
		len(infrav1.CredentialSecretNameSuffix) -
		len(infrav1.LabelSeparator) -
		infrav1.LabelHashLength

	return enrollmentName[:prefixLength] + infrav1.LabelSeparator + suffix + infrav1.CredentialSecretNameSuffix
}
