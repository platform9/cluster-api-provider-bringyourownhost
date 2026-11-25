// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

// creating a workload cluster
// This test is meant to provide a first, fast signal to detect regression; it is recommended to use it as a PR blocker test.
var _ = Describe("When BYOH joins existing cluster [PR-Blocking]", func() {

	var (
		ctx              context.Context
		specName         = "quick-start"
		namespace        *corev1.Namespace
		clusterName      string
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		dockerClient     *client.Client
		err              error
		hosts            []byoHostHandle
		agentLogFile1    string
		agentLogFile2    string
		byoHostName1     string
		byoHostName2     string
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should create a workload cluster with single BYOH host", func() {
		clusterName = fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		hosts, err = spinUpByoHosts(ctx, dockerClient, namespace.Name, 2)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
		}
		byoHostName1, agentLogFile1 = hosts[0].Name, hosts[0].LogFilePath
		byoHostName2, agentLogFile2 = hosts[1].Name, hosts[1].LogFilePath

		specStart := metav1.Now()

		setControlPlaneIP(ctx, dockerClient)
		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, e2eConfig.GetVariableOrEmpty(KubernetesVersion), clusterctl.DefaultFlavor, 1, 1, clusterResources)

		By("Checking the ByoHost conditions reflect a successful join")
		for _, byoHostName := range []string{byoHostName1, byoHostName2} {
			AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, byoHostName, specName,
				infrastructurev1beta1.AgentConnected,
				infrastructurev1beta1.K8sComponentsInstallationSucceeded,
				infrastructurev1beta1.K8sNodeBootstrapSucceeded,
			)
		}

		By("Checking the bound hosts hold access to their own Secrets only")
		assertHostsHoldOwnSecretsOnly(ctx, namespace.Name, specStart, byoHostName1, byoHostName2)
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			ShowInfo([]string{agentLogFile1, agentLogFile2})
		}
	})

	AfterEach(func() {
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		teardownByoHosts(ctx, dockerClient, hosts)
	})
})

// assertHostsHoldOwnSecretsOnly checks, for each host, the Secret rule of its host Role and what the
// API server lets the host read. A host bound to a Machine may get the Machine's bootstrap data
// Secret and its install and uninstall Secrets, and nothing else. A host bound to no Machine has no
// rule on Secrets. managerStart is the time the manager is searched for log lines from.
func assertHostsHoldOwnSecretsOnly(ctx context.Context, namespace string, managerStart metav1.Time, containerHostNames ...string) {
	adminClient := bootstrapClusterProxy.GetClient()
	intervals := e2eConfig.GetIntervals("", "wait-controllers")

	byoHosts := make([]*infrastructurev1beta1.ByoHost, 0, len(containerHostNames))
	for _, containerHostName := range containerHostNames {
		byoHost, err := findByoHost(ctx, adminClient, namespace, containerHostName)
		Expect(err).NotTo(HaveOccurred())
		byoHosts = append(byoHosts, byoHost)
	}

	// boundSecrets maps the name of each bound ByoHost to the three Secret names its Role must list.
	boundSecrets := map[string][]string{}
	for _, byoHost := range byoHosts {
		if byoHost.Status.MachineRef != nil {
			boundSecrets[byoHost.Name] = boundHostSecretNames(ctx, namespace, byoHost)
		}
	}

	for _, byoHost := range byoHosts {
		role := &rbacv1.Role{}
		roleKey := types.NamespacedName{Namespace: namespace, Name: "byoh-host-" + byoHost.Name}

		wantNames, bound := boundSecrets[byoHost.Name]
		if !bound {
			err := adminClient.Get(ctx, roleKey, role)
			Expect(err).NotTo(HaveOccurred())
			for _, rule := range role.Rules {
				Expect(rule.Resources).NotTo(ContainElement("secrets"), "the Role of unbound ByoHost %s has a rule on Secrets", byoHost.Name)
			}
			continue
		}

		Eventually(func() []string {
			if getErr := adminClient.Get(ctx, roleKey, role); getErr != nil {
				return nil
			}
			for _, rule := range role.Rules {
				if len(rule.Resources) == 1 && rule.Resources[0] == "secrets" {
					return rule.ResourceNames
				}
			}
			return nil
		}, intervals...).Should(ConsistOf(wantNames), "Secret names in the Role of ByoHost %s", byoHost.Name)

		for _, rule := range role.Rules {
			if len(rule.Resources) == 1 && rule.Resources[0] == "secrets" {
				Expect(rule.Verbs).To(Equal([]string{"get"}))
			}
		}

		assertHostReadsOnly(ctx, namespace, byoHost, wantNames, boundSecrets)
	}

	logs, err := managerLogsSince(ctx, bootstrapClusterProxy.GetClientSet(), managerStart)
	Expect(err).NotTo(HaveOccurred())
	Expect(logs).NotTo(BeEmpty(), "no manager log lines were read, so the check below proves nothing")
	for _, byoHost := range byoHosts {
		Expect(logLinesContaining(logs, "ByoHost has no host-scoped Role", byoHost.Name)).To(BeEmpty())
	}
}

