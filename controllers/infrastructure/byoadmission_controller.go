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

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

// ByoAdmissionReconciler reconciles a ByoAdmission object
type ByoAdmissionReconciler struct {
	ClientSet clientset.Interface

	// Client reads the BootstrapKubeconfig that issued the token a request
	// authenticated with. The approval is decided from that object.
	Client client.Client
}

const (
	// BootstrapKubeconfigTokenIDIndex is the field index on a
	// BootstrapKubeconfig's status.tokenID. The approver finds the issuing
	// object of a request through it.
	BootstrapKubeconfigTokenIDIndex = "status.tokenID"

	// bootstrapUserPrefix is what the API server puts in front of a token id
	// in the username of a request that authenticated with a bootstrap token.
	bootstrapUserPrefix = "system:bootstrap:"

	// hostCertificateOrganization is the only organization a host certificate
	// may have. The API server derives group membership from it, so an
	// unchecked value would let a token holder request system:masters.
	hostCertificateOrganization = "byoh:hosts"

	// deniedReason is the reason of the Denied condition this controller sets.
	deniedReason = "DeniedByByoAdmissionController"
)

// IndexBootstrapKubeconfigByTokenID returns the token ID of a
// BootstrapKubeconfig for BootstrapKubeconfigTokenIDIndex. An object that has
// no token yet is not indexed.
func IndexBootstrapKubeconfigByTokenID(obj client.Object) []string {
	bootstrapKubeconfig, ok := obj.(*infrastructurev1beta1.BootstrapKubeconfig)
	if !ok || bootstrapKubeconfig.Status.TokenID == "" {
		return nil
	}

	return []string{bootstrapKubeconfig.Status.TokenID}
}

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve

// Reconcile continuosuly checks for CSRs and approves them
func (r *ByoAdmissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
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

	if denial := r.validateCSR(ctx, csr); denial != nil {
		logger.Info("Denying CSR", "object", req.NamespacedName, "reason", denial.Error())
		csr.Status.Conditions = append(csr.Status.Conditions, certv1.CertificateSigningRequestCondition{
			Type:    certv1.CertificateDenied,
			Status:  corev1.ConditionTrue,
			Reason:  deniedReason,
			Message: denial.Error(),
		})
		_, err = r.ClientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
		if err != nil {
			return reconcile.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Update the CSR to the "Approved" condition
	csr.Status.Conditions = append(csr.Status.Conditions, certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Status: corev1.ConditionTrue,
		Reason: "Approved by ByoAdmission Controller",
	})

	// Approve the CSR
	logger.Info("Approving CSR", "object", req.NamespacedName)
	_, err = r.ClientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("CSR Approved", "object", req.NamespacedName)

	return ctrl.Result{}, nil
}

// validateCSR returns why the request must be denied, or nil when it may be
// approved. The host the request may act as comes from the BootstrapKubeconfig
// that issued the requester's token, never from what the request asks for.
func (r *ByoAdmissionReconciler) validateCSR(ctx context.Context, csr *certv1.CertificateSigningRequest) error {
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil {
		return errors.New("certificate request is not PEM encoded")
	}

	certificateRequest, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse the certificate request: %w", err)
	}

	if csr.Spec.SignerName != certv1.KubeAPIServerClientSignerName {
		return fmt.Errorf("signer must be %s, got %q", certv1.KubeAPIServerClientSignerName, csr.Spec.SignerName)
	}

	if !slices.Equal(csr.Spec.Usages, []certv1.KeyUsage{certv1.UsageClientAuth}) {
		return fmt.Errorf("usages must be exactly [%s], got %v", certv1.UsageClientAuth, csr.Spec.Usages)
	}

	if !slices.Equal(certificateRequest.Subject.Organization, []string{hostCertificateOrganization}) {
		return fmt.Errorf("organization must be exactly [%s], got %v", hostCertificateOrganization, certificateRequest.Subject.Organization)
	}

	requested, err := identity.Parse(certificateRequest.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("common name is not a host identity: %w", err)
	}

	tokenID, isBootstrapUser := strings.CutPrefix(csr.Spec.Username, bootstrapUserPrefix)
	if !isBootstrapUser || tokenID == "" {
		return fmt.Errorf("requester %q did not authenticate with a bootstrap token", csr.Spec.Username)
	}

	list := &infrastructurev1beta1.BootstrapKubeconfigList{}
	err = r.Client.List(ctx, list, client.MatchingFields{BootstrapKubeconfigTokenIDIndex: tokenID})
	if err != nil {
		return fmt.Errorf("list BootstrapKubeconfigs by token id %q: %w", tokenID, err)
	}

	if len(list.Items) != 1 {
		return fmt.Errorf("exactly one BootstrapKubeconfig must have token id %q, found %d", tokenID, len(list.Items))
	}
	issuer := list.Items[0]

	if requested.TenantNamespace != issuer.Namespace {
		return fmt.Errorf("token %q was issued in namespace %q, but the request asks for tenant %q",
			tokenID, issuer.Namespace, requested.TenantNamespace)
	}

	if requested.NormalizedHostName != issuer.Spec.HostName {
		return fmt.Errorf("token %q was issued for host %q, but the request asks for host %q",
			tokenID, issuer.Spec.HostName, requested.NormalizedHostName)
	}

	return nil
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

// ByoCSRPredicate selects the CSRs the BYOH agent creates, which are named with
// the byoh-csr- prefix.
func ByoCSRPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return strings.HasPrefix(e.Object.GetName(), "byoh-csr-")
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return strings.HasPrefix(e.ObjectOld.GetName(), "byoh-csr-")
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoAdmissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &infrastructurev1beta1.BootstrapKubeconfig{},
		BootstrapKubeconfigTokenIDIndex, IndexBootstrapKubeconfigByTokenID)
	if err != nil {
		return fmt.Errorf("index BootstrapKubeconfig by %s: %w", BootstrapKubeconfigTokenIDIndex, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&certv1.CertificateSigningRequest{}).
		WithEventFilter(ByoCSRPredicate()).
		Complete(r)
}
