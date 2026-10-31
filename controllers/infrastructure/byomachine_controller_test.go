// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

func TestByoMachineReconciler_setNodeProviderID(t *testing.T) {
	const (
		// The fake client looks Nodes up by the namespace in the key, but a
		// Node is cluster-scoped and stored with none, so the host must have
		// no namespace for the lookup to succeed here.
		namespace   = ""
		hostName    = "byohost-ewjv98-qp4fx"
		nodeName    = "byohost-ewjv98"
		hostIDRegex = ProviderIDPrefix + hostName + "/.+"
	)

	newHost := func(name, reportedHostName string) *infrav1.ByoHost {
		return &infrav1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Status: infrav1.ByoHostStatus{
				HostDetails: infrav1.HostInfo{HostName: reportedHostName},
			},
		}
	}
	newNode := func(name, providerID string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       corev1.NodeSpec{ProviderID: providerID},
		}
	}

	// newRemoteClient returns a workload cluster client holding the given Nodes.
	newRemoteClient := func(nodes ...*corev1.Node) client.Client {
		builder := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme)
		for _, node := range nodes {
			builder = builder.WithObjects(node)
		}
		return builder.Build()
	}
	// storedProviderID returns the ProviderID of the named Node as stored in
	// the workload cluster.
	storedProviderID := func(t *testing.T, remoteClient client.Client, name string) string {
		t.Helper()
		stored := &corev1.Node{}
		err := remoteClient.Get(context.Background(), client.ObjectKey{Name: name}, stored)
		require.NoError(t, err)
		return stored.Spec.ProviderID
	}

	t.Run("sets a new provider id on the Node", func(t *testing.T) {
		testCases := []struct {
			name     string
			host     *infrav1.ByoHost
			node     *corev1.Node
			wantNode string
		}{
			{
				name:     "node found by reported hostname when it differs from the host name",
				host:     newHost(hostName, nodeName),
				node:     newNode(nodeName, ""),
				wantNode: nodeName,
			},
			{
				name:     "empty reported hostname falls back to the host name",
				host:     newHost(hostName, ""),
				node:     newNode(hostName, ""),
				wantNode: hostName,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				remoteClient := newRemoteClient(tc.node)
				reconciler := &ByoMachineReconciler{}

				got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, tc.host)

				require.NoError(t, err)
				stored := storedProviderID(t, remoteClient, tc.wantNode)
				assert.Regexp(t, hostIDRegex, stored)
				assert.Equal(t, stored, got)
			})
		}
	})

	t.Run("Node already has a provider id", func(t *testing.T) {
		t.Run("bound to this host returns the existing provider id", func(t *testing.T) {
			existingID := ProviderIDPrefix + hostName + "/abcde"
			remoteClient := newRemoteClient(newNode(nodeName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName, nodeName))

			require.NoError(t, err)
			assert.Equal(t, existingID, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, nodeName))
		})

		t.Run("bound to another host returns an error and is not modified", func(t *testing.T) {
			existingID := ProviderIDPrefix + "another-host/abcde"
			remoteClient := newRemoteClient(newNode(nodeName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName, nodeName))

			require.EqualError(t, err, "invalid format for node.Spec.ProviderID")
			assert.Empty(t, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, nodeName))
		})
	})

	t.Run("Node is not found", func(t *testing.T) {
		testCases := []struct {
			name  string
			host  *infrav1.ByoHost
			nodes []*corev1.Node
		}{
			{
				name:  "node missing in the workload cluster",
				host:  newHost(hostName, nodeName),
				nodes: nil,
			},
			{
				name:  "node named after the host when a hostname is reported",
				host:  newHost(hostName, nodeName),
				nodes: []*corev1.Node{newNode(hostName, "")},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				remoteClient := newRemoteClient(tc.nodes...)
				reconciler := &ByoMachineReconciler{}

				got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, tc.host)

				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
				assert.Empty(t, got)
			})
		}
	})
}
