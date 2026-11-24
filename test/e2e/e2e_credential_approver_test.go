// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/certificate"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	"github.com/platform9/cluster-api-provider-bringyourownhost/test/builder"
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
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err := kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		// The credential only allows creating requests. Decisions are read with the admin client.
		createCSR := func(identityOf identity.HostIdentity, organization []string, expirationSeconds *int32) string {
			csrName := fmt.Sprintf("byoh-csr-%s-%s", identityOf.TenantNamespace, util.RandomString(8))
			csr, buildErr := builder.CertificateSigningRequest(csrName, identityOf.String(), identity.HostsGroup, credentialCSRKeySize).WithOrganization(organization...).Build()
			Expect(buildErr).NotTo(HaveOccurred())
			csr.Spec.ExpirationSeconds = expirationSeconds
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
		Eventually(conditionTypeOf(noLifetimeCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))
		Eventually(conditionTypeOf(hostsGroupOnlyCSR), intervals...).Should(ContainElement(certv1.CertificateDenied))

		for _, csrName := range []string{otherHostCSR, otherTenantCSR, noLifetimeCSR, hostsGroupOnlyCSR} {
			csr := &certv1.CertificateSigningRequest{}
			err := bootstrapClusterProxy.GetClient().Get(ctx, client.ObjectKey{Name: csrName}, csr)
			Expect(err).NotTo(HaveOccurred())
			Expect(csr.Status.Conditions).To(ContainElement(HaveField("Reason", credentialDeniedReason)))
		}
	})
})
