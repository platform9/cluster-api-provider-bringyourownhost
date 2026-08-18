// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

// creating a workload cluster
// This test is meant to provide a first, fast signal to detect regression; it is recommended to use it as a PR blocker test.
var _ = Describe("When BYOH joins existing cluster [PR-Blocking]", func() {

	var (
		ctx                 context.Context
		specName            = "quick-start"
		namespace           *corev1.Namespace
		clusterName         string
		cancelWatches       context.CancelFunc
		clusterResources    *clusterctl.ApplyClusterTemplateAndWaitResult
		dockerClient        *client.Client
		err                 error
		byohostContainerIDs []string
		agentLogFile1       = fmt.Sprintf("/tmp/host-agent1-%s.log", util.RandomString(6))
		agentLogFile2       = fmt.Sprintf("/tmp/host-agent2-%s.log", util.RandomString(6))
		byoHostName1        = fmt.Sprintf("byohost1-%s", util.RandomString(6))
		byoHostName2        = fmt.Sprintf("byohost2-%s", util.RandomString(6))
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should create a workload cluster with single BYOH host", func() {
		clusterName = fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			Namespace:             namespace.Name,
			PathToHostAgentBinary: pathToHostAgentBinary,
			DockerClient:          dockerClient,
			NetworkInterface:      dockerNetworkInterfaceKind,
			bootstrapClusterProxy: bootstrapClusterProxy,
			CommandArgs: map[string]string{
				agentFlagBootstrapKubeconfig: bootstrapConfPath,
				agentFlagNamespace:           namespace.Name,
				agentFlagVerbosity:           "1",
			},
		}

		var output types.HijackedResponse
		runner.ByoHostName = byoHostName1
		runner.BootstrapKubeconfigData = generateBootstrapKubeconfig(runner.Context, bootstrapClusterProxy, clusterConName)
		byohost, err := runner.SetupByoDockerHost()
		Expect(err).NotTo(HaveOccurred())
		output, byohostContainerID, err := runner.ExecByoDockerHost(byohost)
		Expect(err).NotTo(HaveOccurred())
		byohostContainerIDs = append(byohostContainerIDs, byohostContainerID)
		stopLog1 := StreamDockerLog(output, agentLogFile1)
		defer stopLog1()

		runner.ByoHostName = byoHostName2
		runner.BootstrapKubeconfigData = generateBootstrapKubeconfig(runner.Context, bootstrapClusterProxy, clusterConName)
		byohost, err = runner.SetupByoDockerHost()
		Expect(err).NotTo(HaveOccurred())
		output, byohostContainerID, err = runner.ExecByoDockerHost(byohost)
		Expect(err).NotTo(HaveOccurred())
		byohostContainerIDs = append(byohostContainerIDs, byohostContainerID)

		// read the log of host agent container in backend, and write it
		stopLog2 := StreamDockerLog(output, agentLogFile2)
		defer stopLog2()

		setControlPlaneIP(context.Background(), dockerClient)
		clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
			ClusterProxy: bootstrapClusterProxy,
			ConfigCluster: clusterctl.ConfigClusterInput{
				LogFolder:                filepath.Join(artifactFolder, "clusters", bootstrapClusterProxy.GetName()),
				ClusterctlConfigPath:     clusterctlConfigPath,
				KubeconfigPath:           bootstrapClusterProxy.GetKubeconfigPath(),
				InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
				Flavor:                   clusterctl.DefaultFlavor,
				Namespace:                namespace.Name,
				ClusterName:              clusterName,
				KubernetesVersion:        e2eConfig.GetVariableOrEmpty(KubernetesVersion),
				ControlPlaneMachineCount: ptr.To(int64(1)),
				WorkerMachineCount:       ptr.To(int64(1)),
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		By("Checking the ByoHost conditions reflect a successful join")
		for _, byoHostName := range []string{byoHostName1, byoHostName2} {
			AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, byoHostName, specName,
				infrastructurev1beta1.AgentConnected,
				infrastructurev1beta1.K8sComponentsInstallationSucceeded,
				infrastructurev1beta1.K8sNodeBootstrapSucceeded,
			)
		}
	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			ShowInfo([]string{agentLogFile1, agentLogFile2})
		}
	})

	AfterEach(func() {
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		if dockerClient != nil && len(byohostContainerIDs) != 0 {
			for _, byohostContainerID := range byohostContainerIDs {
				err := dockerClient.ContainerStop(ctx, byohostContainerID, container.StopOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = dockerClient.ContainerRemove(ctx, byohostContainerID, container.RemoveOptions{})
				Expect(err).NotTo(HaveOccurred())
			}
		}

		err := os.Remove(agentLogFile1)
		if err != nil {
			Showf("error removing file %s: %v", agentLogFile1, err)
		}

		err = os.Remove(agentLogFile2)
		if err != nil {
			Showf("error removing file %s: %v", agentLogFile2, err)
		}

		err = os.Remove(ReadByohControllerManagerLogShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadByohControllerManagerLogShellFile, err)
		}

		err = os.Remove(ReadAllPodsShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadAllPodsShellFile, err)
		}
	})
})
