// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	byomachinecontroller "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// agentServiceEnvFileInContainer is the environment file the agent systemd unit reads. The install
// script the agent runs reads BYOH_BUNDLE_REGISTRY_INSECURE from the agent's environment, and under
// systemd that is this file, not the container's environment.
const agentServiceEnvFileInContainer = "/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf"

var _ = Describe("When a host that backs a ByoMachine re-onboards via byohctl [Byohctl] [Reonboard] [ProviderID]", func() {
	var (
		ctx              context.Context
		specName         = "byohctl-reonboard-providerid"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		dockerClient     *dockerclient.Client
		poolHosts        []byoHostHandle
		byohctlHost      *container.CreateResponse
		hostName         string
		logFile          string
		allAgentLogFiles []string
	)

	BeforeEach(func() {
		if byohAgentBundleURLForContainers == "" {
			reason := fmt.Sprintf("no agent bundle available for byohctl's SetupAgent to install "+
				"(neither %s nor a local `make build-host-agent-deb` output was found; see "+
				"ensureLocalAgentBundleRegistry in e2e_agent_bundle_registry.go)", byohAgentBundleURLEnvVar)
			if os.Getenv("CI") != "" {
				// Should never happen in CI -- fail loudly instead of skipping quietly.
				Fail("in CI, this should never happen: " + reason)
			}
			Skip(reason)
		}

		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)

		hostName = fmt.Sprintf("example-worker-%s", util.RandomString(6))
		logFile = fmt.Sprintf("/tmp/byohctl-reonboard-providerid-%s.log", util.RandomString(6))
	})

	It("Should keep every ByoMachine Ready with a providerID that names its current ByoHost", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))
		mgmtClient := bootstrapClusterProxy.GetClient()

		var err error
		dockerClient, err = dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		// The byohctl host joins after the cluster exists, so it cannot be picked for the control plane.
		By("Creating a capacity pool of two hosts")
		poolHosts, err = spinUpByoHosts(ctx, dockerClient, namespace.Name, 2)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range poolHosts {
			defer host.StopLog()
			allAgentLogFiles = append(allAgentLogFiles, host.LogFilePath)
		}

		By("Creating a workload cluster with one control plane node and one worker node")
		setControlPlaneIP(ctx, dockerClient)
		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, e2eConfig.GetVariableOrEmpty(KubernetesVersion), clusterctl.DefaultFlavor, 1, 1, clusterResources)
		Expect(clusterResources.MachineDeployments).To(HaveLen(1))
		machineDeploymentKey := ctrlclient.ObjectKeyFromObject(clusterResources.MachineDeployments[0])

		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			ByoHostName:           hostName,
			Namespace:             namespace.Name,
			DockerClient:          dockerClient,
			NetworkInterface:      dockerNetworkInterfaceKind,
			Env:                   byoHostRunnerEnv(),
			bootstrapClusterProxy: bootstrapClusterProxy,
			CommandArgs: map[string]string{
				agentFlagBootstrapKubeconfig: byohctlBootstrapConfPath,
			},
		}
		created, err := runner.createDockerContainer()
		Expect(err).NotTo(HaveOccurred())
		byohctlHost = &created
		Expect(dockerClient.ContainerStart(ctx, byohctlHost.ID, container.StartOptions{})).To(Succeed())
		Expect(runner.raiseInotifyInstanceLimit(byohctlHost.ID)).To(Succeed())

		binConfig := cpConfig{sourcePath: pathToByohctlBinary, destPath: byohctlContainerPath, container: byohctlHost.ID}
		Expect(copyToContainer(ctx, dockerClient, binConfig)).To(Succeed())

		onboardHost := func() *infrastructurev1beta1.ByoHost {
			GinkgoHelper()

			runner.BootstrapKubeconfigData = generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)
			Expect(runner.copyKubeconfig(binConfig, container.ListOptions{Filters: filters.NewArgs()})).To(Succeed())
			runByohctlOnboard(ctx, dockerClient, byohctlHost.ID, namespace.Name, logFile)

			if bundleRepoInsecureForContainers {
				enableInsecureBundleRegistryForAgent(ctx, dockerClient, byohctlHost.ID)
			}

			AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName,
				infrastructurev1beta1.AgentConnected,
			)
			return waitForByoHost(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName)
		}

		// The control plane ByoMachine and the two worker ByoMachines.
		const byoMachineCount = 3

		By("Onboarding a third host via byohctl and scaling the workers out to use it")
		firstHost := onboardHost()
		scaleMachineDeployment(ctx, mgmtClient, machineDeploymentKey, 2)
		AssertByoMachinesReadyWithProviderIDOfTheirHost(ctx, mgmtClient, namespace.Name, byoMachineCount, specName)
		firstHostAttached := &infrastructurev1beta1.ByoHost{}
		Expect(mgmtClient.Get(ctx, ctrlclient.ObjectKeyFromObject(firstHost), firstHostAttached)).To(Succeed())
		Expect(firstHostAttached.Status.MachineRef).NotTo(BeNil(),
			"the byohctl host %s should back a ByoMachine, otherwise this spec does not exercise a re-onboard of a backing host", firstHost.Name)

		By("Decommissioning the host that backs a ByoMachine")
		runByohctl(ctx, dockerClient, byohctlHost.ID, logFile, nil, byohctlContainerPath, "decommission")
		firstHostKey := ctrlclient.ObjectKeyFromObject(firstHost)
		Eventually(func() bool {
			getErr := mgmtClient.Get(ctx, firstHostKey, &infrastructurev1beta1.ByoHost{})
			return apierrors.IsNotFound(getErr)
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected ByoHost %s to be deleted by decommission", firstHostKey)
		AssertByoMachinesReadyWithProviderIDOfTheirHost(ctx, mgmtClient, namespace.Name, byoMachineCount-1, specName)

		By("Re-onboarding the same host and scaling the workers back out")
		secondHost := onboardHost()
		Expect(secondHost.Name).NotTo(Equal(firstHost.Name), "re-onboarding should create a ByoHost with a new name")
		scaleMachineDeployment(ctx, mgmtClient, machineDeploymentKey, 2)

		By("Checking no ByoMachine is stuck and every providerID names its current ByoHost")
		AssertByoMachinesReadyWithProviderIDOfTheirHost(ctx, mgmtClient, namespace.Name, byoMachineCount, specName)
		secondHostAttached := &infrastructurev1beta1.ByoHost{}
		Expect(mgmtClient.Get(ctx, ctrlclient.ObjectKeyFromObject(secondHost), secondHostAttached)).To(Succeed())
		Expect(secondHostAttached.Status.MachineRef).NotTo(BeNil(),
			"the re-onboarded ByoHost %s should back a ByoMachine", secondHost.Name)
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			ShowInfo(append(allAgentLogFiles, logFile))
		}
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		teardownByoHosts(ctx, dockerClient, poolHosts)

		if dockerClient != nil && byohctlHost != nil {
			if err := dockerClient.ContainerStop(ctx, byohctlHost.ID, container.StopOptions{}); err != nil {
				Showf("error stopping container %s: %v", byohctlHost.ID, err)
			}
			if err := dockerClient.ContainerRemove(ctx, byohctlHost.ID, container.RemoveOptions{}); err != nil {
				Showf("error removing container %s: %v", byohctlHost.ID, err)
			}
		}

		if logFile != "" {
			if err := os.Remove(logFile); err != nil {
				Showf("error removing file %s: %v", logFile, err)
			}
		}
	})
})

