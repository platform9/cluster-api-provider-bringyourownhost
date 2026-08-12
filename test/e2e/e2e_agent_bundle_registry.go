// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	. "github.com/onsi/gomega" //nolint: staticcheck
)

const (
	// override byohctl's onboarding downloads .deb bundle from quay.io tagged with byohctl's
	// own git-describe version
	byohAgentBundleURLEnvVar = "BYOH_AGENT_BUNDLE_URL"

	// Mirrors cmd/byohctl/service/constants.go's constant of the same name (separate module, no import).
	byohAgentBundleInsecureEnvVar = "BYOH_AGENT_BUNDLE_INSECURE_REGISTRY"

	// localAgentBundleDebDir is where `make build-host-agent-deb` (root Makefile) leaves the built
	// agent .deb -- see PF9_BYOHOST_SRCDIR/DEB_SRC_ROOT in the Makefile.
	localAgentBundleDebDir  = "build/pf9-byohost/debsrc"
	localAgentBundleDebFile = "pf9-byohost-agent.deb"

	agentBundleRegistryContainerName = "byohctl-e2e-agent-bundle-registry"
	agentBundleRegistryHostPort      = "5050"
	agentBundleImageTag              = "agent:e2e"
)

// ensureLocalAgentBundleRegistry makes an agent .deb bundle reachable by byohctl's SetupAgent
// (cmd/byohctl/service/constants.go's byohAgentBundleURL, via the BYOH_AGENT_BUNDLE_URL override)
//
// Two addresses exist for the same registry because "localhost" means something different on
// each side of a container boundary
func ensureLocalAgentBundleRegistry(ctx context.Context, dockerClient *client.Client, dockerNetwork string) (string, error) {
	if override := os.Getenv(byohAgentBundleURLEnvVar); override != "" {
		return override, nil
	}

	repoRoot, err := resolveRepoRoot(ctx)
	if err != nil {
		return "", err
	}
	debDir := filepath.Join(repoRoot, localAgentBundleDebDir)
	if _, statErr := os.Stat(filepath.Join(debDir, localAgentBundleDebFile)); statErr != nil {
		// No pre-built bundle (e.g. a local `make test-e2e` run that skipped the CI-only
		// `make build-host-agent-deb` step) -- nothing to serve, caller's spec should skip.
		return "", nil
	}

	imgpkgPath, err := downloadImgpkg(ctx, repoRoot)
	if err != nil {
		return "", err
	}

	if err := startAgentBundleRegistry(ctx, dockerClient, dockerNetwork); err != nil {
		return "", err
	}

	hostAddr := "localhost:" + agentBundleRegistryHostPort
	Eventually(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostAddr+"/v2/", http.NoBody) // #nosec G107 -- fixed, hardcoded local address
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}, "30s", "1s").Should(Succeed(), "local agent-bundle registry never became reachable")

	pushCmd := exec.CommandContext(ctx, imgpkgPath, "push", "-f", debDir, "-i", hostAddr+"/"+agentBundleImageTag) // #nosec G204 -- fixed args, no user input
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to push local agent bundle: %w\n%s", err, output)
	}

	return agentBundleRegistryContainerName + ":5000/" + agentBundleImageTag, nil
}

func startAgentBundleRegistry(ctx context.Context, dockerClient *client.Client, dockerNetwork string) error {
	// Best-effort cleanup of a leftover container from a prior interrupted run -- ignore errors,
	// this is purely so ContainerCreate below doesn't fail on a name conflict.
	_ = dockerClient.ContainerRemove(ctx, agentBundleRegistryContainerName, types.ContainerRemoveOptions{Force: true})

	// Unlike the docker CLI, the SDK's ContainerCreate doesn't auto-pull a missing image -- it
	// just fails with "No such image". Pull explicitly first; the returned reader must be drained
	// for the pull to actually complete before ContainerCreate runs.
	pullReader, err := dockerClient.ImagePull(ctx, "registry:2", types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull registry:2: %w", err)
	}
	_, err = io.Copy(io.Discard, pullReader)
	closeErr := pullReader.Close()
	if err != nil {
		return fmt.Errorf("failed to pull registry:2: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to pull registry:2: %w", closeErr)
	}

	created, err := dockerClient.ContainerCreate(ctx,
		&container.Config{Image: "registry:2"},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				"5000/tcp": []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: agentBundleRegistryHostPort}},
			},
		},
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{dockerNetwork: {}}},
		nil, agentBundleRegistryContainerName)
	if err != nil {
		return fmt.Errorf("failed to create local agent-bundle registry container: %w", err)
	}
	if err := dockerClient.ContainerStart(ctx, created.ID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start local agent-bundle registry container: %w", err)
	}
	return nil
}

// resolveRepoRoot finds the repository root regardless of the test binary's working directory,
// matching cmd/byohctl/Makefile's own REPO_ROOT := $(shell git rev-parse --show-toplevel).
func resolveRepoRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output() // #nosec G204 -- fixed args, no user input
	if err != nil {
		return "", fmt.Errorf("failed to resolve repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// downloadImgpkg gets the imgpkg CLI via the root Makefile's own imgpkg target, rather than
// reimplementing that download here. That target installs it with `go install`, verified by Go's
// checksum database -- so this also picks up architecture-correctness for free (e.g.
// test-e2e-linux-vm's container is arm64 on Apple Silicon), without hardcoding GOARCH here too.
func downloadImgpkg(ctx context.Context, repoRoot string) (string, error) {
	cmd := exec.CommandContext(ctx, "make", "imgpkg") // #nosec G204 -- fixed args, no user input
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to download imgpkg: %w\n%s", err, output)
	}
	return filepath.Join(repoRoot, "bin", "imgpkg"), nil
}
