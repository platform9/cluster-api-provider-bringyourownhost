// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/platform9/cluster-api-provider-bringyourownhost/agent/registration"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/hostname"
)

// certificatePEMBlockType is the PEM block type certRotation insists on.
const certificatePEMBlockType = "CERTIFICATE"

// The ginkgo suite in this package owns testEnv and starts it in BeforeSuite,
// which only runs for ginkgo specs. These Go tests need their own control
// plane, hence the separate names.
var (
	agentEnvOnce sync.Once
	agentEnvErr  error
	agentEnv     *envtest.Environment
	agentCfg     *rest.Config
)

// errAddRunnable is the failure failingManager injects, so a test can assert
// setupHostReconciler wraps the underlying error rather than flattening it.
var errAddRunnable = errors.New("cannot add runnable")

// failingManager rejects every runnable, which is the simplest way to make the
// reconciler's SetupWithManager fail.
type failingManager struct {
	ctrl.Manager
}

func (m *failingManager) Add(manager.Runnable) error {
	return errAddRunnable
}

// TestMain stops the control plane. Nothing starts it here: most tests in this
// package need no API server, so startAgentEnvtest brings one up on first use.
func TestMain(m *testing.M) {
	code := m.Run()

	if agentEnv != nil {
		if err := agentEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
			code = 1
		}
	}

	os.Exit(code)
}

// startAgentEnvtest brings up the control plane on the first call and puts the
// package globals main() would have set into place.
func startAgentEnvtest(t *testing.T) {
	t.Helper()

	agentEnvOnce.Do(func() {
		agentEnv = &envtest.Environment{
			CRDDirectoryPaths: []string{
				filepath.Join("..", "config", "crd", "bases"),
			},
			ErrorIfCRDPathMissing: true,
		}

		agentCfg, agentEnvErr = agentEnv.Start()
	})
	require.NoError(t, agentEnvErr, "start envtest")

	// main() registers the host before it reaches setupHostReconciler, and
	// setupTemplateParser reads that registrar, so a test standing in for
	// main() has to provide both.
	originalRegistrar := registration.LocalHostRegistrar
	t.Cleanup(func() {
		registration.LocalHostRegistrar = originalRegistrar
		scheme = nil
	})
	registration.LocalHostRegistrar = &registration.HostRegistrar{}
	scheme = newScheme()
}

// newReconcilerTestManager builds a plain manager for the tests that register
// the host reconciler. Controller names are validated against a process-wide
// registry, not a per-manager one, so the second test to register would fail
// on a duplicate name. Only production needs that guard.
func newReconcilerTestManager(t *testing.T) ctrl.Manager {
	t.Helper()

	startAgentEnvtest(t)

	mgr, err := ctrl.NewManager(agentCfg, ctrl.Options{
		Scheme:     scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)

	return mgr
}

func testAgentOptions() *agentOptions {
	return &agentOptions{
		byoHostName: "test-host-abc12",
		namespace:   "default",
		// Off so parallel tests never fight over a port.
		metricsBindAddress:  "0",
		downloadPath:        "/var/lib/byoh/bundles",
		heartbeatInterval:   DefaultHeartbeatInterval,
		maxBlockingDuration: DefaultMaxBlockingDuration,
		exit:                func() {},
	}
}

// certPEM issues a self-signed certificate whose validity window starts
// lifetime ago and ends after remaining, so a test can place "now" anywhere
// relative to the 20% renewal threshold.
func certPEM(t *testing.T, lifetime, remaining time.Duration) []byte {
	t.Helper()

	return certPEMWithCommonName(t, "byoh-test", lifetime, remaining)
}

func certPEMWithCommonName(t *testing.T, commonName string, lifetime, remaining time.Duration) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-lifetime),
		NotAfter:     time.Now().Add(remaining),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: der})
}

// recordingLogger collects every message logged through it, which is how these
// tests observe which branch certRotation took.
func recordingLogger(messages *[]string) logr.Logger {
	return funcr.New(func(prefix, args string) {
		*messages = append(*messages, prefix+args)
	}, funcr.Options{})
}

func TestNewScheme(t *testing.T) {
	s := newScheme()

	testCases := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{
			name: "ByoHost",
			gvk:  infrastructurev1beta1.GroupVersion.WithKind("ByoHost"),
		},
		{
			name: "core Secret",
			gvk:  corev1.SchemeGroupVersion.WithKind("Secret"),
		},
		{
			name: "CAPI Cluster",
			gvk:  clusterv1.GroupVersion.WithKind("Cluster"),
		},
		{
			name: "CertificateSigningRequest",
			gvk:  certv1.SchemeGroupVersion.WithKind("CertificateSigningRequest"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, s.Recognizes(tc.gvk))
		})
	}
}

