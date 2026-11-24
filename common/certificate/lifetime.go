// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package certificate holds the certificate settings that the host agent and
// the manager must agree on.
package certificate

// DefaultLifetimeSeconds is the host certificate lifetime the agent requests
// by default, aligned with kubeadm defaults. The approver's default maximum
// is the same value, so a default agent is never denied.
const DefaultLifetimeSeconds = 86400 * 365
