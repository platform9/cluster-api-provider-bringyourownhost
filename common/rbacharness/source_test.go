// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadHelmDocs(t *testing.T) {
	const chart = `---
apiVersion: v1
kind: Service
metadata:
  name: svc
spec:
  ports: [{{ .Values.notRendered | toYaml }}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: b
subjects:
  - kind: ServiceAccount
    name: sa
    namespace: '{{ .Release.Namespace }}'
`
	path := filepath.Join(t.TempDir(), "workload.yaml")
	err := os.WriteFile(path, []byte(chart), 0o600)
	require.NoError(t, err)

	docs, err := loadHelmDocs(path)
	require.NoError(t, err)

	require.Len(t, docs, 1)
	assert.Contains(t, string(docs[0]), "namespace: 'byoh-system'")
	assert.NotContains(t, string(docs[0]), "kind: Service\n")
}

func TestLoadHelmDocs_UnknownFunctionInRBACDocument(t *testing.T) {
	const chart = `kind: ClusterRole
metadata:
  name: '{{ include "name" . }}'
`
	path := filepath.Join(t.TempDir(), "workload.yaml")
	err := os.WriteFile(path, []byte(chart), 0o600)
	require.NoError(t, err)

	_, err = loadHelmDocs(path)
	require.Error(t, err)
}

func TestLoadKustomizeDocs(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"kustomization.yaml": "resources:\n- sa.yaml\n- role.yaml\n",
		"sa.yaml":            "kind: ServiceAccount\nmetadata:\n  name: sa\n",
		"role.yaml":          "kind: ClusterRole\nmetadata:\n  name: r\n",
	}
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
		require.NoError(t, err)
	}

	docs, err := loadKustomizeDocs(dir)
	require.NoError(t, err)

	require.Len(t, docs, 1)
	assert.Contains(t, string(docs[0]), "ClusterRole")
}

func TestLoadDocs_UnknownSource(t *testing.T) {
	_, err := loadDocs(Source("nonexistent"))
	require.Error(t, err)
}

func TestLoadDocs_NoRBACDocuments(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources:\n- sa.yaml\n"), 0o600)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "sa.yaml"), []byte("kind: ServiceAccount\n"), 0o600)
	require.NoError(t, err)
	_, err = loadKustomizeDocs(dir)
	require.Error(t, err)

	chart := filepath.Join(t.TempDir(), "workload.yaml")
	err = os.WriteFile(chart, []byte("kind: Service\n"), 0o600)
	require.NoError(t, err)
	_, err = loadHelmDocs(chart)
	require.Error(t, err)
}

func TestHarness_Apply(t *testing.T) {
	testCases := []struct {
		name string
		doc  string
	}{
		{
			name: "non-RBAC kind",
			doc:  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
		},
		{
			name: "field the kind does not define",
			doc:  "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: r\nrulez: []\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Harness{clients: fake.NewSimpleClientset()}
			err := h.apply(t.Context(), []byte(tc.doc))
			require.Error(t, err)
		})
	}

	t.Run("valid ClusterRole", func(t *testing.T) {
		h := &Harness{clients: fake.NewSimpleClientset()}
		err := h.apply(t.Context(), []byte("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: r\n"))
		require.NoError(t, err)
	})
}
