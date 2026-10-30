// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuralcel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

const bootstrapKubeconfigCRDPath = "../../../config/crd/bases/infrastructure.cluster.x-k8s.io_bootstrapkubeconfigs.yaml"

// newBootstrapKubeconfigSpecValidator builds a CEL validator for the spec
// schema of the generated BootstrapKubeconfig CRD, so the test evaluates the
// rules the API server runs and fails if the generated CRD is stale.
func newBootstrapKubeconfigSpecValidator(t *testing.T) (*structuralcel.Validator, *schema.Structural) {
	t.Helper()

	data, err := os.ReadFile(bootstrapKubeconfigCRDPath)
	require.NoError(t, err)

	crd := &apiextensionsv1.CustomResourceDefinition{}
	err = yaml.Unmarshal(data, crd)
	require.NoError(t, err)

	var specProps *apiextensionsv1.JSONSchemaProps
	for _, version := range crd.Spec.Versions {
		if version.Name != "v1beta1" {
			continue
		}
		require.NotNil(t, version.Schema)
		props, ok := version.Schema.OpenAPIV3Schema.Properties["spec"]
		require.True(t, ok, "v1beta1 schema has no spec property")
		specProps = &props
	}
	require.NotNil(t, specProps, "CRD has no v1beta1 version")

	internalProps := &apiextensionsinternal.JSONSchemaProps{}
	err = apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(specProps, internalProps, nil)
	require.NoError(t, err)

	structural, err := schema.NewStructural(internalProps)
	require.NoError(t, err)

	validator := structuralcel.NewValidator(structural, false, celconfig.PerCallLimit)
	require.NotNil(t, validator, "BootstrapKubeconfig spec schema has no x-kubernetes-validations rules")
	return validator, structural
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

	validator, structural := newBootstrapKubeconfigSpecValidator(t)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errs, _ := validator.Validate(
				t.Context(),
				field.NewPath("spec"),
				structural,
				tc.newSpec,
				tc.oldSpec,
				celconfig.RuntimeCELCostBudget,
			)

			if tc.wantMessage == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), tc.wantMessage)
		})
	}
}
