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
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
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
	// rootCAConfigMapName is the ConfigMap the root CA publisher maintains in
	// every namespace. It carries the CA that signs the API server's serving
	// certificate, so a BYO host can be handed it without the deployment
	// supplying anything.
	rootCAConfigMapName = "kube-root-ca.crt"

	// rootCAConfigMapKey holds the PEM-encoded CA bundle in that ConfigMap.
	rootCAConfigMapKey = "ca.crt"

	// defaultTokenTTL is used when the enrollment does not set spec.tokenTTL.
	// The CRD defaults that field, so this only covers an object written
	// before the default existed.
	defaultTokenTTL = 30 * time.Minute

	// transportRetryInterval is how long to wait before looking for the API
	// server CA again. Its absence is a cluster that has not published the
	// root CA yet, so retrying is worthwhile but not urgent.
	transportRetryInterval = time.Minute

	// tokenRenewalMargin is how much of a token's remaining life is treated as
	// too little to hand to a host. A token about to expire is replaced rather
	// than reused.
	tokenRenewalMargin = 5 * time.Minute
)

// ByoHostEnrollmentReconciler creates the bootstrap credential for one BYO
// host and delivers it as a Secret in the enrollment's own namespace.
type ByoHostEnrollmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Transport says how a BYO host reaches this management cluster. Build it
	// with NewBootstrapTransport, so a bad value stops the manager instead of
	// every enrollment.
	Transport BootstrapTransport
}

// BootstrapTransport is the deployment-level description of how a BYO host
// reaches the management cluster's API server.
//
// It is one description for the whole deployment. The caller creating an
// enrollment cannot know the endpoint a host dials, and must not be trusted to
// supply it.
type BootstrapTransport struct {
	// APIServerURL is the endpoint a BYO host dials, as https://<host>:<port>.
	// It must pass TLS through: a proxy that terminates TLS strips the client
	// certificate the host later presents.
	APIServerURL string

	// CAData, when set, replaces the in-cluster root CA in the kubeconfig the
	// host receives.
	CAData []byte
}

// NewBootstrapTransport validates the deployment's bootstrap transport
// settings and reads the CA override from disk.
//
// caFile is optional. Leaving it empty makes each reconcile read the cluster's
// own kube-root-ca.crt instead. Set it when hosts reach the API server through
// an external endpoint, which kube-root-ca.crt is not documented to verify.
func NewBootstrapTransport(apiServerURL, caFile string) (BootstrapTransport, error) {
	if err := ValidateAPIServerURL(apiServerURL); err != nil {
		return BootstrapTransport{}, err
	}
	if caFile == "" {
		return BootstrapTransport{APIServerURL: apiServerURL}, nil
	}

	caData, err := os.ReadFile(caFile) // #nosec G304 -- path comes from a controller-manager flag set by the deployment, not by an enrollment
	if err != nil {
		return BootstrapTransport{}, fmt.Errorf("read API server CA file %q: %w", caFile, err)
	}
	if err := ValidateCABundle(caData); err != nil {
		return BootstrapTransport{}, fmt.Errorf("API server CA file %q: %w", caFile, err)
	}

	return BootstrapTransport{
		APIServerURL: apiServerURL,
		CAData:       caData,
	}, nil
}

