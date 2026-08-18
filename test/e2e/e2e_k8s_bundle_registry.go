// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/docker/docker/client"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/installer"
)

const (
	// bundleRepoOverrideEnvVar lets a caller with real quay.io access for the k8s bundle (e.g. an
	// amd64 CI runner, where quay.io does have a published tag) skip the local build entirely and
	// point straight at a real registry, mirroring byohAgentBundleURLEnvVar's escape hatch.
	bundleRepoOverrideEnvVar = "BUNDLE_REPO_OVERRIDE"

	// k8sBundleUbuntuVersion matches test/e2e/BYOHDockerFile's `FROM ubuntu:22.04` -- the actual OS
	// of the e2e BYO host image, not a general-purpose parameter.
	k8sBundleUbuntuVersion = "22.04"
	// k8sBundleContainerdVersion is independent of the requested Kubernetes version -- see
	// installer/bundle_builder/ingredients/deb/download.sh.
	k8sBundleContainerdVersion = "1.7.26"

	k8sBundleBuilderImageTag  = "byoh-bundle-e2e:local"
	k8sBundleBuilderContainer = "byoh-bundle-e2e-builder"
)

// ensureLocalK8sBundleRegistry builds the k8s installer bundle (containerd + kubeadm/kubelet/
// kubectl/cri-tools -- the same content installer/bundle_builder normally pushes to quay.io)
// locally and serves it from the shared local registry (see startLocalBundleRegistry), returning
// the registry address to use as K8sInstallerConfig's bundleRepo.
//
// quay.io never gets an arm64-tagged k8s bundle at all: .ci/build-push-bundle.sh's bundle-name
// case statement only ever produces x86-64 tags regardless of $ARCH. So on an arm64 host (e.g.
// this suite running under test-e2e-linux-vm on Apple Silicon) the real quay.io pull 401s
// unconditionally -- this isn't a sandboxed-network workaround, building locally is the only way
// an arm64 e2e run gets a bundle at all today.
//
// The second return value reports whether the returned registry is a plaintext local one that
// needs BYOH_BUNDLE_REGISTRY_INSECURE set on the ByoHost container (see install.sh.tmpl) --
// false when BUNDLE_REPO_OVERRIDE points at a real, presumably-TLS registry instead.
func ensureLocalK8sBundleRegistry(ctx context.Context, dockerClient *client.Client, dockerNetwork, k8sVersion string) (bundleRepoAddr string, insecure bool, err error) {
	if override := os.Getenv(bundleRepoOverrideEnvVar); override != "" {
		return override, false, nil
	}

	repoRoot, err := resolveRepoRoot(ctx)
	if err != nil {
		return "", false, err
	}

	imgpkgPath, err := downloadImgpkg(ctx, repoRoot)
	if err != nil {
		return "", false, err
	}

	debianArch, osBundle, err := k8sBundleArchNames(runtime.GOARCH)
	if err != nil {
		return "", false, err
	}

	bundleDir, err := buildK8sBundle(ctx, repoRoot, debianArch, k8sVersion)
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(bundleDir)

	if err := startLocalBundleRegistry(ctx, dockerClient, dockerNetwork); err != nil {
		return "", false, err
	}

	hostAddr := "localhost:" + localBundleRegistryHostPort
	bundleTag := fmt.Sprintf("%s/%s:%s", hostAddr, installer.GetBundleName(osBundle), k8sVersion)

	pushCmd := exec.CommandContext(ctx, imgpkgPath, "push", "-f", bundleDir, "-i", bundleTag) // #nosec G204 -- fixed args, no user input
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("failed to push local k8s bundle: %w\n%s", err, output)
	}

	return localBundleRegistryContainerName + ":5000", true, nil
}

