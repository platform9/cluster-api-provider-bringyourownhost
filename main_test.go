// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestClusterCacheOptions guards against a regression of the manager crash where
// clustercache.SetupWithManager rejected an empty Client.UserAgent with
// "options.Client.UserAgent must be set" and exited the process.
func TestClusterCacheOptions(t *testing.T) {
	secretClient := fake.NewClientBuilder().Build()

	opts := clusterCacheOptions(secretClient)

	require.NotEmpty(t, opts.Client.UserAgent)
	assert.Same(t, secretClient, opts.SecretClient)
}
