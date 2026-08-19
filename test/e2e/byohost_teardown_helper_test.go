// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	. "github.com/onsi/gomega"
)

// teardownByoHosts stops and removes each host's container and log file, then removes the shared
// controller-manager/all-pods log shell files. Container stop/remove failures fail the test;
// file-removal failures are only logged, matching the AfterEach cleanup contract of its callers.
func teardownByoHosts(ctx context.Context, dockerClient *client.Client, hosts []byoHostHandle) {
	for _, host := range hosts {
		if dockerClient != nil {
			err := dockerClient.ContainerStop(ctx, host.ContainerID, container.StopOptions{})
			Expect(err).NotTo(HaveOccurred())

			err = dockerClient.ContainerRemove(ctx, host.ContainerID, container.RemoveOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		if err := os.Remove(host.LogFilePath); err != nil {
			Showf("error removing file %s: %v", host.LogFilePath, err)
		}
	}

	if err := os.Remove(ReadByohControllerManagerLogShellFile); err != nil {
		Showf("error removing file %s: %v", ReadByohControllerManagerLogShellFile, err)
	}
	if err := os.Remove(ReadAllPodsShellFile); err != nil {
		Showf("error removing file %s: %v", ReadAllPodsShellFile, err)
	}
}
