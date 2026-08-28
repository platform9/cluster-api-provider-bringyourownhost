// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"strings"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// byohCSRNamePrefix marks a certificate signing request as one this
	// controller is responsible for. It is a filter, not a check: any client
	// able to create a request can pick the name, so everything the approver
	// actually trusts comes from the fields below, which the API server fills
	// in from the authenticated request.
	byohCSRNamePrefix = "byoh-csr-"

	// approvedReason is recorded on requests this controller approves.
	approvedReason = "Approved by ByoAdmission Controller"

	// maxCSRExpirationSeconds is the longest certificate lifetime the
	// approver accepts, one year.
	//
	// It matches what the agent asks for today, and must not be lowered on
	// its own. Certificate renewal in the agent sits behind the
	// CERTIFICATE_ROTATION feature flag, which the shipped packaging never
	// sets, so a host never renews its certificate. A lower ceiling would
	// deny every host whose request exceeds it, and a shorter agent request
	// would strand every host once its certificate expired with nothing to
	// renew it. Lower this only together with working renewal.
	maxCSRExpirationSeconds int32 = 86400 * 365

	reasonRequesterNotBootstrapper = "RequesterNotByohBootstrapper"
	reasonUnexpectedSignerName     = "UnexpectedSignerName"
	reasonUnexpectedUsages         = "UnexpectedUsages"
	reasonExpirationTooLong        = "ExpirationTooLong"
)

// csrDenial explains why a certificate signing request must not be approved.
// reason is the short code recorded on the denied condition, message the
// readable detail that goes with it.
type csrDenial struct {
	reason  string
	message string
}

// ByoAdmissionReconciler reconciles a ByoAdmission object
type ByoAdmissionReconciler struct {
	ClientSet clientset.Interface
}

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve

// Reconcile continuously checks for CSRs, approves the ones a BYOH host is
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
			logger.Info("CertificateSigningRequest is already approved", "CSR", csr.Name)
		}
		if csrDenied {
			logger.Info("CertificateSigningRequest is already denied", "CSR", csr.Name)
		}
		return ctrl.Result{}, nil
	}

	if denial := validateByohCSR(csr); denial != nil {
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

// validateByohCSR reports why csr must not be approved, and returns nil when
// the request is one a BYOH host is allowed to have. Every field it reads is
// filled in by the API server from the authenticated request, so a client
// cannot dress up a request to pass.
func validateByohCSR(csr *certv1.CertificateSigningRequest) *csrDenial {
	if !hasGroup(csr.Spec.Groups, infrastructurev1beta1.BootstrapTokenExtraGroups) {
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

	if denial := validateUsages(csr.Spec.Usages); denial != nil {
		return denial
	}

	return validateExpirationSeconds(csr.Spec.ExpirationSeconds)
}

// hasGroup reports whether groups contains want.
func hasGroup(groups []string, want string) bool {
	for _, group := range groups {
		if group == want {
			return true
		}
	}
	return false
}

// validateUsages reports why usages must not be approved. A BYOH certificate
// is used for one thing, authenticating to the API server, so anything beyond
// a single client auth usage is refused rather than ignored.
func validateUsages(usages []certv1.KeyUsage) *csrDenial {
	if len(usages) == 1 && usages[0] == certv1.UsageClientAuth {
		return nil
	}
	return &csrDenial{
		reason:  reasonUnexpectedUsages,
		message: fmt.Sprintf("usages are %v, want exactly [%s]", usages, certv1.UsageClientAuth),
	}
}

// validateExpirationSeconds reports why the requested certificate lifetime
// must not be approved. A request that asks for no particular lifetime is
// left to the signer.
func validateExpirationSeconds(expirationSeconds *int32) *csrDenial {
	if expirationSeconds == nil || *expirationSeconds <= maxCSRExpirationSeconds {
		return nil
	}
	return &csrDenial{
		reason:  reasonExpirationTooLong,
		message: fmt.Sprintf("requested lifetime of %d seconds is longer than the %d seconds allowed", *expirationSeconds, maxCSRExpirationSeconds),
	}
}

// Check if the CSR has the given condition.
func checkCSRCondition(conditions []certv1.CertificateSigningRequestCondition, conditionType certv1.RequestConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoAdmissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
