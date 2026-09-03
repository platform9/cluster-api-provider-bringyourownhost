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
	goruntime "runtime"
	"strings"

	"github.com/docker/docker/client"
	"github.com/onsi/gomega/gexec"
)

const (
	fixtureAgentDebPackageName = "pf9-byohost-agent-fixture"
	fixtureAgentDebFileName    = "pf9-byohost-agent.deb"
)

// buildFixtureAgentBinary builds a real BYOH host agent binary with version.GitVersion baked in
// as gitVersion -- for use as a self-upgrade source/target in the agent-upgrade e2e specs. Reuses
// the same build flags e2e_suite_test.go's SynchronizedBeforeSuite uses for the suite's own main
// agent binary, just with a different, test-chosen version string, so the installed fixture is a
// fully working agent, not a stand-in.
func buildFixtureAgentBinary(gitVersion string) (string, error) {
	return gexec.BuildWithEnvironment(
		"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent",
		[]string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + goruntime.GOARCH},
		"-ldflags", "-X github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitVersion="+gitVersion,
	)
}

// buildFixtureAgentDeb packages binaryPath as a minimal .deb that installs it at
// systemdAgentBinaryPath -- the same path the real production package and this suite's systemd
// harness both use. Returns the directory containing the built .deb (imgpkg push -f wants a
// directory, not a single file).
//
// Deliberately hand-built with dpkg-deb rather than fpm: fpm/ruby are only installed in this
// repo's CI for the real build-host-agent-deb pipeline, gated on workflow_dispatch (see
// .github/workflows/e2e.yml and the CLAUDE.md gotcha about that target's skip/fail split) -- not
// present for every e2e run. dpkg-deb ships with any Debian-family base (confirmed present in
// golang:1.26.4, the base this suite's own linux-test-runner image builds from), so this needs no
// new dependency at all.
//
func buildFixtureAgentDeb(ctx context.Context, binaryPath, gitVersion string) (debDir string, err error) {
	stageDir, err := os.MkdirTemp("", "agent-fixture-deb-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stageDir) //nolint:errcheck // best-effort temp dir cleanup

	debianDir := filepath.Join(stageDir, "DEBIAN")
	if mkdirErr := os.MkdirAll(debianDir, 0755); mkdirErr != nil {
		return "", mkdirErr
	}
	// systemdAgentBinaryPath is "/binary/pf9-byoh-hostagent" -- dpkg-deb packs stageDir's tree
	// onto the target filesystem's root, so the payload lives at stageDir+that same path.
	payloadDir := filepath.Join(stageDir, filepath.Dir(systemdAgentBinaryPath))
	if mkdirErr := os.MkdirAll(payloadDir, 0755); mkdirErr != nil {
		return "", mkdirErr
	}

	binaryData, err := os.ReadFile(binaryPath) //nolint:gosec // binaryPath is a suite-built local binary path, not user input
	if err != nil {
		return "", err
	}
	if writeErr := os.WriteFile(filepath.Join(payloadDir, filepath.Base(systemdAgentBinaryPath)), binaryData, 0755); writeErr != nil { //nolint:gosec // the payload must be executable
		return "", writeErr
	}

	// GOARCH happens to already match Debian's architecture naming for both values this repo's
	// Makefile packages for (amd64/arm64) -- see Makefile's PACKAGE_GOARCH.
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: byoh-e2e\nDescription: agent-upgrade e2e fixture package\n",
		fixtureAgentDebPackageName, strings.TrimPrefix(gitVersion, "v"), goruntime.GOARCH)
	if writeErr := os.WriteFile(filepath.Join(debianDir, "control"), []byte(control), 0644); writeErr != nil {
		return "", writeErr
	}

	postinst := "#!/bin/sh\nset -e\nchmod +x " + systemdAgentBinaryPath + "\n"
	if writeErr := os.WriteFile(filepath.Join(debianDir, "postinst"), []byte(postinst), 0755); writeErr != nil { //nolint:gosec // dpkg requires postinst to be executable
		return "", writeErr
	}

	outDir, err := os.MkdirTemp("", "agent-fixture-bundle-*")
	if err != nil {
		return "", err
	}
	outPath := filepath.Join(outDir, fixtureAgentDebFileName)
	buildCmd := exec.CommandContext(ctx, "dpkg-deb", "--build", "--root-owner-group", stageDir, outPath) // #nosec G204 -- fixed args, stageDir/outPath are our own temp dirs
	if output, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("dpkg-deb --build failed: %w\n%s", buildErr, output)
	}

	return outDir, nil
}

