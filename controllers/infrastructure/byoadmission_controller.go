// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// byohCSRNamePrefix marks a certificate signing request as one this
	// controller is responsible for.
	//
	// NOTE: It is used as a filter and not as a check: any client that is able
	// to create a request can pick the name, so everything the approver
	// actually trusts comes from the fields below, which the API server fills
	// in from the authenticated request.
	byohCSRNamePrefix = "byoh-csr-"

	// approvedReason is recorded on requests this controller approves.
	approvedReason = "Approved by ByoAdmission Controller"

	// maxCSRExpirationSeconds is the longest certificate lifetime the
	// approver accepts.
	//
	// NOTE: Any change to this must also be accompanied by a similar change on
	// the agent. Since lowering the value here will automatically start denying
	// the agent's CSRs.
	maxCSRExpirationSeconds int32 = 86400 * 365

	// bootstrapUsernamePrefix is what the API server records in spec.username
	// when a request authenticates with a bootstrap token. The rest of the
	// username is the token's public ID. A client cannot set this field, which
	// is what makes it usable as an identity.
	bootstrapUsernamePrefix = "system:bootstrap:"

	// certificateRequestPEMType is the only PEM block spec.request may hold.
	certificateRequestPEMType = "CERTIFICATE REQUEST"

	reasonRequesterNotBootstrapper = "RequesterNotByohBootstrapper"
	reasonUnexpectedSignerName     = "UnexpectedSignerName"
	reasonUnexpectedUsages         = "UnexpectedUsages"
	reasonExpirationTooLong        = "ExpirationTooLong"
	reasonMalformedUsername        = "MalformedBootstrapUsername"
	reasonUnknownBootstrapToken    = "UnknownBootstrapToken"
	reasonAmbiguousBootstrapToken  = "AmbiguousBootstrapToken"
	reasonMalformedRequest         = "MalformedCertificateRequest"
	reasonCommonNameNotPermitted   = "CommonNameNotPermitted"
)

// EnrollmentTokenIDIndex indexes ByoHostEnrollment objects by the bootstrap
// token they created, so a certificate request's requester resolves to its
// enrollment in one cache read.
const EnrollmentTokenIDIndex = "status.tokenID"

// csrDenial explains why a certificate signing request must not be approved.
type csrDenial struct {
	// reason is the short code recorded on the denied condition
	reason string
	// message is the readable detail for the csr denial
	message string
}

// ByoAdmissionReconciler reconciles a ByoAdmission object
type ByoAdmissionReconciler struct {
	// ClientSet settles requests through the approval subresource, which the
	// typed client cannot reach.
	ClientSet clientset.Interface

	// Client reads ByoHostEnrollment objects through the manager's cache,
	// using the EnrollmentTokenIDIndex.
	Client client.Client
}

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohostenrollments,verbs=get;list;watch

// Reconcile continuously checks for CSRs, approves the ones a BYO host is
// allowed to have and denies the rest.
func (r *ByoAdmissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received", "object", req.NamespacedName)

	// Fetch the CSR from the api-server
	csr, err := r.ClientSet.CertificatesV1().CertificateSigningRequests().Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "CertificateSigningRequest not found, won't reconcile")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// A request that is not named like ours belongs to somebody else, so it
	// is neither approved nor denied here.
	if !strings.HasPrefix(csr.Name, byohCSRNamePrefix) {
		logger.Info("CertificateSigningRequest is not a BYOH request, won't reconcile", "CSR", csr.Name)
		return ctrl.Result{}, nil
	}

	// Check if the CSR is already approved or denied
	csrApproved := checkCSRCondition(csr.Status.Conditions, certv1.CertificateApproved)
	csrDenied := checkCSRCondition(csr.Status.Conditions, certv1.CertificateDenied)
	if csrApproved || csrDenied {
		if csrApproved {
			logger.Info("CertificateSigningRequest was already approved", "CSR", csr.Name)
		}
		if csrDenied {
			logger.Info("CertificateSigningRequest was already denied", "CSR", csr.Name)
		}
		return ctrl.Result{}, nil
	}

	denial, err := r.validate(ctx, csr)
	if err != nil {
		return reconcile.Result{}, err
	}
	if denial != nil {
		logger.Info("Denying CSR", "object", req.NamespacedName, "reason", denial.reason, "message", denial.message)
		if err := r.setCSRCondition(ctx, csr, certv1.CertificateDenied, denial.reason, denial.message); err != nil {
			return reconcile.Result{}, err
		}
		logger.Info("CSR Denied", "object", req.NamespacedName, "reason", denial.reason)
		return ctrl.Result{}, nil
	}

	// Approve the CSR
	logger.Info("Approving CSR", "object", req.NamespacedName)
	if err := r.setCSRCondition(ctx, csr, certv1.CertificateApproved, approvedReason, ""); err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("CSR Approved", "object", req.NamespacedName)

	return ctrl.Result{}, nil
}

