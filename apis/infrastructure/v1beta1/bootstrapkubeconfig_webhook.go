// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	b64 "encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var bootstrapkubeconfiglog = logf.Log.WithName("bootstrapkubeconfig-resource")

// APIServerURLScheme is the url scheme for the APIServer
const APIServerURLScheme = "https"

func (r *BootstrapKubeconfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(r).
		Complete()
}

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=create;update,versions=v1beta1,name=vbootstrapkubeconfig.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &BootstrapKubeconfig{}

// ValidateCreate implements admission.CustomValidator so a webhook will be registered for the type
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

	return nil, nil
}

// ValidateUpdate implements admission.CustomValidator so a webhook will be registered for the type
func (r *BootstrapKubeconfig) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	bootstrapKubeconfig, ok := newObj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", newObj)
	}
	bootstrapkubeconfiglog.Info("validate update", "name", bootstrapKubeconfig.Name)

	if err := bootstrapKubeconfig.validateAPIServer(); err != nil {
		return nil, err
	}

	if err := bootstrapKubeconfig.validateCAData(); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateDelete implements admission.CustomValidator so a webhook will be registered for the type
func (r *BootstrapKubeconfig) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	bootstrapKubeconfig, ok := obj.(*BootstrapKubeconfig)
	if !ok {
		return nil, fmt.Errorf("expected a BootstrapKubeconfig but got a %T", obj)
	}
	bootstrapkubeconfiglog.Info("validate delete", "name", bootstrapKubeconfig.Name)

	return nil, nil
}

func (r *BootstrapKubeconfig) validateAPIServer() error {
	if r.Spec.APIServer == "" {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer field cannot be empty")
	}

	parsedURL, err := url.Parse(r.Spec.APIServer)
	if err != nil {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer URL is not valid")
	}
	if !r.isURLValid(parsedURL) {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer is not of the format https://hostname:port")
	}
	return nil
}

func (r *BootstrapKubeconfig) validateCAData() error {
	if r.Spec.CertificateAuthorityData == "" {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "CertificateAuthorityData field cannot be empty")
	}

	decodedCAData, err := b64.StdEncoding.DecodeString(r.Spec.CertificateAuthorityData)
	if err != nil {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "cannot base64 decode CertificateAuthorityData")
	}

	block, _ := pem.Decode(decodedCAData)
	if block == nil {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "CertificateAuthorityData is not PEM encoded")
	}

	return nil
}

func (r *BootstrapKubeconfig) isURLValid(parsedURL *url.URL) bool {
	if parsedURL.Host == "" || parsedURL.Scheme != APIServerURLScheme || parsedURL.Port() == "" {
		return false
	}
	return true
}
