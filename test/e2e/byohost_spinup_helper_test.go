// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"sigs.k8s.io/cluster-api/util"
)

// byoHostHandle's agent log keeps streaming until StopLog is called (typically deferred
// right after spinUpByoHosts returns), which closes the docker stream and the log file.
type byoHostHandle struct {
	Name        string
	ContainerID string
	StopLog     func()
	LogFilePath string
}

// spinUpByoHosts creates count BYO hosts, each running agentBinaryPath as a bare, attached
// `docker exec` process. On error it returns the handles created so far alongside the error.
func spinUpByoHosts(ctx context.Context, dockerClient *client.Client, namespace string, count int) ([]byoHostHandle, error) {
	return spinUpByoHostsCommon(ctx, dockerClient, namespace, count, pathToHostAgentBinary,
		func(runner *ByoHostRunner, byohost *container.CreateResponse) (func(), string, error) {
			output, _, err := runner.ExecByoDockerHost(byohost)
			if err != nil {
				return nil, "", err
			}
			logFilePath := fmt.Sprintf("/tmp/host-agent-%s.log", runner.ByoHostName)
			return StreamDockerLog(output, logFilePath), logFilePath, nil
		})
}

// spinUpByoHostsCommon creates count BYO hosts and, for each, calls startAgent once the container
// exists -- shared between spinUpByoHosts (bare docker exec) and
// spinUpByoHostsWithSystemdAgent (systemd unit), which differ only in how the agent process
// actually gets started. A host is always appended to the returned slice before startAgent runs,
// so a failure partway through it still leaves the container tracked for the caller's
// teardownByoHosts -- otherwise its ID is lost and the container leaks.
func spinUpByoHostsCommon(ctx context.Context, dockerClient *client.Client, namespace string, count int, agentBinaryPath string,
	startAgent func(runner *ByoHostRunner, byohost *container.CreateResponse) (stopLog func(), logFilePath string, err error),
) ([]byoHostHandle, error) {

	hosts := make([]byoHostHandle, 0, count)

	for i := 0; i < count; i++ {
		byoHostName := fmt.Sprintf("byohost-%s", util.RandomString(6))

		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			ByoHostName:           byoHostName,
			Namespace:             namespace,
			PathToHostAgentBinary: agentBinaryPath,
			DockerClient:          dockerClient,
			NetworkInterface:      dockerNetworkInterfaceKind,
			Env:                   byoHostRunnerEnv(),
			bootstrapClusterProxy: bootstrapClusterProxy,
			CommandArgs: map[string]string{
				agentFlagBootstrapKubeconfig: bootstrapConfPath,
				agentFlagNamespace:           namespace,
				agentFlagVerbosity:           "1",
			},
			BootstrapKubeconfigData: generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName),
		}

		byohost, err := runner.SetupByoDockerHost()
		if err != nil {
			return hosts, err
		}

		hosts = append(hosts, byoHostHandle{
			Name:        byoHostName,
			ContainerID: byohost.ID,
			StopLog:     func() {},
			LogFilePath: "",
		})

		stopLog, logFilePath, err := startAgent(&runner, byohost)
		if err != nil {
			return hosts, err
		}
		hosts[len(hosts)-1].StopLog = stopLog
		hosts[len(hosts)-1].LogFilePath = logFilePath
	}

	return hosts, nil
}
