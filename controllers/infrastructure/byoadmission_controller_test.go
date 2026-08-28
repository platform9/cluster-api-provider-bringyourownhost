// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
)

// byohCSRName is named the way the agent names its requests, since the
// approver only looks at requests carrying that prefix.
var byohCSRName = "byoh-csr-" + defaultByoHostName

// byohCSRTokenID is the bootstrap token the requests in this file authenticate
// with. The enrollment created below records it, which is what lets the
// approver tie a request to the host it may claim.
const byohCSRTokenID = "abcdef"

var _ = Describe("Controllers/ByoadmissionController", func() {
	var (
		err        error
		CSR        *certv1.CertificateSigningRequest
		enrollment *infrastructurev1beta1.ByoHostEnrollment
	)

	It("should return error for non-existent CSR", func() {
		// Call Reconcile method for a non-existing CSR
		objectKey := types.NamespacedName{Name: defaultByoHostName}
		_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objectKey})
		Expect(err).To(BeNil())
	})

	Context("When a CSR is created", func() {
		BeforeEach(func() {
			ctx = context.Background()

			// The enrollment is what the approver resolves the request's
			// bootstrap token back to, so the host it claims can be checked.
			enrollment = &infrastructurev1beta1.ByoHostEnrollment{
				ObjectMeta: v1.ObjectMeta{
					Name:      defaultByoHostName,
					Namespace: defaultNamespace,
				},
			}
			Expect(k8sManager.GetClient().Create(ctx, enrollment)).ToNot(HaveOccurred())
			enrollment.Status.TokenID = byohCSRTokenID
			Expect(k8sManager.GetClient().Status().Update(ctx, enrollment)).ToNot(HaveOccurred())

			// The approver resolves the token through the manager's cache, so
			// the reconcile below must not run before the index sees it.
			Eventually(func() int {
				enrollments := &infrastructurev1beta1.ByoHostEnrollmentList{}
				if listErr := k8sManager.GetClient().List(ctx, enrollments,
					client.MatchingFields{controllers.EnrollmentTokenIDIndex: byohCSRTokenID}); listErr != nil {
					return 0
				}
				return len(enrollments.Items)
			}).Should(Equal(1))

			commonName := infrastructurev1beta1.HostCommonNamePrefix + defaultByoHostName

			// Create a CSR resource for each test
			CSR, err = builder.CertificateSigningRequest(byohCSRName, commonName, "byoh:hosts", 2048).Build()
			Expect(err).NotTo(HaveOccurred())

			// The api-server fills these in from the authenticated request;
			// the fake client set does not, so the test supplies them.
			CSR.Spec.Groups = []string{infrastructurev1beta1.BootstrapTokenExtraGroups}
			CSR.Spec.Username = "system:bootstrap:" + byohCSRTokenID
		})

		It("should approve the Byoh CSR", func() {
			// Create a dummy CSR request
			_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, CSR, v1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Call Reconcile method
			objectKey := types.NamespacedName{Name: byohCSRName}
			_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objectKey})
			Expect(err).ShouldNot(HaveOccurred())

			// Fetch the updated CSR
			var updateByohCSR *certv1.CertificateSigningRequest
			updateByohCSR, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Get(ctx, byohCSRName, v1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(updateByohCSR.Status.Conditions).Should(ContainElement(certv1.CertificateSigningRequestCondition{
				Type:   certv1.CertificateApproved,
				Reason: "Approved by ByoAdmission Controller",
				Status: corev1.ConditionTrue,
			}))
		})

		It("should not approve a denied CSR", func() {
			// Create a fake denied CSR request
			CSR.Status.Conditions = append(CSR.Status.Conditions, certv1.CertificateSigningRequestCondition{
				Type: certv1.CertificateDenied,
			})

			_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, CSR, v1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Call Reconcile method
			objectKey := types.NamespacedName{Name: byohCSRName}
			_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objectKey})
			Expect(err).To(BeNil())
		})

		It("should not approve an already approved CSR", func() {
			// Create a fake approved CSR request
			CSR.Status.Conditions = append(CSR.Status.Conditions, certv1.CertificateSigningRequestCondition{
				Type: certv1.CertificateApproved,
			})

			_, err = clientSetFake.CertificatesV1().CertificateSigningRequests().Create(ctx, CSR, v1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Call Reconcile method
			objectKey := types.NamespacedName{Name: byohCSRName}
			_, err = byoAdmissionReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: objectKey})
			Expect(err).To(BeNil())
		})

		AfterEach(func() {
			Expect(clientSetFake.CertificatesV1().CertificateSigningRequests().Delete(ctx, byohCSRName, v1.DeleteOptions{})).ShouldNot(HaveOccurred())
			Expect(k8sManager.GetClient().Delete(ctx, enrollment)).ShouldNot(HaveOccurred())
		})

	})

})