func TestLabelFlagsString(t *testing.T) {
	testCases := []struct {
		name  string
		flags labelFlags
		want  []string
	}{
		{
			name:  "no labels",
			flags: labelFlags{},
			want:  nil,
		},
		{
			name:  "one label",
			flags: labelFlags{"k1": "v1"},
			want:  []string{"k1=v1"},
		},
		{
			name:  "two labels",
			flags: labelFlags{"k1": "v1", "k2": "v2"},
			want:  []string{"k1=v1", "k2=v2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.flags.String()

			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			// Map iteration order is unspecified, so compare the parts.
			assert.ElementsMatch(t, tc.want, strings.Split(got, ","))
		})
	}
}

// The parser exists only to substitute host details into bootstrap data, so
// a host with neither a default interface nor a host name must leave it nil
// rather than producing a parser that substitutes empty strings.
func TestSetupTemplateParser(t *testing.T) {
	testCases := []struct {
		name          string
		interfaceName string
		hostName      string
		template      string
		wantParser    bool
		want          string
	}{
		{
			name:       "no interface and no host name",
			wantParser: false,
		},
		{
			name:          "default interface only",
			interfaceName: "eth0",
			template:      "iface={{ .DefaultNetworkInterfaceName }} host={{ .HostName }}",
			wantParser:    true,
			want:          "iface=eth0 host=",
		},
		{
			name:       "host name only",
			hostName:   "example-worker-1",
			template:   "iface={{ .DefaultNetworkInterfaceName }} host={{ .HostName }}",
			wantParser: true,
			want:       "iface= host=example-worker-1",
		},
		{
			name:          "interface and host name",
			interfaceName: "eth0",
			hostName:      "example-worker-1",
			template:      "iface={{ .DefaultNetworkInterfaceName }} host={{ .HostName }}",
			wantParser:    true,
			want:          "iface=eth0 host=example-worker-1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := registration.LocalHostRegistrar
			t.Cleanup(func() { registration.LocalHostRegistrar = original })

			registration.LocalHostRegistrar = &registration.HostRegistrar{
				ByoHostInfo: registration.HostInfo{
					DefaultNetworkInterfaceName: tc.interfaceName,
					HostName:                    tc.hostName,
				},
			}

			parser := setupTemplateParser()

			if !tc.wantParser {
				assert.Nil(t, parser)
				return
			}

			require.NotNil(t, parser)
			got, err := parser.ParseTemplate(tc.template)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizedHostName(t *testing.T) {
	osHostName, err := os.Hostname()
	require.NoError(t, err)

	want, err := hostname.Normalize(osHostName)
	require.NoError(t, err)

	got, err := normalizedHostName()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestResolveByoHostName(t *testing.T) {
	const (
		hostName = "example-worker-1"
		identity = "byoh:host:example-tenant:example-worker-1-abc12"
	)

	certConfig := func(t *testing.T, commonName string) *rest.Config {
		t.Helper()

		return &rest.Config{
			TLSClientConfig: rest.TLSClientConfig{CertData: certPEMWithCommonName(t, commonName, time.Minute, time.Hour)},
		}
	}

	// stateDir returns a state directory whose state file holds stateIdentity.
	stateDir := func(t *testing.T, stateIdentity string) string {
		t.Helper()

		dir := t.TempDir()
		data := fmt.Sprintf(`{"version":1,"phase":"issued","identity":%q}`, stateIdentity)
		err := os.WriteFile(filepath.Join(dir, registration.StateFileName), []byte(data), 0o600)
		require.NoError(t, err)

		return dir
	}

	testCases := []struct {
		name         string
		stateDir     string
		config       *rest.Config
		wantIdentity string
		wantName     string
		wantErr      bool
	}{
		{
			name:         "host identity in client certificate",
			config:       certConfig(t, identity),
			wantIdentity: identity,
			wantName:     "example-worker-1-abc12",
			wantErr:      false,
		},
		{
			name:         "host identity in state file",
			stateDir:     stateDir(t, identity),
			config:       &rest.Config{},
			wantIdentity: identity,
			wantName:     "example-worker-1-abc12",
			wantErr:      false,
		},
		{
			name:     "no identity",
			config:   &rest.Config{},
			wantName: hostName,
			wantErr:  false,
		},
		{
			name:    "client certificate identity is not a host identity",
			config:  certConfig(t, "byoh-test"),
			wantErr: true,
		},
		{
			name:     "state file identity is not a host identity",
			stateDir: stateDir(t, "byoh-test"),
			config:   &rest.Config{},
			wantErr:  true,
		},
		{
			name:     "unparsable host identity in state file",
			stateDir: stateDir(t, "byoh:host:example-tenant"),
			config:   &rest.Config{},
			wantErr:  true,
		},
		{
			name:    "unparsable host identity in client certificate",
			config:  certConfig(t, "byoh:host:example-tenant"),
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.stateDir
			if dir == "" {
				dir = t.TempDir()
			}

			gotIdentity, gotName, err := resolveByoHostName(dir, tc.config, hostName)

			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, gotIdentity)
				assert.Empty(t, gotName)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantIdentity, gotIdentity)
			assert.Equal(t, tc.wantName, gotName)
		})
	}
}

func TestCertRotation(t *testing.T) {
	// A certificate 90% through its life is inside the 20% renewal window;
	// one 10% through is not.
	nearExpiry := certPEM(t, 90*time.Minute, 10*time.Minute)
	freshCert := certPEM(t, 10*time.Minute, 90*time.Minute)

	testCases := []struct {
		name     string
		certData []byte
		wantErr  bool
		wantLog  string
	}{
		{
			name:     "no PEM data",
			certData: nil,
			wantLog:  "failed to decode PEM block containing certificate",
		},
		{
			name:     "PEM block is not a certificate",
			certData: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("nonsense")}),
			wantLog:  "failed to decode PEM block containing certificate",
		},
		{
			name:     "certificate body is unparsable",
			certData: pem.EncodeToMemory(&pem.Block{Type: certificatePEMBlockType, Bytes: []byte("nonsense")}),
			wantErr:  true,
		},
		{
			name:     "certificate has plenty of life left",
			certData: freshCert,
			wantLog:  "certificate are valid",
		},
		{
			name:     "certificate is inside the renewal window",
			certData: nearExpiry,
			wantLog:  "certificate expiration time left is less than 20%, renewing",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var messages []string
			logger := recordingLogger(&messages)

			err := certRotation(t.Context(), logger, "test-tenant", "test-host", "", &rest.Config{
				TLSClientConfig: rest.TLSClientConfig{CertData: tc.certData},
			})

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, strings.Join(messages, "\n"), tc.wantLog)
		})
	}
}

