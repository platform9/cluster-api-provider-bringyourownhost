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

// startByoHostWebhookEnv starts an API server with the ByoHost webhooks
// registered and returns it with the scheme of its clients. The Namespace
// statusTenant exists.
func startByoHostWebhookEnv(t *testing.T) (*envtest.Environment, *runtime.Scheme) {
	t.Helper()

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

	return testEnv, testScheme
}

// newWebhookUserClient returns a client that authenticates as userName. The
// user is in system:masters, so RBAC allows every request and only admission
// can deny one.
func newWebhookUserClient(t *testing.T, testEnv *envtest.Environment, testScheme *runtime.Scheme, userName string) client.Client {
	t.Helper()

	user, err := testEnv.AddUser(envtest.User{
		Name:   userName,
		Groups: []string{"system:masters"},
	}, nil)
	require.NoError(t, err)

	userClient, err := client.New(user.Config(), client.Options{Scheme: testScheme})
	require.NoError(t, err)

	return userClient
}

// TestByoHostWebhook_statusPatchWithHostCertificate sends status patches
// through a real API server. The webhook registration decides whether the
// validator sees them.
func TestByoHostWebhook_statusPatchWithHostCertificate(t *testing.T) {
	testEnv, testScheme := startByoHostWebhookEnv(t)

	hostOneClient := newWebhookUserClient(t, testEnv, testScheme, statusHostOne.String())
	hostTwoClient := newWebhookUserClient(t, testEnv, testScheme, statusHostTwo.String())

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

// secretRefs holds the three Secret references of a ByoHost spec.
type secretRefs struct {
	bootstrap      *corev1.ObjectReference
	installation   *corev1.ObjectReference
	uninstallation *corev1.ObjectReference
}

func secretRef(namespace, name string) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Namespace: namespace,
		Name:      name,
	}
}

func TestByoHostValidator_Handle_secretReferences(t *testing.T) {
	hostName := statusHostOne.ByoHostName()
	hostUser := statusHostOne.String()
	managerUser := kaapiManagerServiceAccount
	tokenUser := "system:serviceaccount:" + statusTenant + ":byoh"

	existing := secretRefs{
		bootstrap:      secretRef("", "m1-bootstrap"),
		installation:   secretRef("", "byoh-install-m1"),
		uninstallation: secretRef(statusTenant, "byoh-uninstall-m1"),
	}
	withBootstrap := func(refs secretRefs, ref *corev1.ObjectReference) secretRefs {
		refs.bootstrap = ref
		return refs
	}
	withInstallation := func(refs secretRefs, ref *corev1.ObjectReference) secretRefs {
		refs.installation = ref
		return refs
	}
	withUninstallation := func(refs secretRefs, ref *corev1.ObjectReference) secretRefs {
		refs.uninstallation = ref
		return refs
	}

	testCases := []struct {
		name      string
		operation admissionv1.Operation
		userName  string
		oldRefs   secretRefs
		newRefs   secretRefs
		newLabels map[string]string
		wantAllow bool
		wantMsg   string
	}{
		{
			name:      "host changes uninstallationSecret to another Secret",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   withUninstallation(existing, secretRef(statusTenant, "byoh-token")),
			wantAllow: false,
			wantMsg:   hostUser + " cannot change spec.uninstallationSecret of resource " + hostName,
		},
		{
			name:      "host changes bootstrapSecret to another Secret",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   withBootstrap(existing, secretRef("", "byoh-token")),
			wantAllow: false,
			wantMsg:   hostUser + " cannot change spec.bootstrapSecret of resource " + hostName,
		},
		{
			name:      "host changes installationSecret to another Secret",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   withInstallation(existing, secretRef("", "byoh-token")),
			wantAllow: false,
			wantMsg:   hostUser + " cannot change spec.installationSecret of resource " + hostName,
		},
		{
			name:      "host sets an unset reference",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   withUninstallation(existing, nil),
			newRefs:   existing,
			wantAllow: false,
			wantMsg:   hostUser + " cannot change spec.uninstallationSecret of resource " + hostName,
		},
		{
			name:      "host clears installationSecret",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   withInstallation(existing, nil),
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "host clears bootstrapSecret",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   withBootstrap(existing, nil),
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "host changes labels and keeps the references",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   existing,
			newRefs:   existing,
			newLabels: map[string]string{"zone": "a"},
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "host updates a ByoHost that has no references",
			operation: admissionv1.Update,
			userName:  hostUser,
			oldRefs:   secretRefs{},
			newRefs:   secretRefs{},
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "manager changes the references",
			operation: admissionv1.Update,
			userName:  managerUser,
			oldRefs:   existing,
			newRefs:   withUninstallation(existing, secretRef(statusTenant, "byoh-uninstall-m2")),
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "token ServiceAccount changes the references",
			operation: admissionv1.Update,
			userName:  tokenUser,
			oldRefs:   existing,
			newRefs:   withUninstallation(existing, secretRef(statusTenant, "byoh-uninstall-m2")),
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "host creates its ByoHost with uninstallationSecret",
			operation: admissionv1.Create,
			userName:  hostUser,
			newRefs:   secretRefs{uninstallation: secretRef(statusTenant, "byoh-token")},
			wantAllow: false,
			wantMsg:   hostUser + " cannot set spec.uninstallationSecret on create of resource " + hostName,
		},
		{
			name:      "host creates its ByoHost with bootstrapSecret",
			operation: admissionv1.Create,
			userName:  hostUser,
			newRefs:   secretRefs{bootstrap: secretRef("", "byoh-token")},
			wantAllow: false,
			wantMsg:   hostUser + " cannot set spec.bootstrapSecret on create of resource " + hostName,
		},
		{
			name:      "host creates its ByoHost with installationSecret",
			operation: admissionv1.Create,
			userName:  hostUser,
			newRefs:   secretRefs{installation: secretRef("", "byoh-token")},
			wantAllow: false,
			wantMsg:   hostUser + " cannot set spec.installationSecret on create of resource " + hostName,
		},
		{
			name:      "host creates its ByoHost with an empty spec",
			operation: admissionv1.Create,
			userName:  hostUser,
			newRefs:   secretRefs{},
			wantAllow: true,
			wantMsg:   "",
		},
		{
			name:      "manager creates a ByoHost with references",
			operation: admissionv1.Create,
			userName:  managerUser,
			newRefs:   existing,
			wantAllow: true,
			wantMsg:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			v := newFakeByoHostValidator(t, nil)

			newHost := &ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       testByoHostKind,
					APIVersion: testAPIVersion,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostName,
					Namespace: statusTenant,
					Labels:    tc.newLabels,
				},
				Spec: ByoHostSpec{
					Identity:             hostUser,
					BootstrapSecret:      tc.newRefs.bootstrap,
					InstallationSecret:   tc.newRefs.installation,
					UninstallationSecret: tc.newRefs.uninstallation,
				},
			}
			oldHost := newHost.DeepCopy()
			oldHost.Labels = nil
			oldHost.Spec.BootstrapSecret = tc.oldRefs.bootstrap
			oldHost.Spec.InstallationSecret = tc.oldRefs.installation
			oldHost.Spec.UninstallationSecret = tc.oldRefs.uninstallation

			newRaw, err := json.Marshal(newHost)
			require.NoError(t, err)
			oldRaw, err := json.Marshal(oldHost)
			require.NoError(t, err)

			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: tc.operation,
					Namespace: statusTenant,
					UserInfo:  authenticationv1.UserInfo{Username: tc.userName},
					Object:    runtime.RawExtension{Raw: newRaw},
				},
			}
			if tc.operation == admissionv1.Update {
				req.OldObject = runtime.RawExtension{Raw: oldRaw}
			}

			resp := v.Handle(t.Context(), req)

			assert.Equal(t, tc.wantAllow, resp.Allowed)
			assert.Equal(t, tc.wantMsg, resp.Result.Message)
		})
	}
}

