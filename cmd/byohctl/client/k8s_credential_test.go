// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newTestSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string][]byte{
			"hostName":   []byte("host1"),
			"kubeconfig": []byte("apiVersion: v1\nkind: Config\n"),
		},
	}
}

func TestGetCredentialSecret(t *testing.T) {
	tests := []struct {
		name       string
		seedSecret bool
		wantErr    bool
	}{
		{name: "secret exists", seedSecret: true, wantErr: false},
		{name: "secret does not exist", seedSecret: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := k8sfake.NewSimpleClientset()
			if tt.seedSecret {
				clientset = k8sfake.NewSimpleClientset(newTestSecret("ns1", "host1-bootstrap"))
			}
			c := &Client{Clientset: clientset}

			secret, err := c.GetCredentialSecret(t.Context(), "ns1", "host1-bootstrap")
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, apierrors.IsNotFound(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "host1-bootstrap", secret.Name)
			assert.Equal(t, []byte("host1"), secret.Data["hostName"])
		})
	}
}

func TestAwaitCredentialSecret_AlreadyExists(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset(newTestSecret("ns1", "host1-bootstrap"))
	c := &Client{Clientset: clientset}

	secret, err := c.AwaitCredentialSecret(t.Context(), "ns1", "host1-bootstrap", 10*time.Millisecond, 200*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "host1-bootstrap", secret.Name)
}

func TestAwaitCredentialSecret_AppearsWhilePolling(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	c := &Client{Clientset: clientset}

	go func() {
		time.Sleep(30 * time.Millisecond)
		_, err := clientset.CoreV1().Secrets("ns1").Create(t.Context(), newTestSecret("ns1", "host1-bootstrap"), metav1.CreateOptions{})
		assert.NoError(t, err)
	}()

	secret, err := c.AwaitCredentialSecret(t.Context(), "ns1", "host1-bootstrap", 10*time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "host1-bootstrap", secret.Name)
}

func TestAwaitCredentialSecret_TimesOut(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	c := &Client{Clientset: clientset}

	_, err := c.AwaitCredentialSecret(t.Context(), "ns1", "host1-bootstrap", 10*time.Millisecond, 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestAwaitCredentialSecret_AbortsOnNonNotFoundError(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	calls := 0
	clientset.PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "host1-bootstrap", nil)
	})
	c := &Client{Clientset: clientset}

	_, err := c.AwaitCredentialSecret(t.Context(), "ns1", "host1-bootstrap", 10*time.Millisecond, time.Second)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "timed out")
	assert.Equal(t, 1, calls)
}