// enableInsecureBundleRegistryForAgent lets the agent service on the host pull the Kubernetes
// bundle from the plaintext local registry, then restarts the service so it takes effect.
func enableInsecureBundleRegistryForAgent(ctx context.Context, dockerClient *dockerclient.Client, containerID string) {
	GinkgoHelper()

	appendEnv := "echo BYOH_BUNDLE_REGISTRY_INSECURE=1 >> " + agentServiceEnvFileInContainer
	output, exitCode, err := execInContainer(ctx, dockerClient, containerID, []string{"sh", "-c", appendEnv})
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(BeZero(), "failed to update %s: %s", agentServiceEnvFileInContainer, output)

	output, exitCode, err = execInContainer(ctx, dockerClient, containerID, []string{"systemctl", "restart", "pf9-byohost-agent"})
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(BeZero(), "failed to restart the agent service: %s", output)
}

// scaleMachineDeployment sets the replica count of the MachineDeployment. Unlike the framework
// helper it does not wait, so a stuck ByoMachine is reported by the ByoMachine assertions with
// its own reason instead of a generic timeout.
func scaleMachineDeployment(ctx context.Context, c ctrlclient.Client, key k8stypes.NamespacedName, replicas int32) {
	GinkgoHelper()

	machineDeployment := &clusterv1.MachineDeployment{}
	Expect(c.Get(ctx, key, machineDeployment)).To(Succeed())
	patchBase := machineDeployment.DeepCopy()
	machineDeployment.Spec.Replicas = ptr.To(replicas)
	Expect(c.Patch(ctx, machineDeployment, ctrlclient.MergeFrom(patchBase))).To(Succeed())
}