// k8sBundleArchNames maps a Go GOARCH to the Debian architecture name (for apt/containerd
// downloads) and the OS-bundle name installer/registry.go's GetSupportedRegistry() resolves BYO
// hosts to (for the imgpkg tag). Only the two architectures registry.go actually wires up for
// Ubuntu 22.04 are supported -- see its AddOsFilter/AddBundleInstaller calls for
// Ubuntu_22.04_x86-64 and Ubuntu_22.04_arm64.
func k8sBundleArchNames(goarch string) (debianArch, osBundle string, err error) {
	switch goarch {
	case "arm64":
		return "arm64", "Ubuntu_22.04_arm64", nil
	case "amd64":
		return "amd64", "Ubuntu_22.04_x86-64", nil
	default:
		return "", "", fmt.Errorf("unsupported GOARCH %q for local k8s bundle build", goarch)
	}
}

// k8sMajorMinor extracts "v1.31" out of "v1.31.0".
func k8sMajorMinor(k8sVersion string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(k8sVersion, "v"), ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected Kubernetes version format %q", k8sVersion)
	}
	return "v" + parts[0] + "." + parts[1], nil
}

// buildK8sBundle runs installer/bundle_builder's existing Docker-based pipeline with BUILD_ONLY=1
// (build content, skip its own quay.io push -- test code below pushes it locally instead),
// mirroring .ci/build-push-bundle.sh, and returns the directory containing the built bundle
// content ready for `imgpkg push -f <dir> -i <addr>`. Caller owns removing the returned directory.
//
// The Debian package revision suffix ("-1.1") below was verified against pkgs.k8s.io's actual
// v1.31 apt repo contents at implementation time -- it's a packaging detail, not something
// derivable from k8sVersion, and isn't guaranteed to hold for other minor versions.
func buildK8sBundle(ctx context.Context, repoRoot, debianArch, k8sVersion string) (string, error) {
	majorMinor, err := k8sMajorMinor(k8sVersion)
	if err != nil {
		return "", err
	}
	kubernetesVersion := strings.TrimPrefix(k8sVersion, "v") + "-1.1"
	critoolVersion := strings.TrimPrefix(majorMinor, "v") + ".0-1.1"

	builderDir := filepath.Join(repoRoot, "installer", "bundle_builder")
	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", k8sBundleBuilderImageTag, builderDir) // #nosec G204 -- fixed args, no user input
	if output, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("failed to build bundle-builder image: %w\n%s", buildErr, output)
	}

	// Best-effort cleanup of a leftover container from a prior interrupted run.
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", k8sBundleBuilderContainer).Run() // #nosec G204 -- fixed args

	runCmd := exec.CommandContext(ctx, "docker", "run", //nolint:gosec // fixed args, no user input
		"--name", k8sBundleBuilderContainer,
		"-e", "BUILD_ONLY=1",
		"-e", "CONTAINERD_VERSION="+k8sBundleContainerdVersion,
		"-e", "KUBERNETES_VERSION="+kubernetesVersion,
		"-e", "KUBERNETES_MAJOR_VERSION="+majorMinor,
		"-e", "CRITOOL_VERSION="+critoolVersion,
		"-e", "ARCH="+debianArch,
		"-e", "UBUNTU_VERSION="+k8sBundleUbuntuVersion,
		k8sBundleBuilderImageTag,
	)
	if output, runErr := runCmd.CombinedOutput(); runErr != nil {
		return "", fmt.Errorf("failed to build k8s bundle content: %w\n%s", runErr, output)
	}
	defer func() { _ = exec.CommandContext(ctx, "docker", "rm", "-f", k8sBundleBuilderContainer).Run() }() // #nosec G204 -- fixed args

	bundleDir, err := os.MkdirTemp("", "byoh-k8s-bundle-*")
	if err != nil {
		return "", err
	}
	cpCmd := exec.CommandContext(ctx, "docker", "cp", k8sBundleBuilderContainer+":/bundle/.", bundleDir) // #nosec G204 -- fixed args, no user input
	if output, err := cpCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(bundleDir)
		return "", fmt.Errorf("failed to copy bundle out of builder container: %w\n%s", err, output)
	}

	return bundleDir, nil
}