// setCSRCondition records conditionType on csr and pushes it through the
// approval subresource, which is the only way an approver may settle a
// request.
func (r *ByoAdmissionReconciler) setCSRCondition(ctx context.Context, csr *certv1.CertificateSigningRequest, conditionType certv1.RequestConditionType, reason, message string) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certv1.CertificateSigningRequestCondition{
		Type:    conditionType,
		Status:  corev1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})

	_, err := r.ClientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update the approval of certificate signing request %q: %w", csr.Name, err)
	}
	return nil
}

// validate reports why csr must not be approved, and returns nil when the
// request is one a BYOH host is allowed to have. An error means the answer is
// unknown, which is never an approval: the request is left pending and the
// reconcile is retried.
func (r *ByoAdmissionReconciler) validate(ctx context.Context, csr *certv1.CertificateSigningRequest) (*csrDenial, error) {
	if denial := validateByohCSR(csr); denial != nil {
		return denial, nil
	}
	return r.validateEnrolledHost(ctx, csr)
}

// validateEnrolledHost reports why csr must not be approved for the host its
// subject claims to be.
//
// The checks above establish that the requester holds a BYOH bootstrap token.
// They say nothing about which host that token was created for, so on their own
// a credential handed to one host still buys a certificate for another. The
// enrollment that created the token is the record of which host it was for, and
// spec.username carries the token ID that leads back to it.
//
// A token with no enrollment behind it is revoked or forged, so it is denied.
// The enrollment controller records status.tokenID before it writes the token
// Secret, so any token that can authenticate is already indexed and a miss here
// is never a race.
func (r *ByoAdmissionReconciler) validateEnrolledHost(ctx context.Context, csr *certv1.CertificateSigningRequest) (*csrDenial, error) {
	tokenID, ok := bootstrapTokenID(csr.Spec.Username)
	if !ok {
		return &csrDenial{
			reason:  reasonMalformedUsername,
			message: fmt.Sprintf("requester %q is not a bootstrap token identity", csr.Spec.Username),
		}, nil
	}

	enrollments := &infrastructurev1beta1.ByoHostEnrollmentList{}
	if err := r.Client.List(ctx, enrollments, client.MatchingFields{EnrollmentTokenIDIndex: tokenID}); err != nil {
		return nil, fmt.Errorf("list ByoHostEnrollments for bootstrap token %q: %w", tokenID, err)
	}
	if len(enrollments.Items) == 0 {
		return &csrDenial{
			reason:  reasonUnknownBootstrapToken,
			message: fmt.Sprintf("no ByoHostEnrollment holds bootstrap token %q", tokenID),
		}, nil
	}
	if len(enrollments.Items) > 1 {
		return &csrDenial{
			reason:  reasonAmbiguousBootstrapToken,
			message: fmt.Sprintf("%d ByoHostEnrollments hold bootstrap token %q, so the host it belongs to is unknown", len(enrollments.Items), tokenID),
		}, nil
	}
	enrollment := &enrollments.Items[0]

	commonName, err := requestedCommonName(csr.Spec.Request)
	if err != nil {
		return &csrDenial{
			reason:  reasonMalformedRequest,
			message: err.Error(),
		}, nil
	}

	permitted := infrastructurev1beta1.HostCommonNamePrefix + enrollment.Name
	if commonName != permitted {
		return &csrDenial{
			reason: reasonCommonNameNotPermitted,
			message: fmt.Sprintf("common name %q was requested, but ByoHostEnrollment %s/%s permits only %q",
				commonName, enrollment.Namespace, enrollment.Name, permitted),
		}, nil
	}
	return nil, nil
}

