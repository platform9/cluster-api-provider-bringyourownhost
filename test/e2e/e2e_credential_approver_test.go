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
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/certificate"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
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

		// The credential only allows creating requests. Decisions are read with the admin client.
		credentialClient *kubernetes.Clientset
		newCSR           func(identityOf identity.HostIdentity, organization []string, expirationSeconds *int32) *certv1.CertificateSigningRequest
		createCSR        func(identityOf identity.HostIdentity, organization []string, expirationSeconds *int32) string
		getCSR           func(csrName string) *certv1.CertificateSigningRequest
		conditionTypeOf  func(csrName string) func() []certv1.RequestConditionType
	)

	BeforeEach(func() {
		ctx = suiteCtx

		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed())

		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)

		hostName = fmt.Sprintf("credential-host-%s", util.RandomString(6))
		issued, kubeconfig = createBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)
		csrNames = nil

		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err = kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		newCSR = func(identityOf identity.HostIdentity, organization []string, expirationSeconds *int32) *certv1.CertificateSigningRequest {
			csrName := fmt.Sprintf("byoh-csr-%s-%s", identityOf.TenantNamespace, util.RandomString(8))
			csr, buildErr := builder.CertificateSigningRequest(csrName, identityOf.String(), identity.HostsGroup, credentialCSRKeySize).WithOrganization(organization...).Build()
			Expect(buildErr).NotTo(HaveOccurred())
			csr.Spec.ExpirationSeconds = expirationSeconds
			return csr
		}

		createCSR = func(identityOf identity.HostIdentity, organization []string, expirationSeconds *int32) string {
			csr := newCSR(identityOf, organization, expirationSeconds)
			_, createErr := credentialClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
			Expect(createErr).NotTo(HaveOccurred())
			csrNames = append(csrNames, csr.Name)
			return csr.Name
		}

		getCSR = func(csrName string) *certv1.CertificateSigningRequest {
			csr := &certv1.CertificateSigningRequest{}
			getErr := bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: csrName}, csr)
			Expect(getErr).NotTo(HaveOccurred())
			return csr
		}

		conditionTypeOf = func(csrName string) func() []certv1.RequestConditionType {
			return func() []certv1.RequestConditionType {
				csr := &certv1.CertificateSigningRequest{}
				if getErr := bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: csrName}, csr); getErr != nil {
					return nil
				}
				var conditionTypes []certv1.RequestConditionType
				for _, condition := range csr.Status.Conditions {
					conditionTypes = append(conditionTypes, condition.Type)
				}
				return conditionTypes
			}
		}
	})

	AfterEach(func() {
		for _, csrName := range csrNames {
			err := bootstrapClusterProxy.GetClient().Delete(ctx, &certv1.CertificateSigningRequest{ObjectMeta: metav1.ObjectMeta{Name: csrName}})
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting CSR %s: %v", csrName, err)
			}
		}

		if issued != nil {
			tokenSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Namespace: metav1.NamespaceSystem,
				Name:      "bootstrap-token-" + issued.Status.TokenID,
			}}
			err := bootstrapClusterProxy.GetClient().Delete(ctx, tokenSecret)
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting token Secret %s: %v", tokenSecret.Name, err)
			}
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
		own := identity.HostIdentity{
			TenantNamespace:    namespace.Name,
			NormalizedHostName: issued.Spec.HostName,
			Suffix:             util.RandomString(identity.SuffixLength),
		}
		otherHost := own
		otherHost.NormalizedHostName = "other-" + issued.Spec.HostName
		otherTenant := own
		otherTenant.TenantNamespace = "other-" + namespace.Name

		lifetime := ptr.To(int32(certificate.DefaultLifetimeSeconds))
		ownOrganization := identity.Organization(namespace.Name)
		ownCSR := createCSR(own, ownOrganization, lifetime)
		otherHostCSR := createCSR(otherHost, ownOrganization, lifetime)

		// The other tenant's request carries that tenant's own organizations, so
		// only the tenant check denies it.
		otherTenantCSR := createCSR(otherTenant, identity.Organization(otherTenant.TenantNamespace), lifetime)

		// A request for the own host with only the hosts group is denied for its
		// organization, before the token's approved request matters.
		hostsGroupOnly := own
		hostsGroupOnly.Suffix = util.RandomString(identity.SuffixLength)
		hostsGroupOnlyCSR := createCSR(hostsGroupOnly, []string{identity.HostsGroup}, lifetime)

		// The approver denies a request without a lifetime before it looks at
		// the issuer, so this request does not claim the token.
		noLifetime := own
		noLifetime.Suffix = util.RandomString(identity.SuffixLength)
		noLifetimeCSR := createCSR(noLifetime, ownOrganization, nil)

		intervals := e2eConfig.GetIntervals("", "wait-controllers")

		Eventually(conditionTypeOf(ownCSR), intervals...).Should(ContainElement(certv1.CertificateApproved))
		Eventually(conditionTypeOf(otherHostCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
		Eventually(conditionTypeOf(otherTenantCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
		Eventually(conditionTypeOf(noLifetimeCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
		Eventually(conditionTypeOf(hostsGroupOnlyCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))

		for _, csrName := range []string{otherHostCSR, otherTenantCSR, noLifetimeCSR, hostsGroupOnlyCSR} {
			csr := getCSR(csrName)
			Expect(csr.Status.Conditions).To(ContainElement(HaveField("Reason", credentialDeniedReason)))
		}
	})

	// The token has not claimed anything until a request is approved, so each spec below uses
	// the BootstrapKubeconfig of its own BeforeEach and starts with an unclaimed token.
	Context("with a token that has not approved a request yet", func() {
		var (
			own       identity.HostIdentity
			intervals []interface{}
		)

		// newOwnIdentity returns an identity for the host of the BootstrapKubeconfig with a new suffix.
		newOwnIdentity := func() identity.HostIdentity {
			return identity.HostIdentity{
				TenantNamespace:    namespace.Name,
				NormalizedHostName: issued.Spec.HostName,
				Suffix:             util.RandomString(identity.SuffixLength),
			}
		}

		BeforeEach(func() {
			own = newOwnIdentity()
			intervals = e2eConfig.GetIntervals("", "wait-controllers")
		})

		It("Should deny a request above the lifetime cap", func() {
			csrName := createCSR(own, identity.Organization(namespace.Name), ptr.To(int32(31536001)))

			Eventually(conditionTypeOf(csrName), intervals...).Should(ContainElement(certv1.CertificateDenied))
			Expect(getCSR(csrName).Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Reason", credentialDeniedReason),
				HaveField("Message", ContainSubstring("lifetime")),
			)))
		})

		It("Should deny a request whose Organizations are not exactly the hosts group and the own tenant group", func() {
			otherTenantGroup := []string{identity.HostsGroup, identity.TenantGroupPrefix + "other-" + namespace.Name}
			extraGroup := []string{identity.HostsGroup, identity.TenantGroupPrefix + namespace.Name, "byoh:extra-probe"}
			lifetime := ptr.To(int32(certificate.DefaultLifetimeSeconds))

			otherTenantGroupCSR := createCSR(own, otherTenantGroup, lifetime)
			extraGroupCSR := createCSR(newOwnIdentity(), extraGroup, lifetime)

			for _, csrName := range []string{otherTenantGroupCSR, extraGroupCSR} {
				Eventually(conditionTypeOf(csrName), intervals...).Should(ContainElement(certv1.CertificateDenied))
				Expect(getCSR(csrName).Status.Conditions).To(ContainElement(SatisfyAll(
					HaveField("Reason", credentialDeniedReason),
					HaveField("Message", ContainSubstring("organization")),
				)))
			}
		})

		It("Should approve one request per token", func() {
			lifetime := ptr.To(int32(certificate.DefaultLifetimeSeconds))
			organization := identity.Organization(namespace.Name)

			firstCSR := createCSR(own, organization, lifetime)
			Eventually(conditionTypeOf(firstCSR), intervals...).Should(ContainElement(certv1.CertificateApproved))

			secondCSR := createCSR(newOwnIdentity(), organization, lifetime)
			Eventually(conditionTypeOf(secondCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
			Expect(getCSR(secondCSR).Status.Conditions).To(ContainElement(HaveField("Reason", credentialDeniedReason)))
		})

		It("Should keep the credential Secret after a denied request and delete it after the approval", func() {
			credentialSecretKey := types.NamespacedName{Namespace: namespace.Name, Name: issued.Name + "-cred"}
			lifetime := ptr.To(int32(certificate.DefaultLifetimeSeconds))
			otherHost := own
			otherHost.NormalizedHostName = "other-" + issued.Spec.HostName

			deniedCSR := createCSR(otherHost, identity.Organization(namespace.Name), lifetime)
			Eventually(conditionTypeOf(deniedCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
			Consistently(func() error {
				return bootstrapClusterProxy.GetClient().Get(ctx, credentialSecretKey, &corev1.Secret{})
			}, "10s", "1s").Should(Succeed(), "a denied request must not delete the credential Secret")

			approvedCSR := createCSR(own, identity.Organization(namespace.Name), lifetime)
			Eventually(conditionTypeOf(approvedCSR), intervals...).Should(ContainElement(certv1.CertificateApproved))
			Eventually(func() bool {
				getErr := bootstrapClusterProxy.GetClient().Get(ctx, credentialSecretKey, &corev1.Secret{})
				return apierrors.IsNotFound(getErr)
			}, intervals...).Should(BeTrue(), "the approval must delete the credential Secret")
		})

		It("Should ignore a request for a signer the approver does not handle", func() {
			specStart := metav1.Now()
			lifetime := ptr.To(int32(certificate.DefaultLifetimeSeconds))

			foreignCSR := newCSR(own, identity.Organization(namespace.Name), lifetime)
			foreignCSR.Spec.SignerName = certv1.KubeAPIServerClientKubeletSignerName
			_, err := credentialClient.CertificatesV1().CertificateSigningRequests().Create(ctx, foreignCSR, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			csrNames = append(csrNames, foreignCSR.Name)

			// The approval of a request that follows shows the manager is running and has reconciled.
			ownCSR := createCSR(newOwnIdentity(), identity.Organization(namespace.Name), lifetime)
			Eventually(conditionTypeOf(ownCSR), intervals...).Should(ContainElement(certv1.CertificateApproved))

			Consistently(conditionTypeOf(foreignCSR.Name), "20s", "2s").Should(BeEmpty())

			logs, err := managerLogsSince(ctx, bootstrapClusterProxy.GetClientSet(), specStart)
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).NotTo(BeEmpty(), "no manager log lines were read, so the check below proves nothing")
			Expect(logLinesContaining(logs, "Denying CSR", foreignCSR.Name)).To(BeEmpty())
		})
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

	// Three rows show the issued credential is bound as expected on a real API server.
	// The full table runs in common/rbacharness.
	It("Should let the credential create certificate signing requests and nothing broader", func() {
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err := kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		testCases := []struct {
			attributes authorizationv1.ResourceAttributes
			allowed    bool
		}{
			{attributes: authorizationv1.ResourceAttributes{Verb: "create", Group: "certificates.k8s.io", Resource: "certificatesigningrequests"}, allowed: true},
			{attributes: authorizationv1.ResourceAttributes{Verb: "update", Group: "certificates.k8s.io", Resource: "certificatesigningrequests", Subresource: "approval"}, allowed: false},
			{attributes: authorizationv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: metav1.NamespaceSystem}, allowed: false},
		}

		for _, tc := range testCases {
			review, reviewErr := credentialClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &tc.attributes},
			}, metav1.CreateOptions{})
			Expect(reviewErr).NotTo(HaveOccurred())
			Expect(review.Status.Allowed).To(Equal(tc.allowed), "verb %q on %s/%s subresource %q in namespace %q",
				tc.attributes.Verb, tc.attributes.Group, tc.attributes.Resource, tc.attributes.Subresource, tc.attributes.Namespace)
		}
	})
})
