// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	b64 "encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

// log is for logging in this package.
var bootstrapkubeconfiglog = logf.Log.WithName("bootstrapkubeconfig-resource")

// APIServerURLScheme is the url scheme for the APIServer
const APIServerURLScheme = "https"

const (
	// DefaultTokenExpiry is how far ahead the defaulting webhook sets
	// spec.tokenExpiresAt when a create leaves it empty.
	DefaultTokenExpiry = 30 * time.Minute

	// MaxTokenExpiryWindow is the upper bound of how far ahead
	// spec.tokenExpiresAt may be set to on create.
	MaxTokenExpiryWindow = time.Hour
)

func (r *BootstrapKubeconfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(r).
		WithDefaulter(&BootstrapKubeconfigDefaulter{}).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=create,versions=v1beta1,name=mbootstrapkubeconfig.kb.io,admissionReviewVersions=v1
//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=create;update,versions=v1beta1,name=vbootstrapkubeconfig.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &BootstrapKubeconfig{}
var _ admission.CustomDefaulter = &BootstrapKubeconfigDefaulter{}

// +k8s:deepcopy-gen=false
// BootstrapKubeconfigDefaulter fills in the two fields a caller must not own:
// who created the object, and when its token stops working. Both are taken
// from the admission request rather than the object body, so a caller cannot
// claim an identity or an expiry it was not given.
type BootstrapKubeconfigDefaulter struct{}

// Default implements admission.CustomDefaulter.
func (d *BootstrapKubeconfigDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	bootstrapKubeconfig, ok := obj.(*BootstrapKubeconfig)
	if !ok {
		return fmt.Errorf("expected a BootstrapKubeconfig but got a %T", obj)
	}
	bootstrapkubeconfiglog.Info("default", "name", bootstrapKubeconfig.Name)

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return fmt.Errorf("read the admission request for BootstrapKubeconfig %q: %w", bootstrapKubeconfig.Name, err)
	}

	// Always overwrite so that callers have no control over the CreatedBy field
	// and instead it accurately captures the user from the request.
	bootstrapKubeconfig.Spec.CreatedBy = req.UserInfo.Username

	if bootstrapKubeconfig.Spec.TokenExpiresAt == nil {
		expiresAt := metav1.NewTime(time.Now().Add(DefaultTokenExpiry))
		bootstrapKubeconfig.Spec.TokenExpiresAt = &expiresAt
	}

	return nil
}

func (r *BootstrapKubeconfig) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	bootstrapKubeconfig, ok := obj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", obj)
	}
	bootstrapkubeconfiglog.Info("validate create", "name", bootstrapKubeconfig.Name)

	if err := bootstrapKubeconfig.validateAPIServer(); err != nil {
		return nil, err
	}

	if err := bootstrapKubeconfig.validateCAData(); err != nil {
		return nil, err
	}

	if err := bootstrapKubeconfig.validateHostName(); err != nil {
		return nil, err
	}

	if err := bootstrapKubeconfig.validateTokenExpiresAt(time.Now()); err != nil {
		return nil, err
	}

	return nil, nil
}

func (r *BootstrapKubeconfig) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	bootstrapKubeconfig, ok := newObj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", newObj)
	}
	oldBootstrapKubeconfig, ok := oldObj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", oldObj)
	}
	bootstrapkubeconfiglog.Info("validate update", "name", bootstrapKubeconfig.Name)

	if err := bootstrapKubeconfig.validateAPIServer(); err != nil {
		return nil, err
	}

	if err := bootstrapKubeconfig.validateCAData(); err != nil {
		return nil, err
	}

	if err := validateImmutableFields(oldBootstrapKubeconfig, bootstrapKubeconfig); err != nil {
		return nil, err
	}

	return nil, nil
}

func (r *BootstrapKubeconfig) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	bootstrapKubeconfig, ok := obj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", obj)
	}
	bootstrapkubeconfiglog.Info("validate delete", "name", bootstrapKubeconfig.Name)

	return nil, nil
}

