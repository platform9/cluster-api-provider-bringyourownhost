// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
)

// systemdRestartPollTimeout/Interval bound how long to wait for systemd's Restart=always to
// relaunch a killed agent process -- service/pf9-byohostagent.service's RestartSec=5s, so a
// handful of seconds of headroom is enough; this has nothing to do with the agent's own
// heartbeat/AgentConnected timing, which is much slower.
const (
	systemdRestartPollTimeout  = 30 * time.Second
	systemdRestartPollInterval = 2 * time.Second
)

var _ = Describe("When the e2e harness runs the host agent under systemd [AgentUpgradeHarness]", func() {

	var (
		ctx              context.Context
		specName         = "agent-upgrade-harness"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		clusterResources *clusterctl.ApplyClusterTemplateAndWaitResult
		hosts            []byoHostHandle
	)

	BeforeEach(func() {
		ctx, namespace, cancelWatches, clusterResources = commonSpecSetup(specName)
	})

	It("Should restart the agent process, not the container, after the process is killed", func() {
		dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())
		setDockerClient(dc)

		hosts, err = spinUpByoHostsWithSystemdAgent(ctx, dockerClient, namespace.Name, 1, pathToHostAgentBinary)
		Expect(err).NotTo(HaveOccurred())
		for _, host := range hosts {
			defer host.StopLog()
		}
		host := hosts[0]

		By("Confirming the systemd-supervised agent registers and connects normally")
		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, host.Name, specName, infrastructurev1beta1.AgentConnected)

		By("Reading the agent's MainPID from systemd")
		originalPID := mainPID(ctx, dockerClient, host.ContainerID)
		Expect(originalPID).To(BeNumerically(">", 0))

		By("Killing the agent process directly (not via docker exec, not via the container)")
		Expect(runContainerCommand(ctx, dockerClient, host.ContainerID, "kill", "-9", strconv.Itoa(originalPID))).To(Succeed())

		By("Asserting systemd relaunches it with a new PID, same container")
		Eventually(func() int {
			return mainPID(ctx, dockerClient, host.ContainerID)
		}, systemdRestartPollTimeout, systemdRestartPollInterval).Should(
			SatisfyAll(BeNumerically(">", 0), Not(BeNumerically("==", originalPID))),
			"expected systemd to relaunch the agent with a new MainPID after it was killed")

		activeState, err := containerCommandOutput(ctx, dockerClient, host.ContainerID,
			"systemctl", "show", "-p", "ActiveState", "--value", systemdAgentServiceUnitName)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(activeState)).To(Equal("active"))
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, clusterResources.Cluster, e2eConfig.GetIntervals, skipCleanup)

		teardownByoHosts(ctx, getDockerClient(), hosts)
	})
})

// mainPID reads the current MainPID systemd reports for the agent unit inside containerID,
// returning 0 if the unit isn't running or the read fails (e.g. mid-restart) rather than erroring
// -- callers poll this via Eventually, where a transient 0 is expected, not a failure.
func mainPID(ctx context.Context, dockerClient *client.Client, containerID string) int {
	out, err := containerCommandOutput(ctx, dockerClient, containerID,
		"systemctl", "show", "-p", "MainPID", "--value", systemdAgentServiceUnitName)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return pid
}
