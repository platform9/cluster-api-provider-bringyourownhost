// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"sigs.k8s.io/cluster-api/util"
)

// byoHostHandle's Output and LogFile are returned open: the caller owns closing them
// (typically deferred right after spinUpByoHosts returns) so agent logs keep streaming
// for the rest of the spec.
type byoHostHandle struct {
	Name        string
	ContainerID string
	Output      types.HijackedResponse
	LogFile     *os.File
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
		output, containerID, err := runner.ExecByoDockerHost(byohost)
		if err != nil {
			return hosts, err
		}

		logFilePath := fmt.Sprintf("/tmp/host-agent-%s.log", byoHostName)
		hosts = append(hosts, byoHostHandle{
			Name:        byoHostName,
			ContainerID: containerID,
			Output:      output,
			LogFile:     WriteDockerLog(output, logFilePath),
			LogFilePath: logFilePath,
		})
	}

	return hosts, nil
}
