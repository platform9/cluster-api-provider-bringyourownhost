// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// This file characterizes the apimachinery entry points that
// byomachine_controller.go relies on to read the installer config's
// installationSecret/uninstallationSecret out of an unstructured object:
// unstructured.NestedFieldNoCopy and runtime.DefaultUnstructuredConverter.
// These are plain `go test` functions rather than Ginkgo specs, so on an
// apimachinery bump `go test -run <name>` pinpoints a break in the raw
// conversion/lookup behavior separately from a break in the reconciler
// business logic exercised by the Ginkgo suite in this package. TestMain
// in suite_test.go still starts envtest for the whole package (Go builds
// one test binary per directory), so this doesn't avoid that setup cost.
package controllers_test

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// installerConfigWithSecret builds an unstructured object shaped like a
// K8sInstallerConfig whose status carries an installationSecret ObjectReference,
// mirroring what the real CRD status looks like on the wire.
func installerConfigWithSecret(secret map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
			"kind":       "K8sInstallerConfig",
			"status":     map[string]interface{}{},
		},
	}
	if secret != nil {
		obj.Object["status"].(map[string]interface{})["installationSecret"] = secret
	}
	return obj
}

func TestNestedFieldNoCopy_InstallationSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	secret := map[string]interface{}{
		"kind":      "Secret",
		"namespace": "default",
		"name":      "k8s-installation-secret",
	}
	obj := installerConfigWithSecret(secret)

	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())
	g.Expect(value).To(gomega.Equal(secret))
}

func TestNestedFieldNoCopy_MissingInstallationSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	obj := installerConfigWithSecret(nil)

	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeFalse())
	g.Expect(value).To(gomega.BeNil())
}

func TestNestedFieldNoCopy_NonMapIntermediate(t *testing.T) {
	g := gomega.NewWithT(t)

	// "status" is a string instead of a map — matches what the controller would
	// see if a CRD ever serialized status unexpectedly (or a conversion webhook misbehaved).
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": "not-a-map",
		},
	}

	_, _, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "installationSecret")
	g.Expect(err).To(gomega.HaveOccurred())
}

func TestFromUnstructured_ObjectReferenceRoundTrip(t *testing.T) {
	g := gomega.NewWithT(t)

	secret := map[string]interface{}{
		"kind":       "Secret",
		"namespace":  "default",
		"name":       "k8s-installation-secret",
		"apiVersion": "v1",
	}

	secretRef := &corev1.ObjectReference{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(secret, secretRef)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secretRef.Kind).To(gomega.Equal("Secret"))
	g.Expect(secretRef.Namespace).To(gomega.Equal("default"))
	g.Expect(secretRef.Name).To(gomega.Equal("k8s-installation-secret"))
	g.Expect(secretRef.APIVersion).To(gomega.Equal("v1"))
}

func TestFromUnstructured_WrongFieldType(t *testing.T) {
	g := gomega.NewWithT(t)

	// "name" must be a string on ObjectReference; a numeric value should fail
	// the conversion rather than silently coerce, exactly as the controller expects.
	secret := map[string]interface{}{
		"kind": "Secret",
		"name": 12345,
	}

	secretRef := &corev1.ObjectReference{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(secret, secretRef)
	g.Expect(err).To(gomega.HaveOccurred())
}

func TestInstallerConfigGVK_TemplateSuffixStripped(t *testing.T) {
	g := gomega.NewWithT(t)

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
	gvk.Kind = "K8sInstallerConfig"
	installerConfig.SetGroupVersionKind(gvk)

	g.Expect(installerConfig.GroupVersionKind()).To(gomega.Equal(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1beta1",
		Kind:    "K8sInstallerConfig",
	}))
}
