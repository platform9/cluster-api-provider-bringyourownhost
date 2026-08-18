// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var _ = Describe("Cluster upgrade test [K8s-Upgrade-Cluster]", func() {

	var (
		ctx                          context.Context
		specName                     = "upgrade"
		namespace                    *corev1.Namespace
		cancelWatches                context.CancelFunc
		clusterResources             *clusterctl.ApplyClusterTemplateAndWaitResult
		byoHostCapacityPool          = 4
		dockerClient                 *client.Client
		allbyohostContainerIDs       []string
		allAgentLogFiles             []string
		kubernetesVersionUpgradeFrom = "v1.31.0"
		kubernetesVersionUpgradeTo   = "v1.31.2"
		etcdUpgradeVersion           = "3.5.15-0"
		coreDNSUpgradeVersion        = "v1.11.3"
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should successfully upgrade cluster", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))
		var err error
		dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		By("Creating byohost capacity pool containing 4 docker hosts")
		hosts, err := spinUpByoHosts(ctx, dockerClient, namespace.Name, byoHostCapacityPool)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.Output.Close()
			defer closeLogFile(host.LogFile, host.LogFilePath)
			allbyohostContainerIDs = append(allbyohostContainerIDs, host.ContainerID)
			allAgentLogFiles = append(allAgentLogFiles, host.LogFilePath)
		}

		By("creating a workload cluster with one control plane node and one worker node")

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
				KubernetesVersion:        kubernetesVersionUpgradeFrom,
				ControlPlaneMachineCount: ptr.To(int64(1)),
				WorkerMachineCount:       ptr.To(int64(1)),
			},
			WaitForClusterIntervals:      e2eConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: e2eConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		By("Upgrading the control plane")
		framework.UpgradeControlPlaneAndWaitForUpgrade(ctx, framework.UpgradeControlPlaneAndWaitForUpgradeInput{
			ClusterProxy:                bootstrapClusterProxy,
			Cluster:                     clusterResources.Cluster,
			ControlPlane:                clusterResources.ControlPlane,
			EtcdImageTag:                etcdUpgradeVersion,
			DNSImageTag:                 coreDNSUpgradeVersion,
			KubernetesUpgradeVersion:    kubernetesVersionUpgradeTo,
			WaitForMachinesToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForKubeProxyUpgrade:     e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForDNSUpgrade:           e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForEtcdUpgrade:          e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
		})

		By("Upgrading the machine deployment")
		framework.UpgradeMachineDeploymentsAndWait(ctx, framework.UpgradeMachineDeploymentsAndWaitInput{
			ClusterProxy:                bootstrapClusterProxy,
			Cluster:                     clusterResources.Cluster,
			UpgradeVersion:              kubernetesVersionUpgradeTo,
			MachineDeployments:          clusterResources.MachineDeployments,
			WaitForMachinesToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		})

		By("Waiting until nodes are ready")
		workloadProxy := bootstrapClusterProxy.GetWorkloadCluster(ctx, namespace.Name, clusterResources.Cluster.Name)
		workloadClient := workloadProxy.GetClient()
		framework.WaitForNodesReady(ctx, framework.WaitForNodesReadyInput{
			Lister:            workloadClient,
			KubernetesVersion: kubernetesVersionUpgradeTo,
			Count:             int(clusterResources.ExpectedTotalNodes()),
			WaitForNodesReady: e2eConfig.GetIntervals(specName, "wait-nodes-ready"),
		})
	})
	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			ShowInfo(allAgentLogFiles)
		}
	})

	AfterEach(func() {
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		if dockerClient != nil {
			for _, byohostContainerID := range allbyohostContainerIDs {
				err := dockerClient.ContainerStop(ctx, byohostContainerID, container.StopOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = dockerClient.ContainerRemove(ctx, byohostContainerID, container.RemoveOptions{})
				Expect(err).NotTo(HaveOccurred())
			}

		}

		for _, agentLogFile := range allAgentLogFiles {
			err := os.Remove(agentLogFile)
			if err != nil {
				Showf("error removing file %s: %v", agentLogFile, err)
			}
		}
		err := os.Remove(ReadByohControllerManagerLogShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadByohControllerManagerLogShellFile, err)
		}
		err = os.Remove(ReadAllPodsShellFile)
		if err != nil {
			Showf("error removing file %s: %v", ReadAllPodsShellFile, err)
		}
	})
})
