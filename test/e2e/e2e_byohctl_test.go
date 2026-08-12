// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/util"
)

const (
	// byohAgentBundleURLEnvVar mirrors the env var byohAgentBundleURL() in
	// cmd/byohctl/service/constants.go checks for an override. byohctl's real onboarding flow
	// downloads a published agent .deb bundle from quay.io tagged with byohctl's own git-describe
	// version; that tag only exists in the registry for commits already on main (or ci-* tags),
	// per .github/workflows/build-byohctl.yml. Without an override pointing at a bundle this run
	// can actually reach, this spec would fail on every ordinary PR branch for a reason unrelated
	// to what it's testing -- so it skips instead, until that bundle-hermeticity gap is closed.
	byohAgentBundleURLEnvVar = "BYOH_AGENT_BUNDLE_URL"
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

		if os.Getenv(byohAgentBundleURLEnvVar) == "" {
			Skip(fmt.Sprintf("skipping: set %s to an agent bundle reachable from this run "+
				"(byohctl's SetupAgent downloads a real published .deb via imgpkg; see the "+
				"byohAgentBundleURLEnvVar doc comment in this file)", byohAgentBundleURLEnvVar))
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
		Expect(dockerClient.ContainerStart(ctx, byohostContainer.ID, types.ContainerStartOptions{})).To(Succeed())
		Expect(runner.raiseInotifyInstanceLimit(byohostContainer.ID)).To(Succeed())

		By("Copying byohctl and a bootstrap kubeconfig into the host container")
		binConfig := cpConfig{sourcePath: pathToByohctlBinary, destPath: byohctlContainerPath, container: byohostContainer.ID}
		Expect(copyToContainer(ctx, dockerClient, binConfig)).To(Succeed())
		Expect(runner.copyKubeconfig(binConfig, types.ContainerListOptions{})).To(Succeed())

		By("Running byohctl onboard --bootstrap-kubeconfig")
		execResp, err := dockerClient.ContainerExecCreate(ctx, byohostContainer.ID, types.ExecConfig{
			AttachStdout: true,
			AttachStderr: true,
			Cmd: []string{
				byohctlContainerPath, "onboard",
				"--bootstrap-kubeconfig", byohctlBootstrapConfPath,
				"--namespace", namespace.Name,
				"--region", "e2e-byohctl",
			},
		})
		Expect(err).NotTo(HaveOccurred())
		output, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, types.ExecStartCheck{})
		Expect(err).NotTo(HaveOccurred())
		f := WriteDockerLog(output, agentLogFile)
		defer closeLogFile(f, agentLogFile)

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
			if err := dockerClient.ContainerRemove(ctx, byohostContainer.ID, types.ContainerRemoveOptions{}); err != nil {
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
