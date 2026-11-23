// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	certv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/rbacharness"
)

const (
	byohGroup = "infrastructure.cluster.x-k8s.io"
	csrGroup  = "certificates.k8s.io"
	ownHost   = "h1-abc12"
)

// TestRBAC_HostKeepsAccess lists the access the agent and byohctl need. A
// later change that tightens the RBAC must keep every row allowed.
func TestRBAC_HostKeepsAccess(t *testing.T) {
	ownHostSelector := &authv1.FieldSelectorAttributes{
		Requirements: []metav1.FieldSelectorRequirement{
			{
				Key:      "metadata.name",
				Operator: metav1.FieldSelectorOpIn,
				Values:   []string{ownHost},
			},
		},
	}

	testCases := []struct {
		name  string
		attrs authv1.ResourceAttributes
	}{
		{
			name: "create own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "get own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "list own ByoHost with the name selector",
			attrs: authv1.ResourceAttributes{
				Verb:          "list",
				Group:         byohGroup,
				Resource:      "byohosts",
				Namespace:     rbacharness.TenantA,
				Name:          ownHost,
				FieldSelector: ownHostSelector,
			},
		},
		{
			name: "watch own ByoHost with the name selector",
			attrs: authv1.ResourceAttributes{
				Verb:          "watch",
				Group:         byohGroup,
				Resource:      "byohosts",
				Namespace:     rbacharness.TenantA,
				Name:          ownHost,
				FieldSelector: ownHostSelector,
			},
		},
		{
			name: "update own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "update",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "patch own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "patch",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "delete own ByoHost",
			attrs: authv1.ResourceAttributes{
				Verb:      "delete",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
				Name:      ownHost,
			},
		},
		{
			name: "get own ByoHost status",
			attrs: authv1.ResourceAttributes{
				Verb:        "get",
				Group:       byohGroup,
				Resource:    "byohosts",
				Subresource: "status",
				Namespace:   rbacharness.TenantA,
				Name:        ownHost,
			},
		},
		{
			name: "patch own ByoHost status",
			attrs: authv1.ResourceAttributes{
				Verb:        "patch",
				Group:       byohGroup,
				Resource:    "byohosts",
				Subresource: "status",
				Namespace:   rbacharness.TenantA,
				Name:        ownHost,
			},
		},
		{
			name: "create Events in the tenant",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Resource:  "events",
				Namespace: rbacharness.TenantA,
			},
		},
		{
			name: "patch Events in the tenant",
			attrs: authv1.ResourceAttributes{
				Verb:      "patch",
				Resource:  "events",
				Namespace: rbacharness.TenantA,
			},
		},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), rbacharness.HostSubject(), &tc.attrs)
				require.NoError(t, err)
				assert.True(t, got)
			})
		}
	}
}

// TestRBAC_BootstrapAccess pins the access of a bootstrap token: the CSR verbs
// the agent uses and nothing else.
func TestRBAC_BootstrapAccess(t *testing.T) {
	csr := func(verb string) authv1.ResourceAttributes {
		return authv1.ResourceAttributes{
			Verb:     verb,
			Group:    csrGroup,
			Resource: "certificatesigningrequests",
		}
	}
	csrSubresource := func(verb, subresource string) authv1.ResourceAttributes {
		attrs := csr(verb)
		attrs.Subresource = subresource
		return attrs
	}

	testCases := []struct {
		name      string
		attrs     authv1.ResourceAttributes
		wantAllow bool
	}{
		{name: "create CSRs", attrs: csr("create"), wantAllow: true},
		{name: "get CSRs", attrs: csr("get"), wantAllow: true},
		{name: "list CSRs", attrs: csr("list"), wantAllow: true},
		{name: "watch CSRs", attrs: csr("watch"), wantAllow: true},
		{name: "update CSRs", attrs: csr("update"), wantAllow: false},
		{name: "patch CSRs", attrs: csr("patch"), wantAllow: false},
		{name: "delete CSRs", attrs: csr("delete"), wantAllow: false},
		{name: "deletecollection CSRs", attrs: csr("deletecollection"), wantAllow: false},
		{name: "update CSR approval", attrs: csrSubresource("update", "approval"), wantAllow: false},
		{name: "update CSR status", attrs: csrSubresource("update", "status"), wantAllow: false},
		{
			name: "approve on the kube-apiserver-client signer",
			attrs: authv1.ResourceAttributes{
				Verb:     "approve",
				Group:    csrGroup,
				Resource: "signers",
				Name:     certv1.KubeAPIServerClientSignerName,
			},
			wantAllow: false,
		},
		{
			name: "get a Secret in kube-system",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Resource:  "secrets",
				Namespace: metav1.NamespaceSystem,
			},
			wantAllow: false,
		},
		{
			name: "get a Secret in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "get",
				Resource:  "secrets",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
		{
			name: "create BootstrapKubeconfigs in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "create",
				Group:     byohGroup,
				Resource:  "bootstrapkubeconfigs",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
		{
			name: "list ByoHosts in tenant-a",
			attrs: authv1.ResourceAttributes{
				Verb:      "list",
				Group:     byohGroup,
				Resource:  "byohosts",
				Namespace: rbacharness.TenantA,
			},
			wantAllow: false,
		},
	}

	for _, source := range rbacharness.Sources() {
		h := harnesses[source]
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("%s/%s", source, tc.name), func(t *testing.T) {
				got, err := h.Allowed(t.Context(), rbacharness.BootstrapSubject(), &tc.attrs)
				require.NoError(t, err)
				assert.Equal(t, tc.wantAllow, got)
			})
		}
	}
}
