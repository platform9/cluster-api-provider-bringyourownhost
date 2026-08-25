// Copyright 2021 VMware, Inc. All Rights Reserved.
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var _ = Describe("When testing MachineDeployment scale out/in [MD-Scale]", func() {

	var (
		ctx                 context.Context
		specName            = "md-scale"
		namespace           *corev1.Namespace
		cancelWatches       context.CancelFunc
		clusterResources    *clusterctl.ApplyClusterTemplateAndWaitResult
		dockerClient        *client.Client
		byoHostCapacityPool = 6
		hosts               []byoHostHandle
		allAgentLogFiles    []string
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should successfully scale a MachineDeployment up and down upon changes to the MachineDeployment replica count", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		dClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		dockerClient = dClient
		Expect(err).NotTo(HaveOccurred())

		By("Creating byohost capacity pool containing 5 hosts")
		hosts, err = spinUpByoHosts(ctx, dockerClient, namespace.Name, byoHostCapacityPool)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
			allAgentLogFiles = append(allAgentLogFiles, host.LogFilePath)
		}
		// TODO: Write agent logs to files for better debugging

		By("creating a workload cluster with one control plane node and one worker node")

		setControlPlaneIP(ctx, dockerClient)
		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, e2eConfig.GetVariableOrEmpty(KubernetesVersion), clusterctl.DefaultFlavor, 3, 1, clusterResources)

		Expect(clusterResources.MachineDeployments[0].Spec.Replicas).To(Equal(ptr.To(int32(1))))

		By("Scaling the MachineDeployment out to 3")
		framework.ScaleAndWaitMachineDeployment(ctx, framework.ScaleAndWaitMachineDeploymentInput{
			ClusterProxy:              bootstrapClusterProxy,
			Cluster:                   clusterResources.Cluster,
			MachineDeployment:         clusterResources.MachineDeployments[0],
			Replicas:                  3,
			WaitForMachineDeployments: e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		})

		Expect(clusterResources.MachineDeployments[0].Spec.Replicas).To(Equal(ptr.To(int32(3))))

		By("Scaling the MachineDeployment down to 2")
		framework.ScaleAndWaitMachineDeployment(ctx, framework.ScaleAndWaitMachineDeploymentInput{
			ClusterProxy:              bootstrapClusterProxy,
			Cluster:                   clusterResources.Cluster,
			MachineDeployment:         clusterResources.MachineDeployments[0],
			Replicas:                  2,
			WaitForMachineDeployments: e2eConfig.GetIntervals(specName, "wait-worker-nodes"),
		})

		Expect(clusterResources.MachineDeployments[0].Spec.Replicas).To(Equal(ptr.To(int32(2))))

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