// TestByoHostWebhook_secretReferencesWithHostCertificate sends writes of the
// Secret references through a real API server.
func TestByoHostWebhook_secretReferencesWithHostCertificate(t *testing.T) {
	testEnv, testScheme := startByoHostWebhookEnv(t)

	hostOneClient := newWebhookUserClient(t, testEnv, testScheme, statusHostOne.String())
	hostTwoClient := newWebhookUserClient(t, testEnv, testScheme, statusHostTwo.String())
	managerClient := newWebhookUserClient(t, testEnv, testScheme, kaapiManagerServiceAccount)

	// Creating with a reference set is denied, so the host creates an empty
	// ByoHost and the manager sets the references afterwards.
	hostName := statusHostOne.ByoHostName()
	err := hostOneClient.Create(t.Context(), &ByoHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hostName,
			Namespace: statusTenant,
		},
	})
	require.NoError(t, err)

	hostKey := client.ObjectKey{
		Namespace: statusTenant,
		Name:      hostName,
	}
	managerSets := &ByoHost{}
	err = managerClient.Get(t.Context(), hostKey, managerSets)
	require.NoError(t, err)
	managerSets.Spec.InstallationSecret = secretRef("", "byoh-install-m1")
	managerSets.Spec.UninstallationSecret = secretRef(statusTenant, "byoh-uninstall-m1")
	err = managerClient.Update(t.Context(), managerSets)
	require.NoError(t, err)

	hostPatch := func(c client.Client, patch string) error {
		host := &ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostName,
				Namespace: statusTenant,
			},
		}
		return c.Patch(t.Context(), host, client.RawPatch(types.MergePatchType, []byte(patch)))
	}

	t.Run("host changes uninstallationSecret to byoh-token", func(t *testing.T) {
		err := hostPatch(hostOneClient, `{"spec":{"uninstallationSecret":{"namespace":"tenant-a","name":"byoh-token"}}}`)

		require.Error(t, err)
		assert.True(t, apierrors.IsForbidden(err), "want a forbidden error, got %v", err)
	})

	t.Run("host creates a ByoHost with uninstallationSecret set", func(t *testing.T) {
		err := hostTwoClient.Create(t.Context(), &ByoHost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statusHostTwo.ByoHostName(),
				Namespace: statusTenant,
			},
			Spec: ByoHostSpec{
				UninstallationSecret: secretRef(statusTenant, "byoh-token"),
			},
		})

		require.Error(t, err)
		assert.True(t, apierrors.IsForbidden(err), "want a forbidden error, got %v", err)
	})

	t.Run("host clears installationSecret", func(t *testing.T) {
		err := hostPatch(hostOneClient, `{"spec":{"installationSecret":null}}`)
		require.NoError(t, err)

		got := &ByoHost{}
		err = managerClient.Get(t.Context(), hostKey, got)
		require.NoError(t, err)
		assert.Nil(t, got.Spec.InstallationSecret)
		assert.Equal(t, secretRef(statusTenant, "byoh-uninstall-m1"), got.Spec.UninstallationSecret)
	})
}
