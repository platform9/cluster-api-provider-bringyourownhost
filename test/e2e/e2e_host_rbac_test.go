// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"

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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// hostRBACOperator is an email-like user that the ByoHost webhook lets create and delete ByoHosts.
// A host certificate would be the real creator, but it needs the agent and a signed certificate.
const hostRBACOperator = "host-rbac-e2e@example.com"

// onboardedHost is a host whose request the approver approved.
type onboardedHost struct {
	identity identity.HostIdentity
	issued   *infrastructurev1beta1.BootstrapKubeconfig
}

var _ = Describe("When the approver creates the RBAC of a host [HostRBAC]", func() {
	var (
		ctx           context.Context
		specName      = "host-rbac"
		namespace     *corev1.Namespace
		cancelWatches context.CancelFunc
		adminClient   client.Client
		kube          kubernetes.Interface
		operator      client.Client
		onboarded     []*onboardedHost
		csrNames      []string
		byoHostNames  []string
		intervals     []interface{}
	)

	// onboardHost issues a token for a new host and approves one request of that token.
	onboardHost := func() *onboardedHost {
		hostName := fmt.Sprintf("rbac-host-%s", util.RandomString(6))
		issued, kubeconfig := createBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)

		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		Expect(err).NotTo(HaveOccurred())
		credentialClient, err := kubernetes.NewForConfig(restConfig)
		Expect(err).NotTo(HaveOccurred())

		host := &onboardedHost{
			identity: identity.HostIdentity{
				TenantNamespace:    namespace.Name,
				NormalizedHostName: issued.Spec.HostName,
				Suffix:             util.RandomString(identity.SuffixLength),
			},
			issued: issued,
		}
		onboarded = append(onboarded, host)

		csrName := fmt.Sprintf("byoh-csr-%s-%s", namespace.Name, util.RandomString(8))
		csr, err := builder.CertificateSigningRequest(csrName, host.identity.String(), identity.HostsGroup, credentialCSRKeySize).
			WithOrganization(identity.Organization(namespace.Name)...).Build()
		Expect(err).NotTo(HaveOccurred())
		csr.Spec.ExpirationSeconds = ptr.To(int32(certificate.DefaultLifetimeSeconds))
		_, err = credentialClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		csrNames = append(csrNames, csrName)

		Eventually(func() []certv1.RequestConditionType {
			got := &certv1.CertificateSigningRequest{}
			if getErr := adminClient.Get(ctx, client.ObjectKey{Name: csrName}, got); getErr != nil {
				return nil
			}
			var types []certv1.RequestConditionType
			for _, condition := range got.Status.Conditions {
				types = append(types, condition.Type)
			}
			return types
		}, intervals...).Should(ContainElement(certv1.CertificateApproved))

		return host
	}

	// createByoHost registers the ByoHost of host the way the agent does after its certificate is signed.
	createByoHost := func(host *onboardedHost) *infrastructurev1beta1.ByoHost {
		byoHost := &infrastructurev1beta1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace.Name, Name: host.identity.ByoHostName()},
		}
		err := operator.Create(ctx, byoHost)
		Expect(err).NotTo(HaveOccurred())
		byoHostNames = append(byoHostNames, byoHost.Name)

		return byoHost
	}

	rbacOwners := func(byoHostName string) func() ([]metav1.OwnerReference, error) {
		return func() ([]metav1.OwnerReference, error) {
			role, err := kube.RbacV1().Roles(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			binding, err := kube.RbacV1().RoleBindings(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			return append(role.OwnerReferences, binding.OwnerReferences...), nil
		}
	}

	rbacExists := func(byoHostName string) error {
		_, err := kube.RbacV1().Roles(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		_, err = kube.RbacV1().RoleBindings(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})

		return err
	}

	rbacGone := func(byoHostName string) bool {
		_, roleErr := kube.RbacV1().Roles(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
		_, bindingErr := kube.RbacV1().RoleBindings(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})

		return apierrors.IsNotFound(roleErr) && apierrors.IsNotFound(bindingErr)
	}

	BeforeEach(func() {
		ctx = suiteCtx

		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed())

		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)
		adminClient = bootstrapClusterProxy.GetClient()
		kube = bootstrapClusterProxy.GetClientSet()
		intervals = e2eConfig.GetIntervals("", "wait-controllers")
		onboarded, csrNames, byoHostNames = nil, nil, nil

		operatorConfig := rest.CopyConfig(bootstrapClusterProxy.GetRESTConfig())
		operatorConfig.Impersonate = rest.ImpersonationConfig{UserName: hostRBACOperator, Groups: []string{"system:masters"}}
		var err error
		operator, err = client.New(operatorConfig, client.Options{Scheme: bootstrapClusterProxy.GetScheme()})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		for _, byoHostName := range byoHostNames {
			err := adminClient.Delete(ctx, &infrastructurev1beta1.ByoHost{ObjectMeta: metav1.ObjectMeta{Namespace: namespace.Name, Name: byoHostName}})
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting ByoHost %s: %v", byoHostName, err)
			}
		}

		for _, csrName := range csrNames {
			err := adminClient.Delete(ctx, &certv1.CertificateSigningRequest{ObjectMeta: metav1.ObjectMeta{Name: csrName}})
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting CSR %s: %v", csrName, err)
			}
		}

		for _, host := range onboarded {
			tokenSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Namespace: metav1.NamespaceSystem,
				Name:      "bootstrap-token-" + host.issued.Status.TokenID,
			}}
			err := adminClient.Delete(ctx, tokenSecret)
			if err != nil && !apierrors.IsNotFound(err) {
				Showf("error deleting token Secret %s: %v", tokenSecret.Name, err)
			}
		}

		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, nil, e2eConfig.GetIntervals, skipCleanup)
	})

	It("Should create the host Role and RoleBinding at approval, owned by the ByoHost after it registers", func() {
		host := onboardHost()
		byoHostName := host.identity.ByoHostName()

		for _, labeled := range []metav1.Object{
			func() metav1.Object {
				role, err := kube.RbacV1().Roles(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				return role
			}(),
			func() metav1.Object {
				binding, err := kube.RbacV1().RoleBindings(namespace.Name).Get(ctx, "byoh-host-"+byoHostName, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				return binding
			}(),
		} {
			Expect(labeled.GetLabels()).To(HaveKeyWithValue("byoh.platform9.io/tenant", namespace.Name))
		}

		byoHost := createByoHost(host)

		Eventually(rbacOwners(byoHostName), intervals...).Should(SatisfyAll(
			HaveLen(2),
			HaveEach(SatisfyAll(
				HaveField("Kind", "ByoHost"),
				HaveField("UID", byoHost.UID),
			)),
		))
	})

	It("Should keep the host Role when the BootstrapKubeconfig is deleted while the host is registered", func() {
		host := onboardHost()
		byoHostName := host.identity.ByoHostName()
		byoHost := createByoHost(host)
		Eventually(rbacOwners(byoHostName), intervals...).Should(HaveEach(HaveField("UID", byoHost.UID)))

		err := adminClient.Delete(ctx, host.issued)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			getErr := adminClient.Get(ctx, client.ObjectKeyFromObject(host.issued), &infrastructurev1beta1.BootstrapKubeconfig{})
			return apierrors.IsNotFound(getErr)
		}, intervals...).Should(BeTrue())

		Consistently(func() error { return rbacExists(byoHostName) }, "15s", "1s").Should(Succeed())
	})

	It("Should remove the host Role and RoleBinding with the ByoHost and leave other hosts alone", func() {
		first := onboardHost()
		second := onboardHost()
		firstByoHost := createByoHost(first)
		secondByoHost := createByoHost(second)
		for _, byoHost := range []*infrastructurev1beta1.ByoHost{firstByoHost, secondByoHost} {
			Eventually(rbacOwners(byoHost.Name), intervals...).Should(HaveEach(HaveField("UID", byoHost.UID)))
		}

		err := operator.Delete(ctx, firstByoHost)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool { return rbacGone(firstByoHost.Name) }, intervals...).Should(BeTrue())
		Consistently(func() error { return rbacExists(secondByoHost.Name) }, "15s", "1s").Should(Succeed())
	})

	It("Should allow the host only its own ByoHost", func() {
		host := onboardHost()
		own := host.identity.ByoHostName()
		other := "other-" + own

		hostKube, err := hostClient(bootstrapClusterProxy.GetRESTConfig(), host.identity.String(), namespace.Name)
		Expect(err).NotTo(HaveOccurred())

		byoHostRule := func(verb, subresource, name string) *authorizationv1.ResourceAttributes {
			return &authorizationv1.ResourceAttributes{
				Verb:        verb,
				Group:       "infrastructure.cluster.x-k8s.io",
				Resource:    "byohosts",
				Subresource: subresource,
				Namespace:   namespace.Name,
				Name:        name,
			}
		}

		allowed := []*authorizationv1.ResourceAttributes{
			byoHostRule("get", "", own),
			byoHostRule("update", "", own),
			byoHostRule("patch", "", own),
			byoHostRule("delete", "", own),
			byoHostRule("get", "status", own),
			byoHostRule("patch", "status", own),
		}
		for _, attrs := range allowed {
			Eventually(func() (bool, error) {
				return hostAllowed(ctx, hostKube, attrs)
			}, intervals...).Should(BeTrue(), "%s %s of the own ByoHost", attrs.Verb, attrs.Subresource)
		}

		denied := []*authorizationv1.ResourceAttributes{
			byoHostRule("get", "", other),
			byoHostRule("list", "", other),
			byoHostRule("patch", "", other),
			byoHostRule("list", "", ""),
		}
		for _, attrs := range denied {
			got, reviewErr := hostAllowed(ctx, hostKube, attrs)
			Expect(reviewErr).NotTo(HaveOccurred())
			Expect(got).To(BeFalse(), "%s of ByoHost %q", attrs.Verb, attrs.Name)
		}
	})
})
