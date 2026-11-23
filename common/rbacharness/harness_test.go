// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/rbacharness"
)

// harnesses holds one running API server per RBAC source.
var harnesses = map[rbacharness.Source]*rbacharness.Harness{}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	defer func() {
		for source, h := range harnesses {
			err := h.Stop()
			if err != nil {
				fmt.Fprintf(os.Stderr, "stop harness for %s: %v\n", source, err)
			}
		}
	}()

	for _, source := range rbacharness.Sources() {
		h, err := rbacharness.Start(context.Background(), source)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start harness for %s: %v\n", source, err)
			return 1
		}
		harnesses[source] = h
	}
	return m.Run()
}

func TestHarness_Allowed(t *testing.T) {
	createCSR := authv1.ResourceAttributes{
		Verb:     "create",
		Group:    "certificates.k8s.io",
		Resource: "certificatesigningrequests",
	}
	getSecret := authv1.ResourceAttributes{
		Verb:      "get",
		Resource:  "secrets",
		Namespace: rbacharness.TenantA,
	}

	hostWithoutGroup := rbacharness.HostSubject()
	hostWithoutGroup.Groups = []string{"system:authenticated"}

	testCases := []struct {
		name      string
		subject   rbacharness.Subject
		attrs     authv1.ResourceAttributes
		wantAllow bool
	}{
		{
			name:      "bootstrap identity creates CSRs",
			subject:   rbacharness.BootstrapSubject(),
			attrs:     createCSR,
			wantAllow: true,
		},
		{
			name:      "bootstrap identity reads a Secret in tenant-a",
			subject:   rbacharness.BootstrapSubject(),
			attrs:     getSecret,
			wantAllow: false,
		},
		{
			name:      "user without groups creates CSRs",
			subject:   rbacharness.Subject{User: "nobody"},
			attrs:     createCSR,
			wantAllow: false,
		},
		{
			name:      "user without groups reads a Secret in tenant-a",
			subject:   rbacharness.Subject{User: "nobody"},
			attrs:     getSecret,
			wantAllow: false,
		},
		{
			name:      "byoh:hosts group reads a Secret in tenant-a",
			subject:   rbacharness.HostSubject(),
			attrs:     getSecret,
			wantAllow: true,
		},
		{
			name:      "host identity without the byoh:hosts group reads a Secret in tenant-a",
			subject:   hostWithoutGroup,
			attrs:     getSecret,
			wantAllow: false,
		},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), tc.subject, &tc.attrs)
				require.NoError(t, err)
				assert.Equal(t, tc.wantAllow, got)
			})
		}
	}
}
