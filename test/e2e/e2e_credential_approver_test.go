// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	"github.com/platform9/cluster-api-provider-bringyourownhost/test/builder"
	authorizationv1 "k8s.io/api/authorization/v1"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// credentialCSROrganization is the organization the agent puts in its certificate request.
	credentialCSROrganization = "byoh:hosts"
	// credentialCSRKeySize is the RSA key size of the certificate requests these specs create.
	credentialCSRKeySize = 2048
	// credentialDeniedReason is the reason the approver sets on the Denied condition.
	credentialDeniedReason = "DeniedByByoAdmissionController"
	// credentialTokenLifetimeTolerance is how far the recorded token lifetime may exceed the configured one.
	// The creation time has second precision and the controller reconciles after the object is created.
	credentialTokenLifetimeTolerance = 5 * time.Second
)

var _ = Describe("When a BootstrapKubeconfig issues a credential [Credential]", func() {
	var (
		ctx           context.Context
		specName      = "credential-approver"
		namespace     *corev1.Namespace
		cancelWatches context.CancelFunc
		hostName      string
		issued        *infrastructurev1beta1.BootstrapKubeconfig
		kubeconfig    string
		csrNames      []string
	)

	BeforeEach(func() {
		ctx = suiteCtx

		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed())

		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)

		hostName = fmt.Sprintf("credential-host-%s", util.RandomString(6))
		issued, kubeconfig = createBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)
		csrNames = nil
	})

	AfterEach(func() {
		for _, csrName := range csrNames {
			err := bootstrapClusterProxy.GetClient().Delete(ctx, &certv1.CertificateSigningRequest{ObjectMeta: metav1.ObjectMeta{Name: csrName}})
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting CSR %s: %v", csrName, err)
			}
		}

		tokenSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceSystem,
			Name:      "bootstrap-token-" + issued.Status.TokenID,
		}}
		err := bootstrapClusterProxy.GetClient().Delete(ctx, tokenSecret)
		if err != nil && !apierrors.IsNotFound(err) {
			Showf("error deleting token Secret %s: %v", tokenSecret.Name, err)
		}

		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, nil, e2eConfig.GetIntervals, skipCleanup)
	})

	It("Should publish a credential Secret owned by the BootstrapKubeconfig", func() {
		adminClient := bootstrapClusterProxy.GetClient()

		credentialSecret := &corev1.Secret{}
		err := adminClient.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: issued.Name + "-cred"}, credentialSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(credentialSecret.Data).To(HaveKey("kubeconfig"))
		Expect(string(credentialSecret.Data["kubeconfig"])).To(Equal(kubeconfig))
		Expect(issued.Status.CredentialSecretRef).To(Equal(credentialSecret.Name))

		controllerRef := metav1.GetControllerOf(credentialSecret)
		Expect(controllerRef).NotTo(BeNil(), "credential Secret has no controller owner")
		Expect(controllerRef.UID).To(Equal(issued.UID))

		Expect(issued.Status.TokenID).NotTo(BeEmpty())
		tokenSecret := &corev1.Secret{}
		err = adminClient.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceSystem, Name: "bootstrap-token-" + issued.Status.TokenID}, tokenSecret)
		Expect(err).NotTo(HaveOccurred())

		Expect(issued.Status.TokenExpiresAt).NotTo(BeNil())
		Expect(issued.Status.TokenExpiresAt.UTC().Format(time.RFC3339)).To(Equal(string(tokenSecret.Data["expiration"])))
	})

	It("Should approve a request for its own host and deny a request for another host or tenant", func() {
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err := kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		// The credential only allows creating requests. Decisions are read with the admin client.
		createCSR := func(identityOf identity.HostIdentity) string {
			csrName := fmt.Sprintf("byoh-csr-%s-%s", identityOf.TenantNamespace, util.RandomString(8))
			csr, buildErr := builder.CertificateSigningRequest(csrName, identityOf.String(), credentialCSROrganization, credentialCSRKeySize).Build()
			Expect(buildErr).NotTo(HaveOccurred())
			_, createErr := credentialClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
			Expect(createErr).NotTo(HaveOccurred())
			csrNames = append(csrNames, csrName)
			return csrName
		}

		own := identity.HostIdentity{
			TenantNamespace:    namespace.Name,
			NormalizedHostName: issued.Spec.HostName,
			Suffix:             util.RandomString(identity.SuffixLength),
		}
		otherHost := own
		otherHost.NormalizedHostName = "other-" + issued.Spec.HostName
		otherTenant := own
		otherTenant.TenantNamespace = "other-" + namespace.Name

		ownCSR := createCSR(own)
		otherHostCSR := createCSR(otherHost)
		otherTenantCSR := createCSR(otherTenant)

		conditionTypeOf := func(csrName string) func() []certv1.RequestConditionType {
			return func() []certv1.RequestConditionType {
				csr := &certv1.CertificateSigningRequest{}
				if err := bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: csrName}, csr); err != nil {
					return nil
				}
				var conditionTypes []certv1.RequestConditionType
				for _, condition := range csr.Status.Conditions {
					conditionTypes = append(conditionTypes, condition.Type)
				}
				return conditionTypes
			}
		}
		intervals := e2eConfig.GetIntervals("", "wait-controllers")

		Eventually(conditionTypeOf(ownCSR), intervals...).Should(ContainElement(certv1.CertificateApproved))
		Eventually(conditionTypeOf(otherHostCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
		Eventually(conditionTypeOf(otherTenantCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))

		for _, csrName := range []string{otherHostCSR, otherTenantCSR} {
			csr := &certv1.CertificateSigningRequest{}
			err := bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: csrName}, csr)
			Expect(err).NotTo(HaveOccurred())
			Expect(csr.Status.Conditions).To(ContainElement(HaveField("Reason", credentialDeniedReason)))
		}
	})

	It("Should record the requester as createdBy and issue a token with the configured lifetime", func() {
		// The bootstrap cluster is too old for SelfSubjectReview, so the requester is the common name
		// of the admin client certificate the API server authenticates the create request with.
		adminCert, _ := pem.Decode(bootstrapClusterProxy.GetRESTConfig().CertData)
		Expect(adminCert).NotTo(BeNil(), "the bootstrap cluster admin credential has no client certificate")
		parsedAdminCert, err := x509.ParseCertificate(adminCert.Bytes)
		Expect(err).NotTo(HaveOccurred())
		Expect(parsedAdminCert.Subject.CommonName).NotTo(BeEmpty())
		Expect(issued.Spec.CreatedBy).To(Equal(parsedAdminCert.Subject.CommonName), "the mutating webhook did not set createdBy from the requester")

		Expect(issued.Status.TokenExpiresAt).NotTo(BeNil())
		lifetime := issued.Status.TokenExpiresAt.Sub(issued.CreationTimestamp.Time)
		Expect(lifetime.Seconds()).To(BeNumerically("~", infrastructurev1beta1.DefaultTokenExpiry.Seconds(), credentialTokenLifetimeTolerance.Seconds()), "token lifetime was %s", lifetime)
	})

	It("Should reject a host name that is not normalized", func() {
		notNormalized := builder.BootstrapKubeconfig(namespace.Name, "not-normalized-"+util.RandomString(5)).Build()
		notNormalized.Spec.HostName = "Not_Normalized"

		err := bootstrapClusterProxy.GetClient().Create(ctx, notNormalized)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("vbootstrapkubeconfig.kb.io"))
		Expect(err.Error()).To(ContainSubstring("host name is not normalized"))
	})

	It("Should only allow the credential to create, get, list and watch certificate signing requests", func() {
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err := kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		testCases := []struct {
			attributes authorizationv1.ResourceAttributes
			allowed    bool
		}{
			{attributes: authorizationv1.ResourceAttributes{Verb: "create", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: true},
			{attributes: authorizationv1.ResourceAttributes{Verb: "get", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: true},
			{attributes: authorizationv1.ResourceAttributes{Verb: "list", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: true},
			{attributes: authorizationv1.ResourceAttributes{Verb: "watch", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: true},
			{attributes: authorizationv1.ResourceAttributes{Verb: "update", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "patch", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "delete", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "deletecollection", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "update", Group: "certificates.k8s.io", Resource: "certificatesigningrequests", Subresource: "approval"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "update", Group: "certificates.k8s.io", Resource: "certificatesigningrequests", Subresource: "status"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "approve", Group: "certificates.k8s.io", Resource: "signers", Name: certv1.KubeAPIServerClientSignerName}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: metav1.NamespaceSystem}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: namespace.Name}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "create", Group: infrastructurev1beta1.GroupVersion.Group, Resource: "bootstrapkubeconfigs", Namespace: namespace.Name}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "list", Group: infrastructurev1beta1.GroupVersion.Group, Resource: "byohosts", Namespace: namespace.Name}, allowed: false},
		}

		for _, tc := range testCases {
			review, reviewErr := credentialClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &tc.attributes},
			}, metav1.CreateOptions{})
			Expect(reviewErr).NotTo(HaveOccurred())
			Expect(review.Status.Allowed).To(Equal(tc.allowed), "verb %q on %s/%s subresource %q name %q in namespace %q",
				tc.attributes.Verb, tc.attributes.Group, tc.attributes.Resource, tc.attributes.Subresource, tc.attributes.Name, tc.attributes.Namespace)
		}
	})
})
