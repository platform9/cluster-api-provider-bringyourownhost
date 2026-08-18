// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

var (
	dockerClient *client.Client
)

var _ = Describe("When BYO Host rejoins the capacity pool [Reuse]", func() {

	var (
		ctx                 context.Context
		specName            = "byohost-reuse"
		namespace           *corev1.Namespace
		cancelWatches       context.CancelFunc
		clusterResources    *clusterctl.ApplyClusterTemplateAndWaitResult
		byohostContainerIDs []string
		agentLogFile1       string
		agentLogFile2       string
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should reuse the same BYO Host after it is reset", func() {
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		client, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())
		setDockerClient(client)

		hosts, err := spinUpByoHosts(ctx, dockerClient, namespace.Name, 2)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
			byohostContainerIDs = append(byohostContainerIDs, host.ContainerID)
		}
		byoHostName2 := hosts[1].Name
		agentLogFile1, agentLogFile2 = hosts[0].LogFilePath, hosts[1].LogFilePath

		By("Creating a cluster")

		setControlPlaneIP(context.Background(), dockerClient)
		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, e2eConfig.GetVariableOrEmpty(KubernetesVersion), clusterctl.DefaultFlavor, 1, 1, clusterResources)

		// Assert on byohost cluster label to match clusterName
		byoHostLookupKey := k8stypes.NamespacedName{Name: byoHostName2, Namespace: namespace.Name}
		byoHostToBeReused := &infrastructurev1beta1.ByoHost{}
		Expect(bootstrapClusterProxy.GetClient().Get(ctx, byoHostLookupKey, byoHostToBeReused)).Should(Succeed())
		cluster, ok := byoHostToBeReused.Labels[clusterv1.ClusterNameLabel]
		Expect(ok).To(BeTrue())
		Expect(cluster).To(Equal(clusterName))

		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, byoHostName2, specName,
			infrastructurev1beta1.AgentConnected,
			infrastructurev1beta1.K8sComponentsInstallationSucceeded,
			infrastructurev1beta1.K8sNodeBootstrapSucceeded,
		)

		By("Delete the cluster and freeing the ByoHosts")
		framework.DeleteAllClustersAndWait(ctx, framework.DeleteAllClustersAndWaitInput{
			ClusterProxy:         bootstrapClusterProxy,
			ClusterctlConfigPath: clusterctlConfigPath,
			Namespace:            namespace.Name,
		}, e2eConfig.GetIntervals(specName, "wait-delete-cluster")...)

		// Assert if cluster label is removed
		// This verifies that the byohost has rejoined the capacity pool. The label is cleared
		// asynchronously by the host agent's own reconcile loop (kubeadm reset plus an optional
		// uninstall script) after the management-cluster side has already let the Cluster finish
		// deleting, so this must poll rather than check once.
		Eventually(func() bool {
			polledByoHost := &infrastructurev1beta1.ByoHost{}
			if err := bootstrapClusterProxy.GetClient().Get(ctx, byoHostLookupKey, polledByoHost); err != nil {
				return true
			}
			_, labelPresent := polledByoHost.Labels[clusterv1.ClusterNameLabel]
			return labelPresent
		}, e2eConfig.GetIntervals("", "wait-controllers")...).Should(BeFalse())

		By("Creating a new cluster")
		clusterName = fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		applyClusterAndWait(ctx, namespace.Name, clusterName, specName, e2eConfig.GetVariableOrEmpty(KubernetesVersion), clusterctl.DefaultFlavor, 1, 1, clusterResources)

		// Assert on byohost cluster label to match clusterName
		byoHostToBeReused = &infrastructurev1beta1.ByoHost{}
		Expect(bootstrapClusterProxy.GetClient().Get(ctx, byoHostLookupKey, byoHostToBeReused)).Should(Succeed())
		cluster, ok = byoHostToBeReused.Labels[clusterv1.ClusterNameLabel]
		Expect(ok).To(BeTrue())
		Expect(cluster).To(Equal(clusterName))

		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, byoHostName2, specName,
			infrastructurev1beta1.AgentConnected,
			infrastructurev1beta1.K8sComponentsInstallationSucceeded,
			infrastructurev1beta1.K8sNodeBootstrapSucceeded,
		)

	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			ShowInfo([]string{agentLogFile1, agentLogFile2})
		}
	})

	AfterEach(func() {
		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		if getDockerClient() != nil && len(byohostContainerIDs) != 0 {
			for _, byohostContainerID := range byohostContainerIDs {
				err := getDockerClient().ContainerStop(ctx, byohostContainerID, container.StopOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = getDockerClient().ContainerRemove(ctx, byohostContainerID, container.RemoveOptions{})
				Expect(err).NotTo(HaveOccurred())
			}
		}

		err := os.Remove(agentLogFile1)
		if err != nil {
			Showf("Failed to remove file %s: %v", agentLogFile1, err)
		}
		err = os.Remove(agentLogFile2)
		if err != nil {
			Showf("Failed to remove file %s: %v", agentLogFile2, err)
		}
		err = os.Remove(ReadByohControllerManagerLogShellFile)
		if err != nil {
			Showf("Failed to remove file %s: %v", ReadByohControllerManagerLogShellFile, err)
		}
		err = os.Remove(ReadAllPodsShellFile)
		if err != nil {
			Showf("Failed to remove file %s: %v", ReadAllPodsShellFile, err)
		}
	})
})

func setDockerClient(dc *client.Client) {
	dockerClient = dc
}

func getDockerClient() *client.Client {
	return dockerClient
}
