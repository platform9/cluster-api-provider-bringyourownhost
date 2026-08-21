// Copyright 2022 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var _ = Describe("Clusterclass upgrade test [K8s-Upgrade-ClusterClass]", func() {

	var (
		ctx                          context.Context
		specName                     = "upgrade"
		namespace                    *corev1.Namespace
		cancelWatches                context.CancelFunc
		clusterResources             *clusterctl.ApplyClusterTemplateAndWaitResult
		byoHostCapacityPool          = 4
		dockerClient                 *client.Client
		hosts                        []byoHostHandle
		allAgentLogFiles             []string
		kubernetesVersionUpgradeFrom = getEnvOrDefault("E2E_K8S_VERSION_FROM", "v1.31.0")
		kubernetesVersionUpgradeTo   = getEnvOrDefault("E2E_K8S_VERSION_TO", "v1.31.2")
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
		hosts, err = spinUpByoHosts(ctx, dockerClient, namespace.Name, byoHostCapacityPool)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
			allAgentLogFiles = append(allAgentLogFiles, host.LogFilePath)
		}

		By("creating a workload cluster with one control plane node and one worker node")

		setControlPlaneIP(ctx, dockerClient)
		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, kubernetesVersionUpgradeFrom, "topology", 1, 1, clusterResources)

		By("Upgrading the control plane")
		framework.UpgradeClusterTopologyAndWaitForUpgrade(ctx, framework.UpgradeClusterTopologyAndWaitForUpgradeInput{
			ClusterProxy:                bootstrapClusterProxy,
			Cluster:                     clusterResources.Cluster,
			ControlPlane:                clusterResources.ControlPlane,
			EtcdImageTag:                etcdUpgradeVersion,
			DNSImageTag:                 coreDNSUpgradeVersion,
			MachineDeployments:          clusterResources.MachineDeployments,
			KubernetesUpgradeVersion:    kubernetesVersionUpgradeTo,
			WaitForMachinesToBeUpgraded: e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForKubeProxyUpgrade:     e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForDNSUpgrade:           e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
			WaitForEtcdUpgrade:          e2eConfig.GetIntervals(specName, "wait-machine-upgrade"),
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

		teardownByoHosts(ctx, dockerClient, hosts)
	})
})
