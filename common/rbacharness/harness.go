// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package rbacharness answers whether the shipped RBAC lets an identity do a
// verb. It starts an envtest API server that runs the RBAC authorizer, applies
// the RBAC objects of the repository and asks the server with a
// SubjectAccessReview. A fake client does no authorization, so it cannot show
// a denial.
package rbacharness

import (
	"context"
	"errors"
	"fmt"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// TenantA and TenantB are the tenant namespaces every harness creates.
const (
	TenantA = "tenant-a"
	TenantB = "tenant-b"
)

// Subject is the identity a SubjectAccessReview is asked about. The API server
// adds no groups of its own to a review, so list system:authenticated when the
// identity holds it.
type Subject struct {
	User   string
	Groups []string
}

// BootstrapSubject is the identity of a bootstrap token for the token abcdef.
func BootstrapSubject() Subject {
	return Subject{
		User:   "system:bootstrap:abcdef",
		Groups: []string{"system:bootstrappers", "system:bootstrappers:byoh", "system:authenticated"},
	}
}

// HostSubject is the identity of the certificate of host h1-abc12 in tenant-a.
func HostSubject() Subject {
	return Subject{
		User:   "byoh:host:tenant-a:h1-abc12",
		Groups: []string{"byoh:hosts", "system:authenticated"},
	}
}

// Harness is a running API server that holds the RBAC of one source.
type Harness struct {
	env     *envtest.Environment
	clients kubernetes.Interface
}

// Start runs an API server with the RBAC authorizer, creates the tenant
// namespaces and applies the RBAC objects of source. Call Stop when done.
func Start(ctx context.Context, source Source) (*Harness, error) {
	docs, err := loadDocs(source)
	if err != nil {
		return nil, fmt.Errorf("load RBAC from %s: %w", source, err)
	}

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start envtest: %w", err), env.Stop())
	}
	h := &Harness{env: env}

	h.clients, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create client: %w", err), h.Stop())
	}

	for _, name := range []string{ManagerNamespace, TenantA, TenantB} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		_, err = h.clients.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create namespace %q: %w", name, err), h.Stop())
		}
	}

	for _, doc := range docs {
		err = h.apply(ctx, doc)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("apply RBAC from %s: %w", source, err), h.Stop())
		}
	}
	return h, nil
}

// Stop shuts the API server down.
func (h *Harness) Stop() error {
	return h.env.Stop()
}

// Allowed asks the API server whether subject may do what attrs describes.
func (h *Harness) Allowed(ctx context.Context, subject Subject, attrs *authv1.ResourceAttributes) (bool, error) {
	review := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:               subject.User,
			Groups:             subject.Groups,
			ResourceAttributes: attrs,
		},
	}
	result, err := h.clients.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("create SubjectAccessReview for %q: %w", subject.User, err)
	}
	return result.Status.Allowed, nil
}

// apply creates the Role, ClusterRole, RoleBinding or ClusterRoleBinding in doc.
// A field that the kind does not define is an error.
// A namespaced object without a namespace goes to ManagerNamespace.
func (h *Harness) apply(ctx context.Context, doc []byte) error {
	var meta metav1.TypeMeta
	err := yaml.Unmarshal(doc, &meta)
	if err != nil {
		return fmt.Errorf("read kind: %w", err)
	}

	if !rbacKinds[meta.Kind] {
		return fmt.Errorf("kind %q is not an RBAC kind", meta.Kind)
	}

	rbac := h.clients.RbacV1()
	switch meta.Kind {
	case "ClusterRole":
		var obj rbacv1.ClusterRole
		err = yaml.UnmarshalStrict(doc, &obj)
		if err == nil {
			_, err = rbac.ClusterRoles().Create(ctx, &obj, metav1.CreateOptions{})
		}
	case "ClusterRoleBinding":
		var obj rbacv1.ClusterRoleBinding
		err = yaml.UnmarshalStrict(doc, &obj)
		if err == nil {
			_, err = rbac.ClusterRoleBindings().Create(ctx, &obj, metav1.CreateOptions{})
		}
	case "Role":
		var obj rbacv1.Role
		err = yaml.UnmarshalStrict(doc, &obj)
		if err == nil {
			_, err = rbac.Roles(namespaceOrDefault(obj.Namespace)).Create(ctx, &obj, metav1.CreateOptions{})
		}
	case "RoleBinding":
		var obj rbacv1.RoleBinding
		err = yaml.UnmarshalStrict(doc, &obj)
		if err == nil {
			_, err = rbac.RoleBindings(namespaceOrDefault(obj.Namespace)).Create(ctx, &obj, metav1.CreateOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", meta.Kind, err)
	}
	return nil
}

func namespaceOrDefault(namespace string) string {
	if namespace == "" {
		return ManagerNamespace
	}
	return namespace
}
