// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"context"
	"fmt"
	"io"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// managerPodNamePrefix is the name prefix of the manager pods in every deployment of the suite.
	managerPodNamePrefix = "byoh-controller-manager"
	// managerContainerName is the container of the manager pod that runs the controllers.
	managerContainerName = "manager"
)

// hostClient returns a client that the API server treats as the host with the certificate identity
// commonName in the tenant namespace. The groups are the ones a host certificate carries once
// authenticated, without the shared hosts group, so only what is granted to the tenant group and to
// the user itself applies. Impersonation lets a spec ask the API server what a host may do without
// the host's private key.
func hostClient(adminConfig *rest.Config, commonName, namespace string) (*kubernetes.Clientset, error) {
	config := rest.CopyConfig(adminConfig)
	config.Impersonate = rest.ImpersonationConfig{
		UserName: commonName,
		Groups:   []string{"byoh:hosts:" + namespace, "system:authenticated"},
	}

	return kubernetes.NewForConfig(config)
}

// hostAllowed asks the API server whether the user of host may do what attrs describe.
func hostAllowed(ctx context.Context, host kubernetes.Interface, attrs *authorizationv1.ResourceAttributes) (bool, error) {
	review, err := host.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("create SelfSubjectAccessReview: %w", err)
	}

	return review.Status.Allowed, nil
}

// managerLogsSince returns the log lines the manager wrote since the given time. The manager pod is
// found by name in any namespace, because the namespace differs by deployment. Pods that cannot be read
// are skipped, so an empty result means no line was read and callers must check for that.
func managerLogsSince(ctx context.Context, clientSet kubernetes.Interface, since metav1.Time) (string, error) {
	pods, err := clientSet.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}

	var logs strings.Builder
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !strings.HasPrefix(pod.Name, managerPodNamePrefix) {
			continue
		}

		options := &corev1.PodLogOptions{SinceTime: &since}
		for j := range pod.Spec.Containers {
			if pod.Spec.Containers[j].Name == managerContainerName {
				options.Container = managerContainerName
			}
		}

		stream, streamErr := clientSet.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, options).Stream(ctx)
		if streamErr != nil {
			continue
		}
		content, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr != nil {
			return "", fmt.Errorf("read logs of pod %s/%s: %w", pod.Namespace, pod.Name, readErr)
		}
		logs.Write(content)
	}

	return logs.String(), nil
}

// logLinesContaining returns the lines of logs that contain every one of the given substrings.
func logLinesContaining(logs string, substrings ...string) []string {
	var matching []string
	for _, line := range strings.Split(logs, "\n") {
		found := true
		for _, substring := range substrings {
			found = found && strings.Contains(line, substring)
		}
		if found {
			matching = append(matching, line)
		}
	}

	return matching
}