// boundHostSecretNames returns the three Secret names that the Role of byoHost must list: the bootstrap
// data Secret of its Machine and the install and uninstall Secrets of its K8sInstallerConfig. The
// ByoHost spec holds them, and the names are checked against the Machine and each other.
func boundHostSecretNames(ctx context.Context, namespace string, byoHost *infrastructurev1beta1.ByoHost) []string {
	adminClient := bootstrapClusterProxy.GetClient()
	intervals := e2eConfig.GetIntervals("", "wait-controllers")

	// The manager writes the Secret references to the ByoHost spec after it creates the Role.
	Eventually(func() error {
		if getErr := adminClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: byoHost.Name}, byoHost); getErr != nil {
			return getErr
		}
		if byoHost.Spec.BootstrapSecret == nil || byoHost.Spec.InstallationSecret == nil || byoHost.Spec.UninstallationSecret == nil {
			return fmt.Errorf("ByoHost %s has not got all three Secret references yet", byoHost.Name)
		}
		return nil
	}, intervals...).Should(Succeed())

	byoMachine := &infrastructurev1beta1.ByoMachine{}
	err := adminClient.Get(ctx, types.NamespacedName{Namespace: byoHost.Status.MachineRef.Namespace, Name: byoHost.Status.MachineRef.Name}, byoMachine)
	Expect(err).NotTo(HaveOccurred())
	machine, err := util.GetOwnerMachine(ctx, adminClient, byoMachine.ObjectMeta)
	Expect(err).NotTo(HaveOccurred())
	Expect(machine).NotTo(BeNil(), "ByoMachine %s has no owner Machine", byoMachine.Name)
	Expect(machine.Spec.Bootstrap.DataSecretName).NotTo(BeNil())

	bootstrapSecret := byoHost.Spec.BootstrapSecret.Name
	installSecret := byoHost.Spec.InstallationSecret.Name
	uninstallSecret := byoHost.Spec.UninstallationSecret.Name
	Expect(bootstrapSecret).To(Equal(*machine.Spec.Bootstrap.DataSecretName))
	installerConfigName, hasInstallPrefix := strings.CutPrefix(installSecret, "byoh-install-")
	Expect(hasInstallPrefix).To(BeTrue(), "install Secret %q is not named byoh-install-<K8sInstallerConfig name>", installSecret)
	Expect(uninstallSecret).To(Equal("byoh-uninstall-" + installerConfigName))

	return []string{bootstrapSecret, installSecret, uninstallSecret}
}

// assertHostReadsOnly checks with the identity of byoHost that the API server lets it get the Secrets in
// wantNames and refuses every other read of Secrets, including the Machine Secrets of the other bound hosts.
func assertHostReadsOnly(ctx context.Context, namespace string, byoHost *infrastructurev1beta1.ByoHost, wantNames []string, boundSecrets map[string][]string) {
	intervals := e2eConfig.GetIntervals("", "wait-controllers")

	hostCN := byoHost.Spec.Identity
	Expect(hostCN).NotTo(BeEmpty(), "ByoHost %s has no identity", byoHost.Name)
	host, err := hostClient(bootstrapClusterProxy.GetRESTConfig(), hostCN, namespace)
	Expect(err).NotTo(HaveOccurred())

	getSecret := func(name string) *authorizationv1.ResourceAttributes {
		return &authorizationv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: namespace, Name: name}
	}

	for _, name := range wantNames {
		Eventually(func() (bool, error) {
			return hostAllowed(ctx, host, getSecret(name))
		}, intervals...).Should(BeTrue(), "ByoHost %s must be allowed to get Secret %s", byoHost.Name, name)
	}

	denied := map[string]*authorizationv1.ResourceAttributes{
		"the token Secret":    getSecret("byoh-token"),
		"a credential Secret": getSecret("probe-cred"),
		"list Secrets":        {Verb: "list", Resource: "secrets", Namespace: namespace},
	}
	for otherName, otherSecrets := range boundSecrets {
		if otherName != byoHost.Name {
			denied["the Machine Secret of ByoHost "+otherName] = getSecret(otherSecrets[0])
		}
	}
	for what, attrs := range denied {
		allowed, reviewErr := hostAllowed(ctx, host, attrs)
		Expect(reviewErr).NotTo(HaveOccurred())
		Expect(allowed).To(BeFalse(), "ByoHost %s must not be allowed %s", byoHost.Name, what)
	}
}
