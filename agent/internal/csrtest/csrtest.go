// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package csrtest looks up certificate signing requests by host label for
// use in agent and registration package tests, where each request carries a
// random name suffix and so can only be found through its label.
package csrtest

import (
	"context"
	"fmt"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	certv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// List returns every certificate signing request labeled for hostName.
func List(ctx context.Context, clientset kubernetes.Interface, hostName string) ([]certv1.CertificateSigningRequest, error) {
	list, err := clientset.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", infrastructurev1beta1.HostCSRLabel, hostName),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Get returns the single certificate signing request labeled for hostName,
// and errors when there is any other number of them.
func Get(ctx context.Context, clientset kubernetes.Interface, hostName string) (*certv1.CertificateSigningRequest, error) {
	items, err := List(ctx, clientset, hostName)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("expected exactly one certificate signing request for host %s, got %d", hostName, len(items))
	}
	return &items[0], nil
}
