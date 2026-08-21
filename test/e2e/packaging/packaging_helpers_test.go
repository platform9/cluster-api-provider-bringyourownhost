// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package packaging_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func copyFileToContainer(ctx context.Context, containerID, localPath, containerPath string) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: strings.TrimPrefix(containerPath, "/"),
		Mode: 0644,
		Size: int64(len(content)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return dockerClient.CopyToContainer(ctx, containerID, "/", &buf, container.CopyToContainerOptions{})
}

func execInContainer(ctx context.Context, containerID string, cmd, env []string) (output string, exitCode int, err error) {
	created, err := dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	})
	if err != nil {
		return "", 0, err
	}

	// Tty must match the exec's own creation config: mismatched Tty here can
	// leave the daemon multiplexing stdout/stderr with an 8-byte frame header
	// per chunk instead of returning the raw stream, corrupting the output.
	attached, err := dockerClient.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return "", 0, err
	}
	defer attached.Close()

	outputBytes, err := io.ReadAll(attached.Reader)
	if err != nil {
		return "", 0, err
	}

	inspected, err := dockerClient.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return string(outputBytes), 0, err
	}
	return string(outputBytes), inspected.ExitCode, nil
}

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

func assertPathsRemoved(ctx context.Context, containerID, removedBy string, paths []string) {
	for _, path := range paths {
		_, exitCode, err := execInContainer(ctx, containerID, []string{"test", "-e", path}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).NotTo(Equal(0), "%s should have been removed by %s", path, removedBy)
	}
}
