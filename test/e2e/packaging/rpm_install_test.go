// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package packaging_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const rpmContainerPath = "/root/pf9-byohost.rpm"

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
		containerID, cleanup := startPackagingContainer(ctx, rpmTestImage)
		defer cleanup()

		By("copying the built RPM into the container")
		Expect(copyFileToContainer(ctx, containerID, rpmPath, rpmContainerPath)).To(Succeed())

		By("installing the RPM")
		installOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"rpm", "-i", rpmContainerPath}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "rpm -i failed:\n%s", installOutput)

		By("asserting the package owns exactly the expected files")
		filesOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"rpm", "-ql", "pf9-byohost"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))
		Expect(strings.Fields(filesOutput)).To(ConsistOf(
			"/binary/pf9-byoh-hostagent",
			"/etc/systemd/system/pf9-byohost-agent.service",
			"/usr/bin/byohctl",
		))

		assertByohctlRuns(ctx, containerID)
		assertEnvironmentFile(ctx, containerID, "%post")
		assertServiceEnabled(ctx, containerID)

		By("uninstalling the RPM")
		uninstallOutput, exitCode, err := execInContainer(ctx, containerID, []string{"rpm", "-e", "pf9-byohost"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "rpm -e failed:\n%s", uninstallOutput)

		By("asserting the binary, unit file, and byohctl are gone")
		assertPathsRemoved(ctx, containerID, []string{
			"/binary/pf9-byoh-hostagent",
			"/etc/systemd/system/pf9-byohost-agent.service",
			"/usr/bin/byohctl",
		})
	})
})
