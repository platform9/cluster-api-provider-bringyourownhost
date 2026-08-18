// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/util"
)

const (
	byohctlContainerPath     = "/byohctl"
	byohctlBootstrapConfPath = "/bootstrap.conf"
)

var _ = Describe("When a host onboards via byohctl using a bootstrap kubeconfig, no Platform9 credentials involved [Byohctl]", func() {
	var (
		ctx              context.Context
		specName         = "byohctl-onboard"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		dockerClient     *client.Client
		byohostContainer *container.CreateResponse
		byoHostName      string
		agentLogFile     string
	)

	BeforeEach(func() {
		ctx = context.TODO()

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

		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed())

		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)

		var err error
		dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		byoHostName = fmt.Sprintf("byohctl-host-%s", util.RandomString(6))
		agentLogFile = fmt.Sprintf("/tmp/byohctl-onboard-%s.log", util.RandomString(6))
	})

	It("Should register a connected ByoHost with no Platform9 credentials ever placed on the host", func() {
		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			ByoHostName:           byoHostName,
			Namespace:             namespace.Name,
			DockerClient:          dockerClient,
			NetworkInterface:      dockerNetworkInterfaceKind,
			bootstrapClusterProxy: bootstrapClusterProxy,
			CommandArgs: map[string]string{
				agentFlagBootstrapKubeconfig: byohctlBootstrapConfPath,
			},
			BootstrapKubeconfigData: generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName),
		}

		created, err := runner.createDockerContainer()
		Expect(err).NotTo(HaveOccurred())
		byohostContainer = &created
		Expect(dockerClient.ContainerStart(ctx, byohostContainer.ID, container.StartOptions{})).To(Succeed())
		Expect(runner.raiseInotifyInstanceLimit(byohostContainer.ID)).To(Succeed())

		By("Copying byohctl and a bootstrap kubeconfig into the host container")
		binConfig := cpConfig{sourcePath: pathToByohctlBinary, destPath: byohctlContainerPath, container: byohostContainer.ID}
		Expect(copyToContainer(ctx, dockerClient, binConfig)).To(Succeed())
		listopt := container.ListOptions{Filters: filters.NewArgs()}
		Expect(runner.copyKubeconfig(binConfig, listopt)).To(Succeed())

		By("Running byohctl onboard --bootstrap-kubeconfig")
		execResp, err := dockerClient.ContainerExecCreate(ctx, byohostContainer.ID, container.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Env: []string{
				byohAgentBundleURLEnvVar + "=" + byohAgentBundleURLForContainers,
				byohAgentBundleInsecureEnvVar + "=1",
			},
			Cmd: []string{
				byohctlContainerPath, "onboard",
				"--bootstrap-kubeconfig", byohctlBootstrapConfPath,
				"--namespace", namespace.Name,
				"--region", "e2e-byohctl",
			},
		})
		Expect(err).NotTo(HaveOccurred())
		output, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
		Expect(err).NotTo(HaveOccurred())
		stopLog := StreamDockerLog(output, agentLogFile)
		defer stopLog()

		By("Checking the ByoHost comes up connected")
		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, byoHostName, specName,
			infrastructurev1beta1.AgentConnected,
		)
	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			ShowInfo([]string{agentLogFile})
		}
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, nil, e2eConfig.GetIntervals, skipCleanup)

		if dockerClient != nil && byohostContainer != nil {
			if err := dockerClient.ContainerStop(ctx, byohostContainer.ID, container.StopOptions{}); err != nil {
				Showf("error stopping container %s: %v", byohostContainer.ID, err)
			}
			if err := dockerClient.ContainerRemove(ctx, byohostContainer.ID, container.RemoveOptions{}); err != nil {
				Showf("error removing container %s: %v", byohostContainer.ID, err)
			}
		}

		if agentLogFile != "" {
			if err := os.Remove(agentLogFile); err != nil {
				Showf("error removing file %s: %v", agentLogFile, err)
			}
		}
	})
})
