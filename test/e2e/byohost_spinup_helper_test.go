// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"

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

// spinUpByoHosts creates count BYO hosts. On error it returns the handles created so
// far alongside the error.
func spinUpByoHosts(ctx context.Context, dockerClient *client.Client, namespace string, count int) ([]byoHostHandle, error) {
	hosts := make([]byoHostHandle, 0, count)

	for i := 0; i < count; i++ {
		byoHostName := fmt.Sprintf("byohost-%s", util.RandomString(6))

		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			ByoHostName:           byoHostName,
			Namespace:             namespace,
			PathToHostAgentBinary: pathToHostAgentBinary,
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

		output, _, err := runner.ExecByoDockerHost(byohost)
		if err != nil {
			return hosts, err
		}

		logFilePath := fmt.Sprintf("/tmp/host-agent-%s.log", byoHostName)
		hosts[len(hosts)-1].StopLog = StreamDockerLog(output, logFilePath)
		hosts[len(hosts)-1].LogFilePath = logFilePath
	}

	return hosts, nil
}
