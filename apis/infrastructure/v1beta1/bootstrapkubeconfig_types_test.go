// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	celtest "k8s.io/apiextensions-apiserver/pkg/test"
)

const bootstrapKubeconfigCRDPath = "../../../config/crd/bases/infrastructure.cluster.x-k8s.io_bootstrapkubeconfigs.yaml"

// newBootstrapKubeconfigSpecValidator returns the CEL validator for the spec schema of the
// generated BootstrapKubeconfig CRD, so the test evaluates the rules the API server runs
// and fails if the generated CRD is stale.
func newBootstrapKubeconfigSpecValidator(t *testing.T) celtest.CELValidateFunc {
	t.Helper()

	crd := celtest.MustLoadManifest[apiextensionsv1.CustomResourceDefinition](t, bootstrapKubeconfigCRDPath)
	validatorsByVersion := celtest.FieldValidators(t, crd)

	validator, ok := validatorsByVersion["v1beta1"]["openAPIV3Schema.properties.spec"]
	require.True(t, ok, "BootstrapKubeconfig v1beta1 spec schema has no x-kubernetes-validations rules")
	return validator
}

func TestBootstrapKubeconfigSpec_Immutable(t *testing.T) {
	t.Parallel()

	const (
		apiServer = "https://abc.com:1234"
		caData    = "Y2E="
	)

	spec := func(hostName string, createdBy *string) map[string]any {
		s := map[string]any{
			"apiserver":                  apiServer,
			"certificate-authority-data": caData,
			"hostName":                   hostName,
		}
		if createdBy != nil {
			s["createdBy"] = *createdBy
		}
		return s
	}
	str := func(s string) *string { return &s }

	testCases := []struct {
		name        string
		oldSpec     map[string]any
		newSpec     map[string]any
		wantMessage string
	}{
		{
			name:    "unchanged host name is allowed",
			oldSpec: spec("example-worker-1", str("admin@example.com")),
			newSpec: spec("example-worker-1", str("admin@example.com")),
		},
		{
			name:        "changed host name is rejected",
			oldSpec:     spec("example-worker-1", str("admin@example.com")),
			newSpec:     spec("example-worker-2", str("admin@example.com")),
			wantMessage: "hostName is immutable",
		},
		{
			name:    "unchanged creator is allowed",
			oldSpec: spec("example-worker-1", str("admin@example.com")),
			newSpec: spec("example-worker-1", str("admin@example.com")),
		},
		{
			name:        "changed creator is rejected",
			oldSpec:     spec("example-worker-1", str("admin@example.com")),
			newSpec:     spec("example-worker-1", str("someone-else@example.com")),
			wantMessage: "createdBy is immutable",
		},
		{
			name:        "creator added when the old object had none is rejected",
			oldSpec:     spec("example-worker-1", nil),
			newSpec:     spec("example-worker-1", str("admin@example.com")),
			wantMessage: "createdBy is immutable",
		},
		{
			name:        "removed creator is rejected",
			oldSpec:     spec("example-worker-1", str("admin@example.com")),
			newSpec:     spec("example-worker-1", nil),
			wantMessage: "createdBy is immutable",
		},
		{
			name:    "no creator before or after is allowed",
			oldSpec: spec("example-worker-1", nil),
			newSpec: spec("example-worker-1", nil),
		},
	}

	validate := newBootstrapKubeconfigSpecValidator(t)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errs := validate(tc.newSpec, tc.oldSpec)

			if tc.wantMessage == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), tc.wantMessage)
		})
	}
}
