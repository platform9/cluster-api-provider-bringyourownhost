// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package rbacharness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"text/template"

	"sigs.k8s.io/yaml"
)

// Source names a copy of the shipped RBAC objects.
type Source string

const (
	// SourceKustomize is the RBAC listed by config/rbac/kustomization.yaml.
	SourceKustomize Source = "kustomize"
	// SourceHelm is the RBAC documents of charts/templates/workload.yaml.
	SourceHelm Source = "helm"
)

// Sources lists every copy of the RBAC the harness can load. Run each row
// against all of them: a row that passes on one and fails on another shows
// drift between the copies.
func Sources() []Source {
	return []Source{SourceKustomize, SourceHelm}
}

// ManagerNamespace is the namespace of the namespaced RBAC objects. It also
// fills .Release.Namespace when the chart is rendered.
const ManagerNamespace = "byoh-system"

var (
	rbacKinds    = map[string]bool{"Role": true, "ClusterRole": true, "RoleBinding": true, "ClusterRoleBinding": true}
	docSeparator = regexp.MustCompile(`(?m)^---[ \t]*$`)
	kindLine     = regexp.MustCompile(`(?m)^kind:[ \t]*(\S+)`)
)

// repoRoot returns the repository root, found from the location of this file.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("rbacharness: cannot locate the source file of the package")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// loadDocs returns the YAML documents of source that hold RBAC kinds.
func loadDocs(source Source) ([][]byte, error) {
	switch source {
	case SourceKustomize:
		return loadKustomizeDocs(filepath.Join(repoRoot(), "config", "rbac"))
	case SourceHelm:
		return loadHelmDocs(filepath.Join(repoRoot(), "charts", "templates", "workload.yaml"))
	default:
		return nil, fmt.Errorf("unknown RBAC source %q", source)
	}
}

// loadKustomizeDocs reads the resources named by dir/kustomization.yaml.
// config/rbac sets no prefix, patch or namespace, so the files are the
// objects that kustomize build emits for this directory.
func loadKustomizeDocs(dir string) ([][]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read kustomization in %q: %w", dir, err)
	}

	var kustomization struct {
		Resources []string `json:"resources"`
	}
	err = yaml.Unmarshal(raw, &kustomization)
	if err != nil {
		return nil, fmt.Errorf("parse kustomization in %q: %w", dir, err)
	}

	var docs [][]byte
	for _, resource := range kustomization.Resources {
		content, err := os.ReadFile(filepath.Join(dir, resource))
		if err != nil {
			return nil, fmt.Errorf("read resource %q: %w", resource, err)
		}
		docs = append(docs, filterRBAC(splitDocs(content))...)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no RBAC documents in %q", dir)
	}
	return docs, nil
}

// loadHelmDocs renders the RBAC documents of a chart template. They only use
// .Release.Namespace, so text/template renders them without the helm binary.
// Documents of other kinds are skipped before rendering. Any other value or
// function in an RBAC document fails the render instead of producing an empty
// string.
func loadHelmDocs(path string) ([][]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read chart template %q: %w", path, err)
	}

	data := map[string]any{
		"Release": map[string]any{"Namespace": ManagerNamespace},
	}

	var docs [][]byte
	for i, doc := range filterRBAC(splitDocs(content)) {
		tmpl, err := template.New(fmt.Sprintf("doc-%d", i)).Option("missingkey=error").Parse(string(doc))
		if err != nil {
			return nil, fmt.Errorf("parse RBAC document %d of %q: %w", i, path, err)
		}
		var rendered bytes.Buffer
		err = tmpl.Execute(&rendered, data)
		if err != nil {
			return nil, fmt.Errorf("render RBAC document %d of %q: %w", i, path, err)
		}
		docs = append(docs, rendered.Bytes())
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no RBAC documents in %q", path)
	}
	return docs, nil
}

func splitDocs(content []byte) [][]byte {
	var docs [][]byte
	for _, doc := range docSeparator.Split(string(content), -1) {
		if len(bytes.TrimSpace([]byte(doc))) == 0 {
			continue
		}
		docs = append(docs, []byte(doc))
	}
	return docs
}

func filterRBAC(docs [][]byte) [][]byte {
	var rbac [][]byte
	for _, doc := range docs {
		match := kindLine.FindSubmatch(doc)
		if len(match) == 2 && rbacKinds[string(match[1])] {
			rbac = append(rbac, doc)
		}
	}
	return rbac
}
