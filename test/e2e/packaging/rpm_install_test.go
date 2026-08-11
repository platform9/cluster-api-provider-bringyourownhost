// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package packaging_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	rpmContainerPath   = "/root/pf9-byohost.rpm"
	stubKubeconfigPath = "/root/bootstrap-kubeconfig.yaml"
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

	return dockerClient.CopyToContainer(ctx, containerID, "/", &buf, types.CopyToContainerOptions{})
}

func execInContainer(ctx context.Context, containerID string, cmd, env []string) (output string, exitCode int, err error) {
	created, err := dockerClient.ContainerExecCreate(ctx, containerID, types.ExecConfig{
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
	attached, err := dockerClient.ContainerExecAttach(ctx, created.ID, types.ExecStartCheck{Tty: true})
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

var _ = Describe("pf9-byohost RPM", func() {
	It("installs cleanly and uninstalls cleanly", func() {
		repoRootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		Expect(err).NotTo(HaveOccurred())
		repoRoot := strings.TrimSpace(string(repoRootBytes))

		By("clearing any stale build/ output from a previous run")
		Expect(os.RemoveAll(filepath.Join(repoRoot, "build"))).To(Succeed())

		By("building the RPM")
		buildCmd := exec.Command("make", "build-host-agent-rpm")
		buildCmd.Dir = repoRoot
		buildOutput, err := buildCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "make build-host-agent-rpm failed:\n%s", buildOutput)

		matches, err := filepath.Glob(filepath.Join(repoRoot, "build/pf9-byohost/rpmbuild/RPMS/*/*.rpm"))
		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(HaveLen(1), "expected exactly one built RPM, found: %v", matches)
		rpmPath := matches[0]

		By("starting a Rocky Linux container")
		created, err := dockerClient.ContainerCreate(ctx,
			&container.Config{Image: packagingTestImage},
			&container.HostConfig{
				Privileged: true,
				Tmpfs:      map[string]string{"/run": "", "/run/lock": "", "/tmp": ""},
			},
			nil, nil, "")
		Expect(err).NotTo(HaveOccurred())
		containerID := created.ID
		defer func() {
			_ = dockerClient.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true})
		}()

		Expect(dockerClient.ContainerStart(ctx, containerID, types.ContainerStartOptions{})).To(Succeed())

		By("waiting for systemd to come up")
		Eventually(func() (string, error) {
			output, _, execErr := execInContainer(ctx, containerID, []string{"systemctl", "is-system-running"}, nil)
			return output, execErr
		}, 30*time.Second, time.Second).Should(SatisfyAny(
			ContainSubstring("running"), ContainSubstring("degraded"),
		))

		By("copying the built RPM and a stub bootstrap kubeconfig into the container")
		Expect(copyFileToContainer(ctx, containerID, rpmPath, rpmContainerPath)).To(Succeed())
		_, exitCode, err := execInContainer(ctx, containerID, []string{"touch", stubKubeconfigPath}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))

		By("installing the RPM")
		installOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"rpm", "-i", rpmContainerPath},
			[]string{"BOOTSTRAP_KUBECONFIG=" + stubKubeconfigPath})
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "rpm -i failed:\n%s", installOutput)

		By("asserting the package owns exactly the expected files")
		filesOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"rpm", "-ql", "pf9-byohost"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))
		Expect(strings.Fields(filesOutput)).To(ConsistOf(
			"/binary/pf9-byoh-hostagent-linux-amd64",
			"/etc/systemd/system/pf9-byohost-agent.service",
		))

		// Not asserting is-active: the agent gets a stub, empty kubeconfig
		// with no real cluster to register with
		By("asserting the service is enabled")
		enabledOutput, _, err := execInContainer(ctx, containerID, []string{"systemctl", "is-enabled", "pf9-byohost-agent"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(enabledOutput)).To(Equal("enabled"))

		By("uninstalling the RPM")
		uninstallOutput, exitCode, err := execInContainer(ctx, containerID, []string{"rpm", "-e", "pf9-byohost"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "rpm -e failed:\n%s", uninstallOutput)

		By("asserting the binary and unit file are gone")
		for _, path := range []string{
			"/binary/pf9-byoh-hostagent-linux-amd64",
			"/etc/systemd/system/pf9-byohost-agent.service",
		} {
			_, exitCode, err := execInContainer(ctx, containerID, []string{"test", "-e", path}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(exitCode).NotTo(Equal(0), fmt.Sprintf("%s should have been removed by rpm -e", path))
		}
	})
})
