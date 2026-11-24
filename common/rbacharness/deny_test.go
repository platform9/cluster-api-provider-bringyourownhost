// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/rbacharness"
	controllers "github.com/platform9/cluster-api-provider-bringyourownhost/controllers/infrastructure"
)

const (
	capiGroup = "cluster.x-k8s.io"
	rbacGroup = "rbac.authorization.k8s.io"

	deniedHost = "h3-qrs77"
)

// createDeniedHostRBAC creates the Role and RoleBinding of host h3-qrs77, which
// no other test uses, and returns once the authorizer reflects them.
func createDeniedHostRBAC(t *testing.T, h *rbacharness.Harness, host rbacharness.Subject) {
	t.Helper()

	role, binding := controllers.HostScopedRBAC(identity.HostIdentity{
		TenantNamespace:    rbacharness.TenantA,
		NormalizedHostName: "h3",
		Suffix:             "qrs77",
	})
	err := h.CreateRBAC(t.Context(), role, binding)
	require.NoError(t, err)

	attrs := &authv1.ResourceAttributes{
		Verb:      "get",
		Group:     byohGroup,
		Resource:  "byohosts",
		Namespace: rbacharness.TenantA,
		Name:      deniedHost,
	}
	require.Eventually(t, func() bool {
		got, reviewErr := h.Allowed(t.Context(), host, attrs)
		return reviewErr == nil && got
	}, 10*time.Second, 100*time.Millisecond, "authorizer did not see the Role of %s", deniedHost)
}

// TestRBAC_HostDeniedRights lists the rights a host must never hold. The
// identity holds every group of a real certificate, so the rows also cover the
// cluster-wide bindings of byoh:hosts. A change that adds one of these rights
// to a shipped file or to the Role of a host fails the matching row.
func TestRBAC_HostDeniedRights(t *testing.T) {
	host1 := rbacharness.Subject{
		User: "byoh:host:tenant-a:" + deniedHost,
		Groups: []string{
			"byoh:hosts",
			identity.TenantGroupPrefix + rbacharness.TenantA,
			"system:authenticated",
		},
	}

	resource := func(verb, group, resource, subresource, name string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{
			Verb:        verb,
			Group:       group,
			Resource:    resource,
			Subresource: subresource,
			Namespace:   rbacharness.TenantA,
			Name:        name,
		}
	}

	testCases := []struct {
		name  string
		attrs authv1.ResourceAttributes
	}{
		{name: "create bootstrapkubeconfigs", attrs: resource("create", byohGroup, "bootstrapkubeconfigs", "", "")},
		{name: "update bootstrapkubeconfigs status", attrs: resource("update", byohGroup, "bootstrapkubeconfigs", "status", "")},
		{
			name:  "update bootstrapkubeconfigs status by name",
			attrs: resource("update", byohGroup, "bootstrapkubeconfigs", "status", deniedHost),
		},
		{name: "create roles", attrs: resource("create", rbacGroup, "roles", "", "")},
		{name: "create rolebindings", attrs: resource("create", rbacGroup, "rolebindings", "", "")},
		{name: "update machines", attrs: resource("update", capiGroup, "machines", "", "")},
		{name: "update machines by name", attrs: resource("update", capiGroup, "machines", "", deniedHost)},
		{name: "patch machines", attrs: resource("patch", capiGroup, "machines", "", "")},
		{name: "patch machines by name", attrs: resource("patch", capiGroup, "machines", "", deniedHost)},
		{name: "update machinedeployments", attrs: resource("update", capiGroup, "machinedeployments", "", "")},
		{name: "update machinedeployments by name", attrs: resource("update", capiGroup, "machinedeployments", "", deniedHost)},
		{name: "patch machinedeployments", attrs: resource("patch", capiGroup, "machinedeployments", "", "")},
		{name: "patch machinedeployments by name", attrs: resource("patch", capiGroup, "machinedeployments", "", deniedHost)},
		{name: "update machinedeployments scale", attrs: resource("update", capiGroup, "machinedeployments", "scale", "")},
		{
			name:  "update machinedeployments scale by name",
			attrs: resource("update", capiGroup, "machinedeployments", "scale", deniedHost),
		},
		{name: "patch machinedeployments scale", attrs: resource("patch", capiGroup, "machinedeployments", "scale", "")},
		{
			name:  "patch machinedeployments scale by name",
			attrs: resource("patch", capiGroup, "machinedeployments", "scale", deniedHost),
		},
		{name: "create serviceaccounts token", attrs: resource("create", "", "serviceaccounts", "token", "")},
		{name: "list secrets", attrs: resource("list", "", "secrets", "", "")},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		createDeniedHostRBAC(t, h, host1)

		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), host1, &tc.attrs)
				require.NoError(t, err)
				assert.False(t, got)
			})
		}
	}
}
