// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"fmt"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
)

// log is for logging in this package.
var byohostenrollmentlog = logf.Log.WithName("byohostenrollment-resource")

const (
	// MaxTokenTTL is the longest bootstrap token life an enrollment may ask
	// for. It follows kubeadm, whose token create and init both issue tokens
	// valid for 24 hours.
	MaxTokenTTL = 24 * time.Hour

	// defaultTokenTTL is the TTL assumed when spec.tokenTTL is unset. The CRD
	// carries the same value as a default, so the apiserver fills the field in
	// before this webhook runs; this covers a caller that validates an object
	// without going through defaulting.
	defaultTokenTTL = 30 * time.Minute
)

// +k8s:deepcopy-gen=false
// ByoHostEnrollmentValidator validates ByoHostEnrollments. It needs a client
// because one of its rules is about a different object, the ByoHost of the
// same name, so it cannot be a method on the enrollment itself.
type ByoHostEnrollmentValidator struct {
	Client client.Client
}

// SetupWebhookWithManager registers the validator with the manager.
func (v *ByoHostEnrollmentValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&ByoHostEnrollment{}).
		WithValidator(v).
		Complete()
}

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohostenrollment,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments,verbs=create;update,versions=v1beta1,name=vbyohostenrollment.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &ByoHostEnrollmentValidator{}

// ValidateCreate implements admission.CustomValidator.
func (v *ByoHostEnrollmentValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	enrollment, ok := obj.(*ByoHostEnrollment)
	if !ok {
		return nil, fmt.Errorf("expected a ByoHostEnrollment but got a %T", obj)
	}
	byohostenrollmentlog.Info("validate create", "name", enrollment.Name)

	if err := validateEnrollmentName(enrollment.Name); err != nil {
		return nil, err
	}

	if err := validateTokenTTL(enrollment.Spec.TokenTTL); err != nil {
		return nil, err
	}

	if err := validateValidUntil(enrollment.Spec.ValidUntil, effectiveTokenTTL(enrollment.Spec.TokenTTL), time.Now()); err != nil {
		return nil, err
	}

	if err := v.validateHostNotRegistered(ctx, enrollment); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateUpdate implements admission.CustomValidator. The spec is immutable:
// re-enrolling a host means deleting the enrollment and creating it again, so
// there is no state where an edited spec has to be reconciled against a
// credential minted from the previous one.
func (v *ByoHostEnrollmentValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldEnrollment, ok := oldObj.(*ByoHostEnrollment)
	if !ok {
		return nil, fmt.Errorf("expected a ByoHostEnrollment but got a %T", oldObj)
	}
	newEnrollment, ok := newObj.(*ByoHostEnrollment)
	if !ok {
		return nil, fmt.Errorf("expected a ByoHostEnrollment but got a %T", newObj)
	}
	byohostenrollmentlog.Info("validate update", "name", newEnrollment.Name)

	if !apiequality.Semantic.DeepEqual(oldEnrollment.Spec, newEnrollment.Spec) {
		return nil, field.Forbidden(field.NewPath("spec"),
			"ByoHostEnrollment spec is immutable; delete the enrollment and create it again")
	}

	return nil, nil
}

// ValidateDelete implements admission.CustomValidator. Nothing about a delete
// is invalid.
func (v *ByoHostEnrollmentValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateEnrollmentName requires metadata.name to be a host name that is
// already normalized. The name is the host's identity through the whole flow -
// the ByoHost, the certificate common name and the node name all derive from
// it - so a caller that skipped normalization is told to normalize rather than
// having its name silently rewritten here.
func validateEnrollmentName(name string) error {
	normalized, err := hostname.Normalize(name)
	if err != nil {
		return field.Invalid(field.NewPath("metadata").Child("name"), name, err.Error())
	}

	if normalized != name {
		return field.Invalid(field.NewPath("metadata").Child("name"), name,
			fmt.Sprintf("host name is not normalized, use %q", normalized))
	}

	return nil
}

// effectiveTokenTTL is the TTL this enrollment will actually get.
func effectiveTokenTTL(ttl *metav1.Duration) time.Duration {
	if ttl == nil {
		return defaultTokenTTL
	}
	return ttl.Duration
}

// validateTokenTTL enforces the ceiling on a single token's life.
func validateTokenTTL(ttl *metav1.Duration) error {
	if ttl == nil {
		return nil
	}

	if ttl.Duration > MaxTokenTTL {
		return field.Invalid(field.NewPath("spec").Child("tokenTTL"), ttl.Duration.String(),
			fmt.Sprintf("must not exceed %s", MaxTokenTTL))
	}

	return nil
}

// validateValidUntil requires the deadline to leave room for at least one
// token. A deadline in the past stops the refresh loop before it starts, and
// one closer than a single TTL expires while the first token is still live,
// which is a deadline that says nothing.
func validateValidUntil(validUntil *metav1.Time, ttl time.Duration, now time.Time) error {
	if validUntil == nil {
		return nil
	}

	path := field.NewPath("spec").Child("validUntil")
	deadline := validUntil.Time

	if !deadline.After(now) {
		return field.Invalid(path, deadline.Format(time.RFC3339), "must be in the future")
	}

	if deadline.Before(now.Add(ttl)) {
		return field.Invalid(path, deadline.Format(time.RFC3339),
			fmt.Sprintf("must be at least one tokenTTL (%s) from now", ttl))
	}

	return nil
}

// validateHostNotRegistered rejects an enrollment for a host that is already
// registered. Handing a live host a second bootstrap credential would let it
// re-register under an identity the cluster already tracks, so re-onboarding
// is a deliberate two-step operation: off-board the host first, then enroll it
// again.
func (v *ByoHostEnrollmentValidator) validateHostNotRegistered(ctx context.Context, enrollment *ByoHostEnrollment) error {
	byoHost := &ByoHost{}
	key := client.ObjectKey{
		Name:      enrollment.Name,
		Namespace: enrollment.Namespace,
	}

	err := v.Client.Get(ctx, key, byoHost)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check whether ByoHost %q already exists: %w", enrollment.Name, err)
	}

	return field.Forbidden(field.NewPath("metadata").Child("name"),
		fmt.Sprintf("ByoHost %q is already registered; off-board that host before enrolling it again", enrollment.Name))
}
