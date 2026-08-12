// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	testK8sInstallerConfigAPIVersion = "infrastructure.cluster.x-k8s.io/v1beta1"
	testK8sInstallerConfigKind       = "K8sInstallerConfig"
	testSecretKind                   = "Secret"
	testSecretNameField              = "name"
	testKindField                    = "kind"
	testSecretName                   = "k8s-installation-secret"
	testObjectReferenceAPIVersion    = "v1"
)

func installerConfigWithSecret(secret map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion":  testK8sInstallerConfigAPIVersion,
			testKindField: testK8sInstallerConfigKind,
			"status":      map[string]interface{}{},
		},
	}
	if secret != nil {
		obj.Object["status"].(map[string]interface{})["installationSecret"] = secret
	}
	return obj
}

func TestNestedFieldNoCopy_InstallationSecret(t *testing.T) {
	secret := map[string]interface{}{
		testKindField:       testSecretKind,
		"namespace":         defaultNamespace,
		testSecretNameField: testSecretName,
	}
	obj := installerConfigWithSecret(secret)

	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, secret, value)
}

func TestNestedFieldNoCopy_MissingInstallationSecret(t *testing.T) {
	obj := installerConfigWithSecret(nil)

	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, value)
}

func TestNestedFieldNoCopy_NonMapIntermediate(t *testing.T) {
	// "status" is a string instead of a map — matches what the controller would
	// see if a CRD ever serialized status unexpectedly (or a conversion webhook misbehaved).
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": "not-a-map",
		},
	}

	_, _, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	assert.Error(t, err)
}

func TestFromUnstructured_ObjectReferenceRoundTrip(t *testing.T) {
	secret := map[string]interface{}{
		testKindField:       testSecretKind,
		"namespace":         defaultNamespace,
		testSecretNameField: testSecretName,
		"apiVersion":        testObjectReferenceAPIVersion,
	}

	secretRef := &corev1.ObjectReference{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(secret, secretRef)
	require.NoError(t, err)
	assert.Equal(t, testSecretKind, secretRef.Kind)
	assert.Equal(t, defaultNamespace, secretRef.Namespace)
	assert.Equal(t, testSecretName, secretRef.Name)
	assert.Equal(t, testObjectReferenceAPIVersion, secretRef.APIVersion)
}

func TestFromUnstructured_WrongFieldType(t *testing.T) {
	// "name" must be a string on ObjectReference; a numeric value should fail
	// the conversion rather than silently coerce, exactly as the controller expects.
	secret := map[string]interface{}{
		testKindField:       testSecretKind,
		testSecretNameField: 12345,
	}

	secretRef := &corev1.ObjectReference{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(secret, secretRef)
	assert.Error(t, err)
}

func TestInstallerConfigGVK_TemplateSuffixStripped(t *testing.T) {
	// getInstallerConfig() derives the InstallerConfig's GVK from the
	// InstallerRef's GVK by stripping the "Template" suffix from the Kind
	// (e.g. K8sInstallerConfigTemplate -> K8sInstallerConfig). Characterize
	// the schema.GroupVersionKind set/get round trip that logic relies on.
	templateGVK := schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "K8sInstallerConfigTemplate",
	}

	installerConfig := &unstructured.Unstructured{}
	gvk := templateGVK
	gvk.Kind = testK8sInstallerConfigKind
	installerConfig.SetGroupVersionKind(gvk)

	assert.Equal(t, schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    testK8sInstallerConfigKind,
	}, installerConfig.GroupVersionKind())
}
