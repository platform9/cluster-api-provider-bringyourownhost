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
		namespace = ""
		hostName  = "byohost-ewjv98-qp4fx"
		// otherNodeName is the normalized hostname without the ByoHost suffix.
		otherNodeName = "byohost-ewjv98"
		hostIDRegex   = ProviderIDPrefix + hostName + "/.+"
	)

	newHost := func(name string) *infrav1.ByoHost {
		return &infrav1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
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

	t.Run("sets a new provider id on the Node named after the host", func(t *testing.T) {
		remoteClient := newRemoteClient(newNode(hostName, ""))
		reconciler := &ByoMachineReconciler{}

		got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

		require.NoError(t, err)
		stored := storedProviderID(t, remoteClient, hostName)
		assert.Regexp(t, hostIDRegex, stored)
		assert.Equal(t, stored, got)
	})

	t.Run("Node already has a provider id", func(t *testing.T) {
		t.Run("bound to this host returns the existing provider id", func(t *testing.T) {
			existingID := ProviderIDPrefix + hostName + "/abcde"
			remoteClient := newRemoteClient(newNode(hostName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

			require.NoError(t, err)
			assert.Equal(t, existingID, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, hostName))
		})

		t.Run("bound to another host returns an error and is not modified", func(t *testing.T) {
			existingID := ProviderIDPrefix + "another-host/abcde"
			remoteClient := newRemoteClient(newNode(hostName, existingID))
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

			require.EqualError(t, err, "invalid format for node.Spec.ProviderID")
			assert.Empty(t, got)
			assert.Equal(t, existingID, storedProviderID(t, remoteClient, hostName))
		})
	})

	t.Run("Node is not found", func(t *testing.T) {
		testCases := []struct {
			name  string
			nodes []*corev1.Node
		}{
			{
				name:  "node missing in the workload cluster",
				nodes: nil,
			},
			{
				name:  "only a node named after the hostname without the suffix exists",
				nodes: []*corev1.Node{newNode(otherNodeName, "")},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				remoteClient := newRemoteClient(tc.nodes...)
				reconciler := &ByoMachineReconciler{}

				got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(hostName))

				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
				assert.Empty(t, got)
			})
		}
	})

	t.Run("two ByoHosts reporting the same hostname never claim the same Node", func(t *testing.T) {
		const (
			firstHostName  = "worker-1-aaaaa"
			secondHostName = "worker-1-bbbbb"
		)
		remoteClient := newRemoteClient(newNode(firstHostName, ""), newNode(secondHostName, ""))
		reconciler := &ByoMachineReconciler{}

		firstID, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(firstHostName))
		require.NoError(t, err)
		secondID, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(secondHostName))
		require.NoError(t, err)

		assert.Regexp(t, ProviderIDPrefix+firstHostName+"/.+", firstID)
		assert.Regexp(t, ProviderIDPrefix+secondHostName+"/.+", secondID)
		assert.Equal(t, firstID, storedProviderID(t, remoteClient, firstHostName))
		assert.Equal(t, secondID, storedProviderID(t, remoteClient, secondHostName))
	})

	t.Run("stale Node of an old ByoHost does not block a re-onboarded host", func(t *testing.T) {
		const (
			oldHostName = "worker-1-old01"
			newHostName = "worker-1-new02"
		)
		staleID := ProviderIDPrefix + oldHostName + "/abcde"
		remoteClient := newRemoteClient(newNode(oldHostName, staleID))
		reconciler := &ByoMachineReconciler{}

		// The new host registered no Node yet, so it must not take the stale one.
		got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(newHostName))
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
		assert.Empty(t, got)
		assert.Equal(t, staleID, storedProviderID(t, remoteClient, oldHostName))

		// Once the new host registers its own Node, it gets its own provider id.
		err = remoteClient.Create(context.Background(), newNode(newHostName, ""))
		require.NoError(t, err)

		got, err = reconciler.setNodeProviderID(context.Background(), remoteClient, newHost(newHostName))
		require.NoError(t, err)
		assert.Regexp(t, ProviderIDPrefix+newHostName+"/.+", got)
		assert.Equal(t, got, storedProviderID(t, remoteClient, newHostName))
		assert.Equal(t, staleID, storedProviderID(t, remoteClient, oldHostName))
	})
}
