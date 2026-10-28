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
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

const (
	registrarNormalizedHostName = "example-worker-1"
	registrarNamespace          = "default"
)

var (
	registrarHostIdentityOne = identity.HostIdentity{
		TenantNamespace:    registrarNamespace,
		NormalizedHostName: registrarNormalizedHostName,
		Suffix:             "abc12",
	}
	registrarHostIdentityTwo = identity.HostIdentity{
		TenantNamespace:    registrarNamespace,
		NormalizedHostName: registrarNormalizedHostName,
		Suffix:             "xyz89",
	}
	registrarByoHostName = registrarHostIdentityOne.ByoHostName()
	identityOne          = registrarHostIdentityOne.String()
	identityTwo          = registrarHostIdentityTwo.String()
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
		labels        map[string]string
		wantLabels    map[string]string
		wantConflict  bool
	}{
		{
			name:          "missing ByoHost is created with the given labels and patched",
			agentIdentity: identityOne,
			labels:        map[string]string{"example": "label"},
			wantLabels:    map[string]string{"example": "label"},
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
				ByoHostInfo: HostInfo{HostName: registrarNormalizedHostName},
			}

			err := hr.Register(t.Context(), registrarByoHostName, registrarNamespace, tc.labels)

			stored := &infrastructurev1beta1.ByoHost{}
			getErr := k8sClient.Get(t.Context(), key, stored)
			require.NoError(t, getErr)

			if tc.wantConflict {
				require.ErrorIs(t, err, ErrIdentityConflict)
				assert.Contains(t, err.Error(), identityOne)
				assert.Contains(t, err.Error(), identityTwo)
				assert.Contains(t, err.Error(), "decommission")
				assert.Equal(t, tc.existing.Spec, stored.Spec)
				assert.Empty(t, stored.Status.HostDetails.NormalizedHostName, "a conflicting ByoHost is not patched")
				assert.NotContains(t, stored.Labels, infrastructurev1beta1.HostArchitectureLabel)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, registrarNormalizedHostName, stored.Status.HostDetails.NormalizedHostName)
			assert.Contains(t, stored.Labels, infrastructurev1beta1.HostArchitectureLabel)
			assert.Subset(t, stored.Labels, tc.wantLabels)
		})
	}
}
