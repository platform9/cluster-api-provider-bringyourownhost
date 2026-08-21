// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/pkg/errors"
)

// The e2e harness's default spinUpByoHosts path (byohost_spinup_helper_test.go) starts the agent
// as a bare, attached `docker exec` child process -- fine for every existing spec, but the
// self-upgrade mechanism's os.Exit(0)-then-relaunch (docs/proposals/agent-self-upgrade-adr.md
// §2.2 step 5) depends on the real pf9-byohostagent.service systemd unit's Restart=always, which
// that path never installs. spinUpByoHostsWithSystemdAgent below is the alternative host
// provisioning path the agent-upgrade specs need instead.
const (
	// systemdAgentBinaryPath matches ConditionPathExists/ExecStart in
	// service/pf9-byohostagent.service.
	systemdAgentBinaryPath = "/binary/pf9-byoh-hostagent"
	// systemdAgentServiceUnitSrcPath is repo-root-relative -- see installSystemdAgentUnit's
	// resolveRepoRoot call for how that's resolved.
	systemdAgentServiceUnitSrcPath = "service/pf9-byohostagent.service"
	// systemdAgentServiceUnitName matches the name the real packaging pipeline installs this same
	// unit file under (Makefile's COMMON_SRC_ROOT rule renames it on copy).
	systemdAgentServiceUnitName = "pf9-byohost-agent.service"
	systemdAgentEnvFileDir      = "/etc/pf9-byohost-agent.service.d"
	// systemdAgentEnvFilePath matches EnvironmentFile= in service/pf9-byohostagent.service.
	systemdAgentEnvFilePath = systemdAgentEnvFileDir + "/pf9-byohost-agent.conf"
	systemdAgentLogDir      = "/var/log/pf9/byoh"
	// systemdAgentLogFile matches the ExecStart redirect in service/pf9-byohostagent.service.
	systemdAgentLogFile = systemdAgentLogDir + "/byoh-agent.log"
)

// spinUpByoHostsWithSystemdAgent creates count BYO hosts whose agent runs under systemd (see the
// package comment above for why). agentBinaryPath is normally pathToHostAgentBinary, but the
// agent-upgrade rollout scenario needs hosts to start on a known, test-controlled version rather
// than whatever this suite's own build produces, so it's a parameter rather than hardcoded. On
// error it returns the handles created so far alongside the error.
func spinUpByoHostsWithSystemdAgent(ctx context.Context, dockerClient *client.Client, namespace string, count int, agentBinaryPath string) ([]byoHostHandle, error) {
	return spinUpByoHostsCommon(ctx, dockerClient, namespace, count, agentBinaryPath,
		func(runner *ByoHostRunner, byohost *container.CreateResponse) (func(), string, error) {
			if err := installSystemdAgentUnit(ctx, dockerClient, byohost.ID, namespace, agentBinaryPath); err != nil {
				return nil, "", err
			}
			logFilePath := fmt.Sprintf("/tmp/host-agent-%s.log", runner.ByoHostName)
			return copySystemdAgentLog(ctx, dockerClient, byohost.ID, logFilePath), logFilePath, nil
		})
}