// AssertByoMachinesReadyWithProviderIDOfTheirHost polls until exactly wantCount ByoMachines exist
// in namespace and each one is Ready with a providerID whose host part is the name of the ByoHost
// attached to it. On timeout it reports every ByoMachine that does not satisfy that.
func AssertByoMachinesReadyWithProviderIDOfTheirHost(ctx context.Context, c ctrlclient.Client, namespace string, wantCount int, specName string) {
	GinkgoHelper()

	var problems []string
	Eventually(func() []string {
		problems = byoMachineProviderIDProblems(ctx, c, namespace, wantCount)
		return problems
	}, e2eConfig.GetIntervals(specName, "wait-worker-nodes")...).Should(BeEmpty(),
		"ByoMachines in namespace %s are not consistent: %s", namespace, strings.Join(problems, "; "))
}

// byoMachineProviderIDProblems returns one entry for each way the ByoMachines in namespace
// differ from the expected state, or nil when they match it.
func byoMachineProviderIDProblems(ctx context.Context, c ctrlclient.Client, namespace string, wantCount int) []string {
	byoMachines := &infrastructurev1beta1.ByoMachineList{}
	if err := c.List(ctx, byoMachines, ctrlclient.InNamespace(namespace)); err != nil {
		return []string{fmt.Sprintf("list ByoMachines: %v", err)}
	}
	byoHosts := &infrastructurev1beta1.ByoHostList{}
	if err := c.List(ctx, byoHosts, ctrlclient.InNamespace(namespace)); err != nil {
		return []string{fmt.Sprintf("list ByoHosts: %v", err)}
	}

	var problems []string
	if len(byoMachines.Items) != wantCount {
		problems = append(problems, fmt.Sprintf("want %d ByoMachines, found %d", wantCount, len(byoMachines.Items)))
	}

	for i := range byoMachines.Items {
		byoMachine := &byoMachines.Items[i]

		var attached []string
		for j := range byoHosts.Items {
			machineRef := byoHosts.Items[j].Status.MachineRef
			if machineRef != nil && machineRef.Name == byoMachine.Name {
				attached = append(attached, byoHosts.Items[j].Name)
			}
		}

		//nolint: staticcheck // SA1019: deprecated field, still the field the controller sets
		if !byoMachine.Status.Ready {
			problems = append(problems, fmt.Sprintf("ByoMachine %s is not Ready (providerID %q)", byoMachine.Name, byoMachine.Spec.ProviderID))
		}
		if len(attached) != 1 {
			problems = append(problems, fmt.Sprintf("ByoMachine %s has %d attached ByoHosts: %v", byoMachine.Name, len(attached), attached))
			continue
		}

		providerIDPattern := "^" + regexp.QuoteMeta(byomachinecontroller.ProviderIDPrefix+attached[0]+"/") + ".+$"
		if !regexp.MustCompile(providerIDPattern).MatchString(byoMachine.Spec.ProviderID) {
			problems = append(problems, fmt.Sprintf("ByoMachine %s has providerID %q, want host part %s", byoMachine.Name, byoMachine.Spec.ProviderID, attached[0]))
		}
	}

	return problems
}
