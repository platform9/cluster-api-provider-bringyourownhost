// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Deliberately its own Ginkgo suite rather than a spec inside
// test/e2e: that suite's SynchronizedBeforeSuite stands up a
// full kind bootstrap cluster.
package packaging_test

import (
	"context"
	"testing"

	"github.com/docker/docker/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const packagingTestImage = "byoh/packaging-test-rocky:dev"

var (
	ctx          context.Context
	dockerClient *client.Client
)

func TestPackaging(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Packaging Suite")
}

var _ = BeforeSuite(func() {
	ctx = context.Background()

	var err error
	dockerClient, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	Expect(err).NotTo(HaveOccurred())
})
