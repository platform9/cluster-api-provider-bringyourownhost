// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
)

// statusTenant is the tenant namespace of the hosts in the status tests.
const statusTenant = "tenant-a"

var (
	statusHostOne = identity.HostIdentity{
		TenantNamespace:    statusTenant,
		NormalizedHostName: "h1",
		Suffix:             "abc12",
	}
	statusHostTwo = identity.HostIdentity{
		TenantNamespace:    statusTenant,
		NormalizedHostName: "h2",
		Suffix:             "def34",
	}
)

func TestByoHostValidator_Handle_statusSubresource(t *testing.T) {
	testCases := []struct {
		name        string
		userName    string
		namespace   string
		oldIdentity string
		wantAllow   bool
		wantMsg     string
	}{
		{
			name:        "host patching the status of its own ByoHost",
			userName:    statusHostOne.String(),
			namespace:   statusTenant,
			oldIdentity: statusHostOne.String(),
			wantAllow:   true,
			wantMsg:     "",
		},
		{
			name:        "host patching the status of another host in its tenant",
			userName:    statusHostOne.String(),
			namespace:   statusTenant,
			oldIdentity: statusHostTwo.String(),
			wantAllow:   false,
			wantMsg:     statusHostOne.String() + " cannot update resource " + statusHostTwo.ByoHostName(),
		},
		{
			name:        "host patching the status of a ByoHost in another tenant",
			userName:    statusHostOne.String(),
			namespace:   "tenant-b",
			oldIdentity: statusHostOne.String(),
			wantAllow:   false,
			wantMsg:     statusHostOne.String() + " cannot create/update resource " + statusHostOne.ByoHostName(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeByoHostValidator(t, nil)

			hostName := statusHostOne.ByoHostName()
			if tc.oldIdentity == statusHostTwo.String() {
				hostName = statusHostTwo.ByoHostName()
			}
			host := &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostName,
					Namespace: tc.namespace,
				},
				Spec: ByoHostSpec{
					Identity: tc.oldIdentity,
				},
			}
			raw, err := json.Marshal(host)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation:   admissionv1.Update,
					SubResource: "status",
					Namespace:   tc.namespace,
					UserInfo:    authenticationv1.UserInfo{Username: tc.userName},
					Object:      runtime.RawExtension{Raw: raw},
					OldObject:   runtime.RawExtension{Raw: raw},
				},
			}

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}

// TestByoHostWebhook_statusPatchWithHostCertificate sends status patches
// through a real API server. The webhook registration decides whether the
// validator sees them.
func TestByoHostWebhook_statusPatchWithHostCertificate(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "config", "webhook")},
		},
	}
	adminConfig, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		stopErr := testEnv.Stop()
		assert.NoError(t, stopErr)
	})

	testScheme := runtime.NewScheme()
	err = scheme.AddToScheme(testScheme)
	require.NoError(t, err)
	err = AddToScheme(testScheme)
	require.NoError(t, err)

	webhookOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(adminConfig, ctrl.Options{
		Scheme: testScheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOptions.LocalServingHost,
			Port:    webhookOptions.LocalServingPort,
			CertDir: webhookOptions.LocalServingCertDir,
		}),
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	mgr.GetWebhookServer().Register("/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost", &webhook.Admission{
		Handler: &ByoHostValidator{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(testScheme),
		},
	})
	mgr.GetWebhookServer().Register("/mutate-infrastructure-cluster-x-k8s-io-v1beta1-byohost", &webhook.Admission{
		Handler: &ByoHostIdentityStamper{
			Decoder: admission.NewDecoder(testScheme),
		},
	})

	go func() {
		startErr := mgr.Start(t.Context())
		assert.NoError(t, startErr)
	}()

	dialer := &net.Dialer{Timeout: time.Second}
	addr := fmt.Sprintf("%s:%d", webhookOptions.LocalServingHost, webhookOptions.LocalServingPort)
	require.Eventually(t, func() bool {
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server has a self-signed certificate
		}
		conn, dialErr := tlsDialer.DialContext(t.Context(), "tcp", addr)
		if dialErr != nil {
			return false
		}
		return conn.Close() == nil
	}, 10*time.Second, 100*time.Millisecond)

	adminClient, err := client.New(adminConfig, client.Options{Scheme: testScheme})
	require.NoError(t, err)
	err = adminClient.Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: statusTenant},
	})
	require.NoError(t, err)

	// The users are in system:masters, so RBAC allows every request and only
	// admission can deny one.
	newHostClient := func(hostIdentity identity.HostIdentity) client.Client {
		t.Helper()

		user, addErr := testEnv.AddUser(envtest.User{
			Name:   hostIdentity.String(),
			Groups: []string{"system:masters"},
		}, nil)
		require.NoError(t, addErr)

		hostClient, newErr := client.New(user.Config(), client.Options{Scheme: testScheme})
		require.NoError(t, newErr)

		return hostClient
	}
	hostOneClient := newHostClient(statusHostOne)
	hostTwoClient := newHostClient(statusHostTwo)

	// Each host creates its own ByoHost, so the stamper records its identity.
	for hostClient, hostIdentity := range map[client.Client]identity.HostIdentity{
		hostOneClient: statusHostOne,
		hostTwoClient: statusHostTwo,
	} {
		createErr := hostClient.Create(t.Context(), &ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostIdentity.ByoHostName(),
				Namespace: statusTenant,
			},
		})
		require.NoError(t, createErr)
	}

	patch := []byte(`{"status":{"machineRef":{"name":"machine-1"}}}`)

	testCases := []struct {
		name      string
		client    client.Client
		hostName  string
		wantAllow bool
	}{
		{
			name:      "host patching the status of its own ByoHost",
			client:    hostOneClient,
			hostName:  statusHostOne.ByoHostName(),
			wantAllow: true,
		},
		{
			name:      "host patching the status of another host in its tenant",
			client:    hostOneClient,
			hostName:  statusHostTwo.ByoHostName(),
			wantAllow: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			host := &ByoHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tc.hostName,
					Namespace: statusTenant,
				},
			}

			err := tc.client.Status().Patch(t.Context(), host, client.RawPatch(types.MergePatchType, patch))

			if tc.wantAllow {
				require.NoError(t, err)
				assert.Equal(t, "machine-1", host.Status.MachineRef.Name)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsForbidden(err), "want a forbidden error, got %v", err)
		})
	}
}