// installSystemdAgentUnit copies the real agent binary and systemd unit into containerID at the
// paths the unit expects, writes its EnvironmentFile, and enables+starts it.
func installSystemdAgentUnit(ctx context.Context, dockerClient *client.Client, containerID, namespace, agentBinaryPath string) error {
	if err := copyToContainer(ctx, dockerClient, cpConfig{
		sourcePath: agentBinaryPath,
		destPath:   systemdAgentBinaryPath,
		container:  containerID,
	}); err != nil {
		return errors.Wrap(err, "copy agent binary to systemd ExecStart path")
	}

	// The ginkgo CLI runs the compiled test binary with its cwd set to the package directory
	// (test/e2e), not the repo root -- resolveRepoRoot (already used by
	// e2e_agent_bundle_registry.go for the same reason) finds the real root regardless.
	repoRoot, err := resolveRepoRoot(ctx)
	if err != nil {
		return errors.Wrap(err, "resolve repo root for systemd unit source path")
	}
	if copyErr := copyToContainer(ctx, dockerClient, cpConfig{
		sourcePath: filepath.Join(repoRoot, systemdAgentServiceUnitSrcPath),
		destPath:   "/etc/systemd/system/" + systemdAgentServiceUnitName,
		container:  containerID,
	}); copyErr != nil {
		return errors.Wrap(copyErr, "copy pf9-byohost-agent systemd unit")
	}

	if mkdirErr := runContainerCommand(ctx, dockerClient, containerID,
		"mkdir", "-p", systemdAgentEnvFileDir, systemdAgentLogDir); mkdirErr != nil {
		return errors.Wrap(mkdirErr, "create systemd agent config/log directories")
	}

	envFileLocal, err := uniqueTempFilePath("pf9-byohost-agent-*.conf")
	if err != nil {
		return errors.Wrap(err, "allocate local temp path for systemd agent EnvironmentFile")
	}
	defer os.Remove(envFileLocal) //nolint:errcheck // best-effort local temp file cleanup

	// BOOTSTRAP_KUBECONFIG points at the same in-container path SetupByoDockerHost already wrote
	// the kubeconfig to (bootstrapConfPath) -- no need for a second copy at the path
	// docs/agent-upgrade.md's onboarding flow uses, since this harness controls both sides.
	// REGION must be "key=value" -- the unit's ExecStart passes it straight through as
	// --label "$REGION", and agent/main.go's labelFlags.Set rejects anything without an "=".
	// PATH is set explicitly (covering /usr/local/bin, where installImgpkgOnHost places its
	// imgpkg wrapper) since the real agent finds it via a plain exec.LookPath("imgpkg"), and
	// systemd's own default PATH for services isn't guaranteed to include it.
	envContent := fmt.Sprintf("NAMESPACE=%s\nBOOTSTRAP_KUBECONFIG=%s\nREGION=region=e2e\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n", namespace, bootstrapConfPath)
	if err := os.WriteFile(envFileLocal, []byte(envContent), 0600); err != nil {
		return errors.Wrap(err, "write local systemd agent EnvironmentFile")
	}

	if err := copyToContainer(ctx, dockerClient, cpConfig{
		sourcePath: envFileLocal,
		destPath:   systemdAgentEnvFilePath,
		container:  containerID,
	}); err != nil {
		return errors.Wrap(err, "copy systemd agent EnvironmentFile")
	}

	return runContainerCommand(ctx, dockerClient, containerID, "sh", "-c",
		"systemctl daemon-reload && systemctl enable "+systemdAgentServiceUnitName+" && systemctl start "+systemdAgentServiceUnitName)
}

// runContainerCommand execs cmd inside containerID and returns an error including captured
// output if it exits non-zero -- unlike raiseInotifyInstanceLimit's fire-and-forget ExecStart,
// callers here need to know if e.g. `systemctl start` actually failed.
func runContainerCommand(ctx context.Context, dockerClient *client.Client, containerID string, cmd ...string) error {
	_, err := containerCommandOutput(ctx, dockerClient, containerID, cmd...)
	return err
}

// containerCommandOutput is runContainerCommand's sibling for callers that need the command's
// stdout/stderr, not just success/failure (e.g. reading back a MainPID).
func containerCommandOutput(ctx context.Context, dockerClient *client.Client, containerID string, cmd ...string) (string, error) {
	execCmd, err := dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	})
	if err != nil {
		return "", err
	}
	resp, err := dockerClient.ContainerExecAttach(ctx, execCmd.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	// ContainerExecCreate above didn't set Tty, so the attach stream is stdcopy's multiplexed
	// stdout/stderr framing, not plain bytes -- read it raw and any output we try to parse (e.g.
	// MainPID) silently corrupts. Demux both into one buffer; ordering between the two doesn't
	// matter for the diagnostic-string/parsed-value use cases here.
	var output bytes.Buffer
	_, _ = stdcopy.StdCopy(&output, &output, resp.Reader) //nolint:errcheck // best-effort diagnostic output, exit-code check below is authoritative
	resp.Close()

	inspect, err := dockerClient.ContainerExecInspect(ctx, execCmd.ID)
	if err != nil {
		return "", err
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("command %v exited %d: %s", cmd, inspect.ExitCode, output.String())
	}
	return output.String(), nil
}

// copySystemdAgentLog returns a byoHostHandle.StopLog-shaped closer that, unlike the live stream
// StreamDockerLog attaches for the bare-exec path, reads the systemd-supervised agent's log file
// (ExecStart redirects there, not to a docker exec stream) once, at call time -- typically
// deferred to right before teardown, same calling convention.
func copySystemdAgentLog(ctx context.Context, dockerClient *client.Client, containerID, localPath string) func() {
	return func() {
		execCmd, err := dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Cmd:          []string{"cat", systemdAgentLogFile},
		})
		if err != nil {
			return
		}
		resp, err := dockerClient.ContainerExecAttach(ctx, execCmd.ID, container.ExecAttachOptions{})
		if err != nil {
			return
		}
		defer resp.Close()

		f, err := os.Create(localPath) //nolint:gosec // localPath is test-generated (fmt.Sprintf with a random suffix), not user input
		if err != nil {
			return
		}
		defer f.Close()
		// Same multiplexed-stream caveat as containerCommandOutput -- demux, don't read raw.
		_, _ = stdcopy.StdCopy(f, f, resp.Reader) //nolint:errcheck // best-effort log snapshot for test diagnostics
	}
}

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