// ValidateAPIServerURL checks that rawURL is an absolute https URL with a host.
func ValidateAPIServerURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("API server URL is empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("API server URL %q is not a URL: %w", rawURL, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("API server URL must be an https URL with a host, got %q", rawURL)
	}
	return nil
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile creates and delivers the bootstrap credential for one host.
//
// The write order is load-bearing. The token ID reaches status before the
// token Secret exists, so a reconcile that dies partway leaves a record of the
// token it created. The next pass finds that token and either reuses it or
// deletes it, instead of creating a second live credential and abandoning the
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
		return ctrl.Result{}, r.patchEnrollment(ctx, enrollment,
			enrollmentNotReadyMutateFn(enrollment, infrav1.InvalidHostNameReason, err.Error()))
	}

	apiServerURL, caData, transportErr := r.resolveTransport(ctx)
	if transportErr != nil {
		logger.Info("Bootstrap transport is unusable, will retry", "error", transportErr.Error())
		markErr := r.patchEnrollment(ctx, enrollment,
			enrollmentNotReadyMutateFn(enrollment, infrav1.TransportUnavailableReason, transportErr.Error()))
		if markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{RequeueAfter: transportRetryInterval}, nil
	}

	token, err := r.ensureBootstrapToken(ctx, enrollment, hostName)
	if err != nil {
		markErr := r.patchEnrollment(ctx, enrollment,
			enrollmentNotReadyMutateFn(enrollment, infrav1.CredentialGenerateFailedReason, err.Error()))
		if markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	// The kubeconfig the agent uses to request its own client certificate.
	kubeconfig, err := runtime.Encode(clientcmdlatest.Codec, &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{infrav1.DefaultClusterName: {
			Server:                   apiServerURL,
			CertificateAuthorityData: caData,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{infrav1.DefaultAuth: {Token: token.value}},
		Contexts: map[string]*clientcmdapi.Context{infrav1.DefaultContext: {
			Cluster:   infrav1.DefaultClusterName,
			AuthInfo:  infrav1.DefaultAuth,
			Namespace: infrav1.DefaultNamespace,
		}},
		CurrentContext: infrav1.DefaultContext,
	})
	if err != nil {
		err = fmt.Errorf("encode bootstrap kubeconfig: %w", err)
		markErr := r.patchEnrollment(ctx, enrollment,
			enrollmentNotReadyMutateFn(enrollment, infrav1.CredentialGenerateFailedReason, err.Error()))
		if markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	// byohctl reads this Secret and pushes it to the host. The owner reference
	// makes deletion of the enrollment remove it.
	credentialSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialSecretName(enrollment.Name),
			Namespace: enrollment.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: infrav1.GroupVersion.String(),
				Kind:       "ByoHostEnrollment",
				Name:       enrollment.Name,
				UID:        enrollment.UID,
				Controller: new(true),
			}},
		},
		Type: infrav1.CredentialSecretType,
		Data: map[string][]byte{
			infrav1.CredentialSecretKubeconfigKey: kubeconfig,
			infrav1.CredentialSecretHostNameKey:   []byte(hostName),
		},
	}
	if err = r.createOrUpdateSecret(ctx, credentialSecret); err != nil {
		markErr := r.patchEnrollment(ctx, enrollment,
			enrollmentNotReadyMutateFn(enrollment, infrav1.CredentialGenerateFailedReason, err.Error()))
		if markErr != nil {
			return ctrl.Result{}, markErr
		}
		return ctrl.Result{}, err
	}

	err = r.patchEnrollment(ctx, enrollment, func() {
		enrollment.Status.CredentialSecretRef = &corev1.LocalObjectReference{Name: credentialSecret.Name}
		enrollment.Status.ExpiresAt = new(metav1.NewTime(token.expiresAt))
		enrollment.Status.ObservedGeneration = enrollment.Generation
		conditions.MarkTrue(enrollment, infrav1.CredentialReady)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Bootstrap credential is ready", "secret", credentialSecret.Name, "expiresAt", token.expiresAt)
	return ctrl.Result{}, nil
}

// reconcileDelete removes the bootstrap token Secret this enrollment created.
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
// useful life left.
func (r *ByoHostEnrollmentReconciler) ensureBootstrapToken(ctx context.Context, enrollment *infrav1.ByoHostEnrollment, hostName hostname.Name) (*bootstrapToken, error) {
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
			if token := validateToken(existing, now); token != nil {
				return token, nil
			}
			// The recorded token is expired, malformed, or carries no
			// expiration at all. A token with no expiration never stops
			// working, so it is deleted rather than left behind.
			logger.Info("Discarding unusable bootstrap token Secret", "secret", key.Name)
			if deleteErr := r.Delete(ctx, existing); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return nil, fmt.Errorf("delete unusable bootstrap token Secret %s: %w", key.Name, deleteErr)
			}
		case apierrors.IsNotFound(err):
			// The token ID was recorded but the Secret was never written, or
			// was removed. A replacement strands nothing.
		default:
			return nil, fmt.Errorf("get bootstrap token Secret %s: %w", key.Name, err)
		}
	}

	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap token: %w", err)
	}
	tokenID, _, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("split bootstrap token: %w", err)
	}

	ttl := defaultTokenTTL
	if enrollment.Spec.TokenTTL != nil {
		ttl = enrollment.Spec.TokenTTL.Duration
	}
	expiresAt := now.Add(ttl)

	tokenSecret, err := buildTokenSecret(tokenStr, hostName, infrav1.BootstrapTokenExtraGroups, expiresAt)
	if err != nil {
		return nil, err
	}
	tokenSecret.Labels = map[string]string{
		infrav1.EnrollmentLabel: generateSafeLabelValue(enrollment.Namespace, enrollment.Name),
	}

	// tokenStr only exists in memory until the Secret write below. So
	// recording tokenID in status first is safe: if we crash now, nothing
	// was ever created. If the patch succeeds but the Secret write fails,
	// status names a token that does not exist, and the next pass gets a
	// NotFound and generates a replacement. Writing the Secret first would
	// risk the opposite: a live token in kube-system that status never
	// records, sitting there for its full TTL.
	err = r.patchEnrollment(ctx, enrollment, func() {
		enrollment.Status.TokenID = tokenID
		enrollment.Status.ExpiresAt = new(metav1.NewTime(expiresAt))
	})
	if err != nil {
		return nil, err
	}

	if err := r.createOrUpdateSecret(ctx, tokenSecret); err != nil {
		return nil, err
	}

	logger.Info("Created bootstrap token", "tokenID", tokenID, "expiresAt", expiresAt)
	return &bootstrapToken{
		value:     tokenStr,
		expiresAt: expiresAt,
	}, nil
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

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostEnrollmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.ByoHostEnrollment{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// bootstrapToken is a token that is safe to hand to a host: the full token
// string and the moment it stops working.
type bootstrapToken struct {
	value     string
	expiresAt time.Time
}

// enrollmentNotReadyMutateFn records why no usable credential exists yet.
func enrollmentNotReadyMutateFn(enrollment *infrav1.ByoHostEnrollment, reason, message string) func() {
	return func() {
		enrollment.Status.ObservedGeneration = enrollment.Generation
		conditions.MarkFalse(enrollment, infrav1.CredentialReady, reason, clusterv1.ConditionSeverityWarning, "%s", message)
	}
}

// resolveTransport returns the API server URL and CA bundle to render into a
// host's kubeconfig.
//
// The URL and any CA override are fixed at startup. Without an override the
// cluster's own kube-root-ca.crt is read on every reconcile, so a rotated root
// CA reaches new enrollments without restarting the manager.
func (r *ByoHostEnrollmentReconciler) resolveTransport(ctx context.Context) (apiServerURL string, caData []byte, err error) {
	if err := ValidateAPIServerURL(r.Transport.APIServerURL); err != nil {
		return "", nil, err
	}
	if len(r.Transport.CAData) > 0 {
		return r.Transport.APIServerURL, r.Transport.CAData, nil
	}

	key := client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: rootCAConfigMapName}
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, key, configMap); err != nil {
		return "", nil, fmt.Errorf("get ConfigMap %s: %w", key, err)
	}

	rootCA := configMap.Data[rootCAConfigMapKey]
	if rootCA == "" {
		return "", nil, fmt.Errorf("ConfigMap %s key %q is empty", key, rootCAConfigMapKey)
	}
	if err := ValidateCABundle([]byte(rootCA)); err != nil {
		return "", nil, fmt.Errorf("ConfigMap %s key %q: %w", key, rootCAConfigMapKey, err)
	}

	return r.Transport.APIServerURL, []byte(rootCA), nil
}

