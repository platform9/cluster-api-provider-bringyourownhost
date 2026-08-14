// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package packaging_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Same reasoning as rpmContainerPath (rpm_install_test.go): not under /tmp,
// since it's tmpfs-mounted and systemd's own tmp.mount unit can shadow
// anything copied there before it runs.
const debContainerPath = "/root/pf9-byohost-agent.deb"

var _ = Describe("pf9-byohost deb", func() {
	It("installs cleanly and uninstalls cleanly", func() {
		repoRootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		Expect(err).NotTo(HaveOccurred())
		repoRoot := strings.TrimSpace(string(repoRootBytes))

		By("building the deb")
		buildCmd := exec.Command("make", "build-host-agent-deb")
		buildCmd.Dir = repoRoot
		buildOutput, err := buildCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "make build-host-agent-deb failed:\n%s", buildOutput)

		matches, err := filepath.Glob(filepath.Join(repoRoot, "build/pf9-byohost/debsrc/*.deb"))
		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(HaveLen(1), "expected exactly one built deb, found: %v", matches)
		debPath := matches[0]

		By("asserting the deb is tagged for the architecture of the binary it actually contains")
		// The build (make build-host-agent-deb, run via exec.Command above) and this
		// test process both run natively on whatever host/container invoked `go
		// test` - no cross-compilation involved - so runtime.GOARCH here is the same
		// architecture PACKAGE_GOARCH resolved to in the Makefile. Debian's arch
		// names happen to equal Go's GOARCH values for amd64/arm64.
		archOutput, err := exec.Command("dpkg-deb", "-f", debPath, "Architecture").Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(archOutput))).To(Equal(runtime.GOARCH))

		By("starting a byoh/node:e2e container")
		created, err := dockerClient.ContainerCreate(ctx,
			&container.Config{Image: debTestImage},
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

		By("copying the built deb into the container")
		Expect(copyFileToContainer(ctx, containerID, debPath, debContainerPath)).To(Succeed())

		By("installing the deb")
		installOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"dpkg", "-i", debContainerPath}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "dpkg -i failed:\n%s", installOutput)

		By("asserting the package owns the binary and unit file")
		filesOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"dpkg", "-L", "pf9-byohost-agent"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))
		// Unlike rpm -ql (rpm_install_test.go), dpkg -L also lists the
		// directory entries fpm preserved from the staged source tree
		// (/binary, /etc/systemd/system, /usr/share/doc/..., etc.), so this
		// asserts the two files are present rather than an exact list.
		Expect(strings.Fields(filesOutput)).To(ContainElements(
			"/binary/pf9-byoh-hostagent",
			"/etc/systemd/system/pf9-byohost-agent.service",
		))

		By("asserting after-install.sh generated the EnvironmentFile the systemd unit reads BOOTSTRAP_KUBECONFIG from")
		confOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"cat", "/etc/pf9-byohost-agent.service.d/pf9-byohost-agent.conf"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))
		Expect(confOutput).To(ContainSubstring("BOOTSTRAP_KUBECONFIG="))
		Expect(confOutput).To(ContainSubstring("NAMESPACE="))
		Expect(confOutput).To(ContainSubstring("REGION="))

		// Not asserting is-active: same reasoning as the RPM test - no real
		// cluster for the agent to reach in this environment.
		By("asserting the service is enabled")
		enabledOutput, _, err := execInContainer(ctx, containerID, []string{"systemctl", "is-enabled", "pf9-byohost-agent"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(enabledOutput)).To(Equal("enabled"))

		By("uninstalling the deb")
		uninstallOutput, exitCode, err := execInContainer(ctx, containerID, []string{"dpkg", "-r", "pf9-byohost-agent"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0), "dpkg -r failed:\n%s", uninstallOutput)

		By("asserting the binary, unit file, and generated conf directory are gone")
		for _, path := range []string{
			"/binary/pf9-byoh-hostagent",
			"/etc/systemd/system/pf9-byohost-agent.service",
			"/etc/pf9-byohost-agent.service.d",
		} {
			_, pathExitCode, pathErr := execInContainer(ctx, containerID, []string{"test", "-e", path}, nil)
			Expect(pathErr).NotTo(HaveOccurred())
			Expect(pathExitCode).NotTo(Equal(0), "%s should have been removed by dpkg -r", path)
		}

		By("asserting before-remove.sh logged to the same directory every other pf9 log lives in")
		uninstallLogOutput, exitCode, err := execInContainer(ctx, containerID,
			[]string{"cat", "/var/log/pf9/byoh/byoh-agent-uninstall.log"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(Equal(0))
		Expect(uninstallLogOutput).To(ContainSubstring("Uninstallation of pf9-byoh-hostagent completed successfully"))
	})
})