// enrollmentTokenIDIndexer backs EnrollmentTokenIDIndex. An enrollment that
// has not yet created a token is left out of the index rather than filed under
// the empty string, so a request quoting no token ID matches nothing.
func enrollmentTokenIDIndexer(object client.Object) []string {
	enrollment, ok := object.(*infrastructurev1beta1.ByoHostEnrollment)
	if !ok || enrollment.Status.TokenID == "" {
		return nil
	}
	return []string{enrollment.Status.TokenID}
}

// bootstrapTokenID returns the token ID a bootstrap token username carries,
// and reports whether username had that shape at all.
func bootstrapTokenID(username string) (string, bool) {
	tokenID, found := strings.CutPrefix(username, bootstrapUsernamePrefix)
	if !found || tokenID == "" {
		return "", false
	}
	return tokenID, true
}

// requestedCommonName returns the common name in a PEM-encoded certificate
// signing request. Everything inside the request is client-supplied, so the
// common name is a claim to check rather than an identity to trust.
func requestedCommonName(request []byte) (string, error) {
	block, _ := pem.Decode(request)
	if block == nil {
		return "", errors.New("certificate request is not PEM")
	}
	if block.Type != certificateRequestPEMType {
		return "", fmt.Errorf("certificate request holds a %q PEM block, want %q", block.Type, certificateRequestPEMType)
	}
	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate request: %w", err)
	}
	return parsed.Subject.CommonName, nil
}

// validateByohCSR reports why csr must not be approved, and returns nil when
// the request is one a BYOH host is allowed to have. Every field it reads is
// filled in by the API server from the authenticated request, so a client
// cannot dress up a request to pass.
func validateByohCSR(csr *certv1.CertificateSigningRequest) *csrDenial {
	if !slices.Contains(csr.Spec.Groups, infrastructurev1beta1.BootstrapTokenExtraGroups) {
		return &csrDenial{
			reason:  reasonRequesterNotBootstrapper,
			message: fmt.Sprintf("requester %q is not in group %q", csr.Spec.Username, infrastructurev1beta1.BootstrapTokenExtraGroups),
		}
	}

	if csr.Spec.SignerName != certv1.KubeAPIServerClientSignerName {
		return &csrDenial{
			reason:  reasonUnexpectedSignerName,
			message: fmt.Sprintf("signer name is %q, want %q", csr.Spec.SignerName, certv1.KubeAPIServerClientSignerName),
		}
	}

	usages := csr.Spec.Usages
	if len(usages) != 1 || usages[0] != certv1.UsageClientAuth {
		return &csrDenial{
			reason:  reasonUnexpectedUsages,
			message: fmt.Sprintf("usages are %v, want exactly [%s]", usages, certv1.UsageClientAuth),
		}
	}

	expirationSeconds := csr.Spec.ExpirationSeconds
	if expirationSeconds != nil && *expirationSeconds > maxCSRExpirationSeconds {
		return &csrDenial{
			reason:  reasonExpirationTooLong,
			message: fmt.Sprintf("requested lifetime of %d seconds is longer than the %d seconds allowed", *expirationSeconds, maxCSRExpirationSeconds),
		}
	}

	return nil
}

// Check if the CSR has the given condition.
// FIXME: This function can be replaced by a slices.Contains call.
func checkCSRCondition(conditions []certv1.CertificateSigningRequestCondition, conditionType certv1.RequestConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoAdmissionReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &infrastructurev1beta1.ByoHostEnrollment{}, EnrollmentTokenIDIndex, enrollmentTokenIDIndexer); err != nil {
		return fmt.Errorf("index ByoHostEnrollment by %s: %w", EnrollmentTokenIDIndex, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&certv1.CertificateSigningRequest{}).WithEventFilter(
		// watch only BYOH created CSRs
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return strings.HasPrefix(e.Object.GetName(), byohCSRNamePrefix)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return strings.HasPrefix(e.ObjectOld.GetName(), byohCSRNamePrefix)
			}}).
		Complete(r)
}