// ValidateCABundle checks that data is PEM holding at least one parseable
// certificate. A bundle that only looks like PEM fails on the host, long after
// it was written, as a TLS error with no obvious cause.
func ValidateCABundle(data []byte) error {
	found := false
	rest := data
	// The loop walks every PEM block in the bundle. pem.Decode signals the end
	// by returning a nil block, and that is only known inside the body, so
	// there is nothing to put in the loop header.
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
// Both the expiration and the group are checked here because neither fails
// where it is written. A token with no expiration never stops working, which
// in a group that may create certificate requests is a permanent cluster
// credential. A group outside the API server's bootstrap group pattern is
// rejected only when the host uses the token, as a 401.
func buildTokenSecret(tokenStr string, hostName hostname.Name, group string, expiresAt time.Time) (*corev1.Secret, error) {
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
			bootstrapapi.BootstrapTokenDescriptionKey: fmt.Appendf(
				nil, "Bootstrap credential for BYOH host %s", hostName),
			bootstrapapi.BootstrapTokenExtraGroupsKey: []byte(group),
		},
	}, nil
}

// validateToken checks whether an existing token Secret can still be handed to
// a host. It returns the token when it can and nil when it cannot. A Secret
// with no expiration is never usable: it would authenticate forever.
func validateToken(secret *corev1.Secret, now time.Time) *bootstrapToken {
	tokenID := string(secret.Data[bootstrapapi.BootstrapTokenIDKey])
	tokenSecret := string(secret.Data[bootstrapapi.BootstrapTokenSecretKey])
	if tokenID == "" || tokenSecret == "" {
		return nil
	}

	rawExpiration, found := secret.Data[bootstrapapi.BootstrapTokenExpirationKey]
	if !found || len(rawExpiration) == 0 {
		return nil
	}
	expiry, err := time.Parse(time.RFC3339, string(rawExpiration))
	if err != nil {
		return nil
	}
	if !expiry.After(now.Add(tokenRenewalMargin)) {
		return nil
	}

	return &bootstrapToken{
		value:     tokenID + "." + tokenSecret,
		expiresAt: expiry,
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
