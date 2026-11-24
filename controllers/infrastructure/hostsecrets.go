// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

const (
	verbGet         = "get"
	secretsResource = "secrets"
)

// isSecretsRule reports whether rule applies to core Secrets and nothing else.
func isSecretsRule(rule *rbacv1.PolicyRule) bool {
	return slices.Equal(rule.APIGroups, []string{""}) && slices.Equal(rule.Resources, []string{secretsResource})
}

// HostSecretRules returns rules with the rule on core Secrets replaced by one
// that lets get read the Secrets named in names. Rules on other resources are
// kept. With no names the result has no rule on Secrets, because a rule with an
// empty resourceNames list matches every Secret.
func HostSecretRules(rules []rbacv1.PolicyRule, names []string) []rbacv1.PolicyRule {
	result := make([]rbacv1.PolicyRule, 0, len(rules)+1)
	for i := range rules {
		if !isSecretsRule(&rules[i]) {
			result = append(result, rules[i])
		}
	}

	sorted := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == "" })
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if len(sorted) == 0 {
		return result
	}

	return append(result, rbacv1.PolicyRule{
		APIGroups:     []string{""},
		Resources:     []string{secretsResource},
		ResourceNames: sorted,
		Verbs:         []string{verbGet},
	})
}

// setHostSecretAccess sets the Secrets that the host-scoped Role of byoHost
// lets it read to names. It only updates the Role the approver created. A host
// without one, or whose Role lacks the tenant label of its namespace, is left
// alone. The Role is written only when its rules change.
func setHostSecretAccess(ctx context.Context, reader client.Reader, writer client.Client, byoHost *infrastructurev1beta1.ByoHost, names []string) error {
	logger := log.FromContext(ctx).WithValues("byohost", byoHost.Name)
	key := client.ObjectKey{Namespace: byoHost.Namespace, Name: hostRBACName(byoHost.Name)}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		role := &rbacv1.Role{}
		err := reader.Get(ctx, key, role)
		if apierrors.IsNotFound(err) {
			logger.Info("ByoHost has no host-scoped Role, Secret access is not set")
			return nil
		}
		if err != nil {
			return fmt.Errorf("get Role %q: %w", key.Name, err)
		}

		if role.Labels[identity.TenantLabel] != role.Namespace {
			logger.Info("Role is not host-scoped, Secret access is not set", "role", role.Name)
			return nil
		}

		rules := HostSecretRules(role.Rules, names)
		if equality.Semantic.DeepEqual(role.Rules, rules) {
			return nil
		}

		role.Rules = rules
		return writer.Update(ctx, role)
	})
}
