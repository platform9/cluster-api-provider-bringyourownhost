// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	registrarHostName    = "example-worker-1"
	registrarByoHostName = "example-worker-1-abc12"
	registrarNamespace   = "default"
	identityOne          = "byoh:host:default:example-worker-1-abc12"
	identityTwo          = "byoh:host:default:example-worker-1-xyz89"
)

func newRegistrarByoHost(specIdentity string) *infrastructurev1beta1.ByoHost {
	return &infrastructurev1beta1.ByoHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registrarByoHostName,
			Namespace: registrarNamespace,
		},
		Spec: infrastructurev1beta1.ByoHostSpec{
			Identity: specIdentity,
		},
	}
}

func newRegistrarClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	err := infrastructurev1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&infrastructurev1beta1.ByoHost{}).
		Build()
}

func TestHostRegistrar_Register(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		agentIdentity string
		existing      *infrastructurev1beta1.ByoHost
		wantConflict  bool
	}{
		{
			name:          "missing ByoHost is created and patched",
			agentIdentity: identityOne,
		},
		{
			name:          "same identity is a restart and is patched",
			agentIdentity: identityOne,
			existing:      newRegistrarByoHost(identityOne),
		},
		{
			name:          "empty spec identity is patched",
			agentIdentity: identityOne,
			existing:      newRegistrarByoHost(""),
		},
		{
			name:          "empty agent identity is patched",
			agentIdentity: "",
			existing:      newRegistrarByoHost(identityTwo),
		},
		{
			name:          "different identity is an error and is not patched",
			agentIdentity: identityOne,
			existing:      newRegistrarByoHost(identityTwo),
			wantConflict:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var objs []client.Object
			if tc.existing != nil {
				objs = append(objs, tc.existing)
			}
			k8sClient := newRegistrarClient(t, objs...)
			key := types.NamespacedName{Name: registrarByoHostName, Namespace: registrarNamespace}
			hr := &HostRegistrar{
				K8sClient:   k8sClient,
				Identity:    tc.agentIdentity,
				ByoHostInfo: HostInfo{HostName: registrarHostName},
			}

			err := hr.Register(t.Context(), registrarByoHostName, registrarNamespace, map[string]string{"example": "label"})

			stored := &infrastructurev1beta1.ByoHost{}
			getErr := k8sClient.Get(t.Context(), key, stored)
			require.NoError(t, getErr)

			if tc.wantConflict {
				require.ErrorIs(t, err, ErrIdentityConflict)
				assert.Contains(t, err.Error(), identityOne)
				assert.Contains(t, err.Error(), identityTwo)
				assert.Contains(t, err.Error(), "decommission")
				assert.Equal(t, tc.existing.Spec, stored.Spec)
				assert.Empty(t, stored.Status.HostDetails.HostName, "a conflicting ByoHost is not patched")
				assert.NotContains(t, stored.Labels, infrastructurev1beta1.HostArchitectureLabel)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, registrarHostName, stored.Status.HostDetails.HostName)
			assert.Contains(t, stored.Labels, infrastructurev1beta1.HostArchitectureLabel)
		})
	}
}

func TestHostRegistrar_Register_CreatesByoHostWithGivenName(t *testing.T) {
	t.Parallel()

	k8sClient := newRegistrarClient(t)
	hr := &HostRegistrar{K8sClient: k8sClient, Identity: identityOne}

	err := hr.Register(t.Context(), registrarByoHostName, registrarNamespace, map[string]string{"example": "label"})
	require.NoError(t, err)

	list := &infrastructurev1beta1.ByoHostList{}
	err = k8sClient.List(t.Context(), list)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, registrarByoHostName, list.Items[0].Name)
	assert.Equal(t, registrarNamespace, list.Items[0].Namespace)
	assert.Equal(t, "label", list.Items[0].Labels["example"])
}
