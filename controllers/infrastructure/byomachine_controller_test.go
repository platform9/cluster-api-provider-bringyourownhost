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

	testCases := []struct {
		name string
		host *infrav1.ByoHost
		// nodes holds the Nodes that exist in the workload cluster.
		nodes []*corev1.Node
		// nodeToCheck is the Node whose ProviderID is asserted after the call.
		nodeToCheck string
		// wantProviderID is an exact match; wantProviderIDRegex is used when
		// the id has a random suffix.
		wantProviderID      string
		wantProviderIDRegex string
		wantErr             string
		wantNotFound        bool
	}{
		{
			name:                "node found by reported hostname when it differs from the host name",
			host:                newHost(hostName, nodeName),
			nodes:               []*corev1.Node{newNode(nodeName, "")},
			nodeToCheck:         nodeName,
			wantProviderIDRegex: hostIDRegex,
		},
		{
			name:                "empty reported hostname falls back to the host name",
			host:                newHost(hostName, ""),
			nodes:               []*corev1.Node{newNode(hostName, "")},
			nodeToCheck:         hostName,
			wantProviderIDRegex: hostIDRegex,
		},
		{
			name:         "node missing returns not found",
			host:         newHost(hostName, nodeName),
			nodes:        nil,
			wantNotFound: true,
		},
		{
			name:         "node named after the host is not used when a hostname is reported",
			host:         newHost(hostName, nodeName),
			nodes:        []*corev1.Node{newNode(hostName, "")},
			wantNotFound: true,
		},
		{
			name:           "node bound to another host returns an error and is not modified",
			host:           newHost(hostName, nodeName),
			nodes:          []*corev1.Node{newNode(nodeName, ProviderIDPrefix+"another-host/abcde")},
			nodeToCheck:    nodeName,
			wantProviderID: ProviderIDPrefix + "another-host/abcde",
			wantErr:        "invalid format for node.Spec.ProviderID",
		},
		{
			name:           "node already bound to this host returns the existing provider id",
			host:           newHost(hostName, nodeName),
			nodes:          []*corev1.Node{newNode(nodeName, ProviderIDPrefix+hostName+"/abcde")},
			nodeToCheck:    nodeName,
			wantProviderID: ProviderIDPrefix + hostName + "/abcde",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme)
			for _, node := range tc.nodes {
				builder = builder.WithObjects(node)
			}
			remoteClient := builder.Build()
			reconciler := &ByoMachineReconciler{}

			got, err := reconciler.setNodeProviderID(context.Background(), remoteClient, tc.host)

			switch {
			case tc.wantNotFound:
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err), "expected not found, got %v", err)
				return
			case tc.wantErr != "":
				require.EqualError(t, err, tc.wantErr)
				assert.Empty(t, got)
			default:
				require.NoError(t, err)
			}

			stored := &corev1.Node{}
			err = remoteClient.Get(context.Background(), client.ObjectKey{Name: tc.nodeToCheck}, stored)
			require.NoError(t, err)

			if tc.wantProviderID != "" {
				assert.Equal(t, tc.wantProviderID, stored.Spec.ProviderID)
				if tc.wantErr == "" {
					assert.Equal(t, tc.wantProviderID, got)
				}
				return
			}
			assert.Regexp(t, tc.wantProviderIDRegex, stored.Spec.ProviderID)
			assert.Equal(t, stored.Spec.ProviderID, got)
		})
	}
}
