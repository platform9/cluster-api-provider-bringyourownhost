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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
)

// byohCSRName is named the way the agent names its requests, since the
// approver only looks at requests carrying that prefix.
var byohCSRName = "byoh-csr-" + defaultByoHostName

var _ = Describe("Controllers/ByoadmissionController", func() {
	var (
		err error
		CSR *certv1.CertificateSigningRequest
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

			// Create a CSR resource for each test
			CSR, err = builder.CertificateSigningRequest(byohCSRName, "test-cn", "test-org", 2048).Build()
			Expect(err).NotTo(HaveOccurred())

			// The api-server fills these in from the authenticated request;
			// the fake client set does not, so the test supplies them.
			CSR.Spec.Groups = []string{infrastructurev1beta1.BootstrapTokenExtraGroups}
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
		})

	})

})