// pushFixtureAgentBundle imgpkg-pushes the .deb in debDir to the shared local bundle registry
// (see e2e_agent_bundle_registry.go) under tag, returning the address containers on dockerNetwork
// can pull it from. Starts the registry itself if it isn't already running -- a typical local
// `make test-e2e-linux-vm` run never builds the real agent bundle
// (build/pf9-byohost/debsrc/pf9-byohost-agent.deb), so ensureLocalAgentBundleRegistry may never
// have started it.
//
// Returns the registry's container IP on dockerNetwork, not its container-name alias: imgpkg
// (via go-containerregistry's name.Registry.Scheme) only skips TLS automatically for RFC1918/
// loopback addresses, not arbitrary hostnames -- pointing agent/cloudinit/cmd_runner.go's
// unconfigurable `imgpkg pull` at the real private IP gets that for free, with no wrapper script
// or registry-insecure flag needed anywhere.
func pushFixtureAgentBundle(ctx context.Context, dockerClient *client.Client, dockerNetwork, debDir, tag string) (string, error) {
	repoRoot, err := resolveRepoRoot(ctx)
	if err != nil {
		return "", err
	}
	if startErr := startLocalBundleRegistry(ctx, dockerClient, dockerNetwork); startErr != nil {
		return "", startErr
	}
	imgpkgPath, err := downloadImgpkg(ctx, repoRoot)
	if err != nil {
		return "", err
	}

	hostAddr := "localhost:" + localBundleRegistryHostPort
	pushCmd := exec.CommandContext(ctx, imgpkgPath, "push", "-f", debDir, "-i", hostAddr+"/"+tag) // #nosec G204 -- fixed args, no user input
	if output, pushErr := pushCmd.CombinedOutput(); pushErr != nil {
		return "", fmt.Errorf("failed to push fixture agent bundle: %w\n%s", pushErr, output)
	}

	registryIP, err := localBundleRegistryIP(ctx, dockerClient, dockerNetwork)
	if err != nil {
		return "", err
	}
	return registryIP + ":5000/" + tag, nil
}

// localBundleRegistryIP returns the shared local bundle registry's own IP on dockerNetwork.
func localBundleRegistryIP(ctx context.Context, dockerClient *client.Client, dockerNetwork string) (string, error) {
	inspect, err := dockerClient.ContainerInspect(ctx, localBundleRegistryContainerName)
	if err != nil {
		return "", err
	}
	endpoint, ok := inspect.NetworkSettings.Networks[dockerNetwork]
	if !ok || endpoint.IPAddress == "" {
		return "", fmt.Errorf("local bundle registry has no IP on network %q", dockerNetwork)
	}
	return endpoint.IPAddress, nil
}

// installImgpkgOnHost puts imgpkg on containerID's PATH -- production hosts get it via the
// k8s-installer's self-install fallback, which this suite's hosts skip (never join a cluster).
func installImgpkgOnHost(ctx context.Context, dockerClient *client.Client, containerID, repoRoot string) error {
	imgpkgPath, err := downloadImgpkg(ctx, repoRoot)
	if err != nil {
		return err
	}
	return copyToContainer(ctx, dockerClient, cpConfig{
		sourcePath: imgpkgPath,
		destPath:   "/usr/local/bin/imgpkg",
		container:  containerID,
	})
}