// setupHostReconciler must hand registration failures back to main() instead
// of exiting, so main() stays the only place that decides to terminate.
func TestSetupHostReconcilerReturnsRegistrationError(t *testing.T) {
	opts := testAgentOptions()
	mgr := &failingManager{Manager: newReconcilerTestManager(t)}

	err := setupHostReconciler(t.Context(), mgr, mgr.GetClient(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, errAddRunnable)
	assert.Contains(t, err.Error(), "host reconciler")
}

// The context main() builds from the signal handler is the only stop signal
// the reconciler has. Canceling it must bring the manager down: if the
// reconciler captured a different context (context.TODO(), say), its event
// filter outlives the manager and Start never returns cleanly.
func TestManagerStopsWhenSetupContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := testAgentOptions()
	mgr := newReconcilerTestManager(t)

	err := setupHostReconciler(ctx, mgr, mgr.GetClient(), opts)
	require.NoError(t, err)

	startErr := make(chan error, 1)
	go func() {
		startErr <- mgr.Start(ctx)
	}()

	synced := mgr.GetCache().WaitForCacheSync(ctx)
	require.True(t, synced, "manager cache never synced")

	cancel()

	select {
	case err := <-startErr:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatal("manager did not stop within a minute of canceling the setup context")
	}
}

// The agent must never cache another host's ByoHost, nor anything outside its
// own namespace.
func TestNewManagerScopesCacheToThisHost(t *testing.T) {
	opts := testAgentOptions()

	startAgentEnvtest(t)
	mgr, err := newManager(agentCfg, opts)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = mgr.Start(ctx)
	}()

	synced := mgr.GetCache().WaitForCacheSync(ctx)
	require.True(t, synced, "manager cache never synced")

	direct, err := client.New(agentCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	for _, name := range []string{opts.byoHostName, "other-host"} {
		host := &infrastructurev1beta1.ByoHost{}
		host.Name = name
		host.Namespace = opts.namespace

		createErr := direct.Create(ctx, host)
		require.NoError(t, createErr)

		t.Cleanup(func() { _ = direct.Delete(context.Background(), host) })
	}

	hosts := &infrastructurev1beta1.ByoHostList{}
	require.Eventually(t, func() bool {
		if err := mgr.GetClient().List(ctx, hosts); err != nil {
			return false
		}
		return len(hosts.Items) > 0
	}, 30*time.Second, 100*time.Millisecond)

	require.Len(t, hosts.Items, 1)
	assert.Equal(t, opts.byoHostName, hosts.Items[0].Name)
}