func (r *BootstrapKubeconfig) validateAPIServer() error {
	apiserverField := field.NewPath("spec").Child("apiserver")

	if r.Spec.APIServer == "" {
		return field.Invalid(apiserverField, r.Spec.APIServer, "APIServer field cannot be empty")
	}

	parsedURL, err := url.Parse(r.Spec.APIServer)
	if err != nil {
		return field.Invalid(apiserverField, r.Spec.APIServer, "APIServer URL is not valid")
	}
	if !r.isURLValid(parsedURL) {
		return field.Invalid(apiserverField, r.Spec.APIServer, "APIServer is not of the format https://hostname:port")
	}
	return nil
}

func (r *BootstrapKubeconfig) validateCAData() error {
	caDataField := field.NewPath("spec").Child("caData")

	if r.Spec.CertificateAuthorityData == "" {
		return field.Invalid(caDataField, r.Spec.CertificateAuthorityData, "CertificateAuthorityData field cannot be empty")
	}

	decodedCAData, err := b64.StdEncoding.DecodeString(r.Spec.CertificateAuthorityData)
	if err != nil {
		return field.Invalid(caDataField, r.Spec.CertificateAuthorityData, "cannot base64 decode CertificateAuthorityData")
	}

	block, _ := pem.Decode(decodedCAData)
	if block == nil {
		return field.Invalid(caDataField, r.Spec.CertificateAuthorityData, "CertificateAuthorityData is not PEM encoded")
	}

	return nil
}

// validateHostName requires the host name to already be normalized.
func (r *BootstrapKubeconfig) validateHostName() error {
	path := field.NewPath("spec").Child("hostName")

	if r.Spec.HostName == "" {
		return field.Invalid(path, r.Spec.HostName, "hostName field cannot be empty")
	}

	normalized, err := hostname.Normalize(r.Spec.HostName)
	if err != nil {
		return field.Invalid(path, r.Spec.HostName, err.Error())
	}

	if normalized != r.Spec.HostName {
		detail := fmt.Sprintf("host name is not normalized, use %q", normalized)
		return field.Invalid(path, r.Spec.HostName, detail)
	}

	return nil
}

func (r *BootstrapKubeconfig) validateTokenExpiresAt(now time.Time) error {
	if r.Spec.TokenExpiresAt == nil {
		return nil
	}

	path := field.NewPath("spec").Child("tokenExpiresAt")
	if r.Spec.TokenExpiresAt.After(now.Add(MaxTokenExpiryWindow)) {
		detail := fmt.Sprintf("must not be more than %s in the future", MaxTokenExpiryWindow)
		return field.Invalid(path, r.Spec.TokenExpiresAt.Format(time.RFC3339), detail)
	}

	return nil
}

// validateImmutableFields rejects a change to any field that identifies what
// the object issues or who it was issued for. The defaulting webhook only runs
// on create, so without this an edit could extend a live token's life or point
// the credential's RBAC grant at a different identity.
func validateImmutableFields(oldObj, newObj *BootstrapKubeconfig) error {
	spec := field.NewPath("spec")

	if oldObj.Spec.HostName != newObj.Spec.HostName {
		return field.Forbidden(spec.Child("hostName"), "hostName is immutable")
	}

	if oldObj.Spec.CreatedBy != newObj.Spec.CreatedBy {
		return field.Forbidden(spec.Child("createdBy"), "createdBy is immutable")
	}

	if !apiequality.Semantic.DeepEqual(oldObj.Spec.TokenExpiresAt, newObj.Spec.TokenExpiresAt) {
		return field.Forbidden(spec.Child("tokenExpiresAt"), "tokenExpiresAt is immutable")
	}

	return nil
}

// FIXME CLAUDE: Remove this function if it is used only once.
func (r *BootstrapKubeconfig) isURLValid(parsedURL *url.URL) bool {
	if parsedURL.Host == "" || parsedURL.Scheme != APIServerURLScheme || parsedURL.Port() == "" {
		return false
	}
	return true
}
