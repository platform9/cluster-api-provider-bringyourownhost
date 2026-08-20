// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
)

// agentUpgradeUnavailabilityPollInterval/Duration bound how long the "budget stays exhausted"
// assertion polls -- long enough to span several of the rollout controller's own 15s reconcile
// ticks (byohostagentupgrade_controller.go's requeueInterval), so a single lucky tick can't pass
// the assertion by accident.
const (
	agentUpgradeUnavailabilityDuration = 45 * time.Second
	agentUpgradeUnavailabilityPoll     = 5 * time.Second
)

var _ = Describe("When an agent upgrade rollout hits pre-existing unavailability [AgentUpgrade]", func() {

	var (
		ctx              context.Context
		specName         = "agent-upgrade-pause"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		hosts            []byoHostHandle
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should pause, not fail, on pre-existing unrelated unavailability, and resume once it clears", func() {
		dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())
		setDockerClient(dc)

		hosts, err = spinUpByoHosts(ctx, dockerClient, namespace.Name, 4)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
		}

		By("Waiting for all 4 hosts to report AgentConnected=True")
		for _, host := range hosts {
			AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, host.Name, specName, infrastructurev1beta1.AgentConnected)
		}

		By("Killing one host's agent process, unrelated to any upgrade")
		disconnected := hosts[0]
		Expect(killAgentProcess(ctx, dockerClient, disconnected.ContainerID)).To(Succeed())

		disconnectedKey := k8stypes.NamespacedName{Name: disconnected.Name, Namespace: namespace.Name}
		Eventually(func() bool {
			host := &infrastructurev1beta1.ByoHost{}
			if getErr := bootstrapClusterProxy.GetClient().Get(ctx, disconnectedKey, host); getErr != nil {
				return false
			}
			return conditions.IsFalse(host, infrastructurev1beta1.AgentConnected)
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected ByoHost %s to report AgentConnected=False after its agent process was killed", disconnected.Name)

		By("Creating a ByoHostAgentUpgrade targeting all 4 hosts with MaxUnavailable=1")
		upgrade := &infrastructurev1beta1.ByoHostAgentUpgrade{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", specName, util.RandomString(6)),
				Namespace: namespace.Name,
			},
			Spec: infrastructurev1beta1.ByoHostAgentUpgradeSpec{
				Selector:      metav1.LabelSelector{},
				TargetVersion: "v9.9.9",
				PackageURL:    "registry.example.com/agent-bundle:v9.9.9",
			},
		}
		Expect(bootstrapClusterProxy.GetClient().Create(ctx, upgrade)).To(Succeed())
		upgradeKey := k8stypes.NamespacedName{Name: upgrade.Name, Namespace: upgrade.Namespace}

		By("Asserting the rollout stays Pending and picks no host while the unrelated host is disconnected")
		Consistently(func() error {
			for _, host := range hosts {
				h := &infrastructurev1beta1.ByoHost{}
				if getErr := bootstrapClusterProxy.GetClient().Get(ctx, k8stypes.NamespacedName{Name: host.Name, Namespace: namespace.Name}, h); getErr != nil {
					return getErr
				}
				if h.Spec.DesiredAgent != nil {
					return fmt.Errorf("host %s already has DesiredAgent set, expected the rollout to stay blocked", host.Name)
				}
			}

			got := &infrastructurev1beta1.ByoHostAgentUpgrade{}
			if getErr := bootstrapClusterProxy.GetClient().Get(ctx, upgradeKey, got); getErr != nil {
				return getErr
			}
			if got.Status.Phase != "" && got.Status.Phase != infrastructurev1beta1.ByoHostAgentUpgradePhasePending {
				return fmt.Errorf("expected Phase to stay Pending, got %q", got.Status.Phase)
			}
			if conditions.IsFalse(got, infrastructurev1beta1.RolloutAvailable) &&
				conditions.GetReason(got, infrastructurev1beta1.RolloutAvailable) != infrastructurev1beta1.InsufficientAvailabilityBudgetReason {
				return fmt.Errorf("expected RolloutAvailable=False reason %q, got %q",
					infrastructurev1beta1.InsufficientAvailabilityBudgetReason, conditions.GetReason(got, infrastructurev1beta1.RolloutAvailable))
			}
			return nil
		}, agentUpgradeUnavailabilityDuration, agentUpgradeUnavailabilityPoll).Should(Succeed())

		By("Restarting the killed host's agent")
		stopNewLog, err := restartAgentProcess(ctx, dockerClient, disconnected.ContainerID, namespace.Name, disconnected.LogFilePath)
		Expect(err).NotTo(HaveOccurred())
		defer stopNewLog()

		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, disconnected.Name, specName, infrastructurev1beta1.AgentConnected)

		By("Asserting the rollout resumes picking hosts on its own, no operator action taken on the upgrade itself")
		Eventually(func() bool {
			for _, host := range hosts {
				h := &infrastructurev1beta1.ByoHost{}
				if err := bootstrapClusterProxy.GetClient().Get(ctx, k8stypes.NamespacedName{Name: host.Name, Namespace: namespace.Name}, h); err != nil {
					continue
				}
				if h.Spec.DesiredAgent != nil && h.Spec.DesiredAgent.Version == "v9.9.9" {
					return true
				}
			}
			return false
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected at least one host to be picked for upgrade once the unrelated host recovered")
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		teardownByoHosts(ctx, getDockerClient(), hosts)
	})
})

// killAgentProcess simulates an unrelated agent crash/outage: it kills the host agent process by
// name inside containerID, without touching the container itself. The e2e harness doesn't yet
// run the agent under a process supervisor (see the agent-upgrade e2e plan's PR2), so unlike a
// real self-upgrade exit this kill is never automatically recovered -- restartAgentProcess below
// is what stands in for that recovery in this test.
func killAgentProcess(ctx context.Context, dockerClient *client.Client, containerID string) error {
	execCommand, err := dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"pkill", "-f", "./agent"},
	})
	if err != nil {
		return err
	}
	return dockerClient.ContainerExecStart(ctx, execCommand.ID, container.ExecStartOptions{})
}

// restartAgentProcess re-execs the agent binary inside containerID with the same flags
// spinUpByoHosts originally started it with, and starts a fresh log stream at logFilePath.
// Returns a StopLog closer for that new stream, matching byoHostHandle.StopLog's contract.
func restartAgentProcess(ctx context.Context, dockerClient *client.Client, containerID, namespace, logFilePath string) (func(), error) {
	runner := ByoHostRunner{
		Context:      ctx,
		DockerClient: dockerClient,
		CommandArgs: map[string]string{
			agentFlagBootstrapKubeconfig: bootstrapConfPath,
			agentFlagNamespace:           namespace,
			agentFlagVerbosity:           "1",
		},
	}
	output, _, err := runner.ExecByoDockerHost(&container.CreateResponse{ID: containerID})
	if err != nil {
		return nil, err
	}
	return StreamDockerLog(output, logFilePath), nil
}
