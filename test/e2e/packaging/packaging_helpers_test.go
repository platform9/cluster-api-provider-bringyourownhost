// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package packaging_test

import (
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func startPackagingContainer(ctx context.Context, image string) (containerID string, cleanup func()) {
	created, err := dockerClient.ContainerCreate(ctx,
		&container.Config{Image: image},
		&container.HostConfig{
			Privileged: true,
			Tmpfs:      map[string]string{"/run": "", "/run/lock": "", "/tmp": ""},
		},
		nil, nil, "")
	Expect(err).NotTo(HaveOccurred())
	containerID = created.ID
	cleanup = func() {
		_ = dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	}

	Expect(dockerClient.ContainerStart(ctx, containerID, container.StartOptions{})).To(Succeed())

	By("waiting for systemd to come up")
	Eventually(func() (string, error) {
		output, _, execErr := execInContainer(ctx, containerID, []string{"systemctl", "is-system-running"}, nil)
		return output, execErr
	}, 30*time.Second, time.Second).Should(SatisfyAny(
		ContainSubstring("running"), ContainSubstring("degraded"),
	))

	return containerID, cleanup
}

func assertByohctlRuns(ctx context.Context, containerID string) {
	By("asserting byohctl was installed executable and runs")
	byohctlOutput, exitCode, err := execInContainer(ctx, containerID, []string{"byohctl", "version"}, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(Equal(0), "byohctl version failed:\n%s", byohctlOutput)
}

func assertEnvironmentFile(ctx context.Context, containerID, generatedBy string) {
	By("asserting " + generatedBy + " generated the EnvironmentFile the systemd unit reads BOOTSTRAP_KUBECONFIG from")
	confOutput, exitCode, err := execInContainer(ctx, containerID,
		[]string{"cat", "/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf"}, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(Equal(0))
	Expect(confOutput).To(ContainSubstring("BOOTSTRAP_KUBECONFIG="))
	Expect(confOutput).To(ContainSubstring("NAMESPACE="))
	Expect(confOutput).To(ContainSubstring("REGION="))
}

// Not asserting is-active: no real cluster for the agent to reach in this environment.
func assertServiceEnabled(ctx context.Context, containerID string) {
	By("asserting the service is enabled")
	enabledOutput, _, err := execInContainer(ctx, containerID, []string{"systemctl", "is-enabled", "pf9-byohost-agent"}, nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(enabledOutput)).To(Equal("enabled"))
}

func assertPathsRemoved(ctx context.Context, containerID string, paths []string) {
	for _, path := range paths {
		_, exitCode, err := execInContainer(ctx, containerID, []string{"test", "-e", path}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).NotTo(Equal(0), "%s should have been removed", path)
	}
}
