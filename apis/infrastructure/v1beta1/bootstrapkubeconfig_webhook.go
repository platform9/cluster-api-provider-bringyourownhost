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

// DefaultTokenExpiry is how far ahead the defaulting webhook stamps
// spec.tokenExpiresAt on create.
const DefaultTokenExpiry = 30 * time.Minute

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

	// Stamp rather than fill in. A body-supplied expiry would let a caller
	// create a token that outlives its onboarding window.
	expiresAt := metav1.NewTime(time.Now().Add(DefaultTokenExpiry))
	bootstrapKubeconfig.Spec.TokenExpiresAt = &expiresAt

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
	if parsedURL.Host == "" || parsedURL.Scheme != APIServerURLScheme || parsedURL.Port() == "" {
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

// validateImmutableFields rejects a change to any field that identifies what
// the object issues or who it was issued for. The defaulting webhook only runs
// on create, so without this an edit could extend a live token's life or point
// the credential's RBAC grant at a different identity.
func validateImmutableFields(oldObj, newObj *BootstrapKubeconfig) error {
	immutableFields := []struct {
		name    string
		changed bool
	}{
		{
			name:    "hostName",
			changed: oldObj.Spec.HostName != newObj.Spec.HostName,
		},
		{
			name:    "createdBy",
			changed: oldObj.Spec.CreatedBy != newObj.Spec.CreatedBy,
		},
		{
			name:    "tokenExpiresAt",
			changed: !apiequality.Semantic.DeepEqual(oldObj.Spec.TokenExpiresAt, newObj.Spec.TokenExpiresAt),
		},
	}

	for _, f := range immutableFields {
		if f.changed {
			return field.Forbidden(field.NewPath("spec").Child(f.name), f.name+" is immutable")
		}
	}

	return nil
}
