// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/hostname"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/framework"
	conditions "sigs.k8s.io/cluster-api/util/conditions/deprecated/v1beta1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// byoHostSuffixLength is the length of the random suffix the agent appends to the host
// name to form the ByoHost name.
const byoHostSuffixLength = 5

// findByoHost returns the ByoHost in namespace that belongs to the host whose
// (unnormalized) name is containerHostName. A ByoHost matches when its name is
// <normalized host name>-<5 characters>, or when its spec.identity parses to the
// normalized host name. It returns an error if no ByoHost or several ByoHosts match.
func findByoHost(ctx context.Context, c ctrlclient.Client, namespace, containerHostName string) (*infrastructurev1beta1.ByoHost, error) {
	normalized, err := hostname.Normalize(containerHostName)
	if err != nil {
		return nil, fmt.Errorf("normalize host name %q: %w", containerHostName, err)
	}

	byoHosts := &infrastructurev1beta1.ByoHostList{}
	if err := c.List(ctx, byoHosts, ctrlclient.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list ByoHosts in namespace %s: %w", namespace, err)
	}

	var matches []*infrastructurev1beta1.ByoHost
	for i := range byoHosts.Items {
		byoHost := &byoHosts.Items[i]
		nameMatches := strings.HasPrefix(byoHost.Name, normalized+"-") &&
			len(byoHost.Name) == len(normalized)+1+byoHostSuffixLength

		_, identityHostName, _, parseErr := infrastructurev1beta1.ParseHostIdentity(byoHost.Spec.Identity)
		identityMatches := parseErr == nil && identityHostName == normalized

		if nameMatches || identityMatches {
			matches = append(matches, byoHost)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no ByoHost in namespace %s belongs to host %q (expected name %s-<%d chars>)",
			namespace, containerHostName, normalized, byoHostSuffixLength)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Name)
		}
		return nil, fmt.Errorf("%d ByoHosts in namespace %s belong to host %q: %s",
			len(matches), namespace, containerHostName, strings.Join(names, ", "))
	}
}

// waitForByoHost polls until exactly one ByoHost belongs to containerHostName (see
// findByoHost) and returns it. The agent creates the ByoHost during registration, so
// it does not exist right after the container starts.
func waitForByoHost(ctx context.Context, clusterProxy framework.ClusterProxy, namespace, containerHostName, specName string) *infrastructurev1beta1.ByoHost {
	var byoHost *infrastructurev1beta1.ByoHost
	var lastErr error

	Eventually(func() bool {
		byoHost, lastErr = findByoHost(ctx, clusterProxy.GetClient(), namespace, containerHostName)
		return lastErr == nil
	}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
		"expected exactly one ByoHost for host %s in namespace %s, last lookup error: %v", containerHostName, namespace, lastErr)

	return byoHost
}

// AssertByoHostConditionsTrue polls the ByoHost of the host named containerHostName until
// each of conditionTypes reports True. Unlike waiting for the workload cluster to come up,
// this exercises BYOH's own condition-reporting path end to end (agent heartbeat ->
// AgentConnected, agent bootstrap status -> K8sNodeBootstrapSucceeded/K8sComponentsInstallationSucceeded),
// which a passing ApplyClusterTemplateAndWait does not by itself confirm.
func AssertByoHostConditionsTrue(ctx context.Context, clusterProxy framework.ClusterProxy, namespace, containerHostName, specName string, conditionTypes ...clusterv1.ConditionType) {
	for _, conditionType := range conditionTypes {
		conditionType := conditionType
		Eventually(func() bool {
			byoHost, err := findByoHost(ctx, clusterProxy.GetClient(), namespace, containerHostName)
			if err != nil {
				return false
			}
			return conditions.IsTrue(byoHost, conditionType)
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected the ByoHost of host %s in namespace %s to report condition %s=True", containerHostName, namespace, conditionType)
	}
}
