// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

// RehomeIdentityFileName holds the identity written by `byohctl rehome`.
const RehomeIdentityFileName = "rehome-identity"

// ReadRehomeIdentity returns the rehome identity, or "" if there is none.
// It errors if the identity is for another host or tenant.
func ReadRehomeIdentity(stateDir, tenantNamespace, normalizedHostName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, RehomeIdentityFileName))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read rehome identity: %w", err)
	}

	value := strings.TrimSpace(string(data))
	parsed, err := identity.Parse(value)
	if err != nil {
		return "", fmt.Errorf("rehome identity %q: %w", value, err)
	}
	if parsed.TenantNamespace != tenantNamespace || parsed.NormalizedHostName != normalizedHostName {
		return "", fmt.Errorf("rehome identity %q is not for host %q in tenant %q", value, normalizedHostName, tenantNamespace)
	}
	return value, nil
}

// RemoveRehomeIdentity deletes the rehome identity file.
func RemoveRehomeIdentity(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, RehomeIdentityFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove rehome identity: %w", err)
	}
	return nil
}
