// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

// agentUpgradeRolloutFleetSize is the ADR's own §5.3 example size for this scenario.
const agentUpgradeRolloutFleetSize = 4

// agentUpgradeRolloutTimeout/Poll bound the full rollout: MaxUnavailable=1 mostly serializes the
// 4 hosts, and each one's cycle is imgpkg pull (local registry, fast) + dpkg -i + os.Exit(0) +
// systemd relaunch (RestartSec=5s) + the rollout controller's own 15s reconcile tick -- a few
// minutes of headroom for 4 hosts.
const (
	agentUpgradeRolloutTimeout = 5 * time.Minute
	agentUpgradeRolloutPoll    = 5 * time.Second
)

var _ = Describe("When an agent upgrade rollout stages across a fleet [AgentUpgrade]", func() {

	var (
		ctx              context.Context
		specName         = "agent-upgrade-rollout"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		hosts            []byoHostHandle
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should stage an agent upgrade across the fleet respecting maxUnavailable", func() {
		const (
			oldVersion = "v9.9.8"
			newVersion = "v9.9.9"
		)

		dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())
		setDockerClient(dc)

		By("Building the old and new agent binaries")
		oldBinary, err := buildFixtureAgentBinary(oldVersion)
		Expect(err).NotTo(HaveOccurred())
		newBinary, err := buildFixtureAgentBinary(newVersion)
		Expect(err).NotTo(HaveOccurred())

		By("Packaging the new version as a .deb fixture and pushing it to the local registry")
		newDebDir, err := buildFixtureAgentDeb(ctx, newBinary, newVersion)
		Expect(err).NotTo(HaveOccurred())
		packageURL, err := pushFixtureAgentBundle(ctx, dockerClient, dockerNetworkInterfaceKind, newDebDir,
			fmt.Sprintf("agent-fixture-%s:e2e", util.RandomString(6)))
		Expect(err).NotTo(HaveOccurred())

		By("Spinning up a 4-host fleet running the old version under systemd")
		hosts, err = spinUpByoHostsWithSystemdAgent(ctx, dockerClient, namespace.Name, agentUpgradeRolloutFleetSize, oldBinary)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
		}

		By("Installing imgpkg on each host (stands in for the k8s-installer's self-install fallback)")
		repoRoot, err := resolveRepoRoot(ctx)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			Expect(installImgpkgOnHost(ctx, dockerClient, host.ContainerID, repoRoot)).To(Succeed())
		}

		By("Waiting for all 4 hosts to connect")
		for _, host := range hosts {
			AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, host.Name, specName, infrastructurev1beta1.AgentConnected)
		}

		// Captured before the rollout starts -- proof, once the rollout completes, that each
		// host's agent process actually restarted (a new PID) inside the same container (an
		// unchanged container ID), not that the container itself was recreated.
		originalPIDs := make(map[string]int, len(hosts))
		for _, host := range hosts {
			originalPIDs[host.Name] = mainPID(ctx, dockerClient, host.ContainerID)
			Expect(originalPIDs[host.Name]).To(BeNumerically(">", 0))
		}

		By("Creating a ByoHostAgentUpgrade targeting all 4 hosts with MaxUnavailable=1")
		maxUnavailable := intstr.FromInt(1)
		upgrade := &infrastructurev1beta1.ByoHostAgentUpgrade{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", specName, util.RandomString(6)),
				Namespace: namespace.Name,
			},
			Spec: infrastructurev1beta1.ByoHostAgentUpgradeSpec{
				Selector:       metav1.LabelSelector{},
				TargetVersion:  newVersion,
				PackageURL:     packageURL,
				MaxUnavailable: &maxUnavailable,
			},
		}
		Expect(bootstrapClusterProxy.GetClient().Create(ctx, upgrade)).To(Succeed())
		upgradeKey := k8stypes.NamespacedName{Name: upgrade.Name, Namespace: upgrade.Namespace}

		By("Polling the rollout to completion, asserting it never exceeds MaxUnavailable or fails")
		Eventually(func(g Gomega) infrastructurev1beta1.ByoHostAgentUpgradePhase {
			got := &infrastructurev1beta1.ByoHostAgentUpgrade{}
			g.Expect(bootstrapClusterProxy.GetClient().Get(ctx, upgradeKey, got)).To(Succeed())
			g.Expect(got.Status.UnavailableCount).To(BeNumerically("<=", 1),
				"UnavailableCount exceeded MaxUnavailable mid-rollout")
			g.Expect(got.Status.Phase).NotTo(Equal(infrastructurev1beta1.ByoHostAgentUpgradePhaseFailed),
				"rollout failed: FailedHosts=%v", got.Status.FailedHosts)
			return got.Status.Phase
		}, agentUpgradeRolloutTimeout, agentUpgradeRolloutPoll).Should(Equal(infrastructurev1beta1.ByoHostAgentUpgradePhaseCompleted))

		By("Asserting full convergence")
		converged := &infrastructurev1beta1.ByoHostAgentUpgrade{}
		Expect(bootstrapClusterProxy.GetClient().Get(ctx, upgradeKey, converged)).To(Succeed())
		Expect(converged.Status.Upgraded).To(Equal(int32(agentUpgradeRolloutFleetSize)))
		Expect(converged.Status.FailedHosts).To(BeEmpty())

		By("Asserting each host's agent process restarted inside its unchanged container")
		for _, host := range hosts {
			h := &infrastructurev1beta1.ByoHost{}
			Expect(bootstrapClusterProxy.GetClient().Get(ctx, k8stypes.NamespacedName{Name: host.Name, Namespace: namespace.Name}, h)).To(Succeed())
			Expect(h.Status.AgentVersion).To(Equal(newVersion), "host %s never reported the new version", host.Name)

			newPID := mainPID(ctx, dockerClient, host.ContainerID)
			Expect(newPID).To(SatisfyAll(BeNumerically(">", 0), Not(BeNumerically("==", originalPIDs[host.Name]))),
				"expected host %s's agent process to have restarted with a new PID", host.Name)

			inspect, err := dockerClient.ContainerInspect(ctx, host.ContainerID)
			Expect(err).NotTo(HaveOccurred(), "expected host %s's original container to still exist", host.Name)
			Expect(inspect.State.Running).To(BeTrue(), "expected host %s's container to still be running", host.Name)
		}
	})

	JustAfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			logFiles := make([]string, 0, len(hosts))
			for _, host := range hosts {
				logFiles = append(logFiles, host.LogFilePath)
			}
			ShowInfo(logFiles)
		}
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		teardownByoHosts(ctx, getDockerClient(), hosts)
	})
})
