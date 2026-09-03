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
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/registration"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common/hostname"
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
		hostName:  "test-host",
		namespace: "default",
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

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "byoh-test"},
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

// The parser exists only to substitute the host's network interface name into
// bootstrap data, so an unknown interface must leave it nil rather than
// producing a parser that substitutes an empty string.
func TestSetupTemplateParser(t *testing.T) {
	testCases := []struct {
		name          string
		interfaceName string
		wantParser    bool
	}{
		{
			name:          "no default interface discovered",
			interfaceName: "",
			wantParser:    false,
		},
		{
			name:          "default interface discovered",
			interfaceName: "eth0",
			wantParser:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := registration.LocalHostRegistrar
			t.Cleanup(func() { registration.LocalHostRegistrar = original })

			registration.LocalHostRegistrar = &registration.HostRegistrar{
				ByoHostInfo: registration.HostInfo{
					DefaultNetworkInterfaceName: tc.interfaceName,
				},
			}

			parser := setupTemplateParser()

			if !tc.wantParser {
				assert.Nil(t, parser)
				return
			}

			require.NotNil(t, parser)
			info, ok := parser.Template.(registration.HostInfo)
			require.True(t, ok, "template should carry host info")
			assert.Equal(t, tc.interfaceName, info.DefaultNetworkInterfaceName)
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

			err := certRotation(t.Context(), logger, "test-host", &rest.Config{
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

	for _, name := range []string{opts.hostName, "other-host"} {
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
	assert.Equal(t, opts.hostName, hosts.Items[0].Name)
}

func TestRetryWithBackoff(t *testing.T) {
	testCases := []struct {
		name string
		// failures is how many times the attempt fails before succeeding.
		// A negative value means it never succeeds.
		failures     int
		cancelAfter  int
		wantAttempts int
		wantErr      error
	}{
		{
			name:         "succeeds on the first attempt",
			failures:     0,
			wantAttempts: 1,
		},
		{
			name:         "succeeds after a few failures",
			failures:     3,
			wantAttempts: 4,
		},
		{
			name:         "gives up only when the context is done",
			failures:     -1,
			cancelAfter:  2,
			wantAttempts: 2,
			wantErr:      context.Canceled,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			attempts := 0
			attempt := func() error {
				attempts++
				if tc.cancelAfter > 0 && attempts >= tc.cancelAfter {
					cancel()
				}
				if tc.failures < 0 || attempts <= tc.failures {
					return errors.New("attempt failed")
				}
				return nil
			}

			err := retryWithBackoff(ctx, logr.Discard(), time.Millisecond, 4*time.Millisecond, attempt)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantAttempts, attempts)
		})
	}
}

// readHostName must never fall back to the machine's own reported host name: byohctl already
// normalizes the name it registers on the management cluster side, and a disagreement between the
// two only shows up much later as a certificate common name mismatch.
func TestReadHostName(t *testing.T) {
	testCases := []struct {
		name       string
		writeFile  bool
		content    string
		want       string
		wantErrSub string
	}{
		{
			name:      "reads and normalizes the identity file",
			writeFile: true,
			content:   "My-Host_Name.\n",
			want:      "my-host-name",
		},
		{
			name:       "missing identity file is a startup error",
			writeFile:  false,
			wantErrSub: "read host identity",
		},
		{
			name:       "empty identity file is a startup error",
			writeFile:  true,
			content:    "   \n",
			wantErrSub: "is empty",
		},
		{
			name:       "identity that cannot normalize into an object name is a startup error",
			writeFile:  true,
			content:    "not a valid host name!!",
			wantErrSub: "normalize host identity",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.writeFile {
				path := filepath.Join(dir, hostname.FileName)
				writeErr := os.WriteFile(path, []byte(tc.content), 0o600)
				require.NoError(t, writeErr)
			}

			got, err := readHostName(dir)

			if tc.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveNamespace(t *testing.T) {
	testCases := []struct {
		name        string
		flagValue   string
		flagChanged bool
		writeFile   bool
		fileContent string
		want        string
	}{
		{
			name:        "an explicit flag wins over the namespace file",
			flagValue:   "flag-ns",
			flagChanged: true,
			writeFile:   true,
			fileContent: "file-ns",
			want:        "flag-ns",
		},
		{
			name:        "the namespace file is used when the flag was not set",
			flagValue:   "default",
			flagChanged: false,
			writeFile:   true,
			fileContent: "file-ns",
			want:        "file-ns",
		},
		{
			name:      "the flag value is kept when no namespace file exists",
			flagValue: "default",
			want:      "default",
		},
		{
			name:        "the flag value is kept when the namespace file is empty",
			flagValue:   "default",
			writeFile:   true,
			fileContent: "   \n",
			want:        "default",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.writeFile {
				path := filepath.Join(dir, namespaceFileName)
				writeErr := os.WriteFile(path, []byte(tc.fileContent), 0o600)
				require.NoError(t, writeErr)
			}

			got := resolveNamespace(tc.flagValue, tc.flagChanged, dir)

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestWaitForBootstrapCredentialReturnsImmediatelyWhenFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap-kubeconfig.yaml")
	err := os.WriteFile(path, []byte("data"), 0o600)
	require.NoError(t, err)

	waitErr := waitForBootstrapCredential(t.Context(), logr.Discard(), path, time.Hour)

	require.NoError(t, waitErr)
}

// The wait must notice the file on its own short poll interval, not on retryWithBackoff's delay,
// which starts at bootstrapRetryInitialDelay and only grows from there.
func TestWaitForBootstrapCredentialReturnsPromptlyOnceFileAppears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap-kubeconfig.yaml")

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- waitForBootstrapCredential(t.Context(), logr.Discard(), path, time.Millisecond)
	}()

	err := os.WriteFile(path, []byte("data"), 0o600)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		select {
		case waitErr := <-done:
			assert.NoError(t, waitErr)
			return true
		default:
			return false
		}
	}, time.Second, 5*time.Millisecond, "waitForBootstrapCredential never returned after the file appeared")

	assert.Less(t, time.Since(start), bootstrapRetryInitialDelay,
		"the wait should not have consumed any of the retry backoff's initial delay")
}

func TestWaitForBootstrapCredentialReturnsContextErrorWhenCanceled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bootstrap-kubeconfig.yaml")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := waitForBootstrapCredential(ctx, logr.Discard(), path, time.Millisecond)

	require.ErrorIs(t, err, context.Canceled)
}

// ensureBootstrapCredential reads the bootstrapKubeConfig package variable directly, matching
// handleBootstrapFlow, which it wraps; these tests save and restore it like the existing bootstrap
// flow tests in host_agent_test.go do.
func TestEnsureBootstrapCredential(t *testing.T) {
	t.Run("skips the certificate path when byohConfigPath already exists", func(t *testing.T) {
		original := bootstrapKubeConfig
		t.Cleanup(func() { bootstrapKubeConfig = original })
		bootstrapKubeConfig = filepath.Join(t.TempDir(), "does-not-exist")

		byohConfigPath := filepath.Join(t.TempDir(), "config")
		writeErr := os.WriteFile(byohConfigPath, []byte("existing"), 0o600)
		require.NoError(t, writeErr)

		err := ensureBootstrapCredential(t.Context(), logr.Discard(), "test-host", byohConfigPath)

		require.NoError(t, err)
	})

	t.Run("is a no-op when no bootstrap kubeconfig is configured", func(t *testing.T) {
		original := bootstrapKubeConfig
		t.Cleanup(func() { bootstrapKubeConfig = original })
		bootstrapKubeConfig = ""

		byohConfigPath := filepath.Join(t.TempDir(), "config")

		err := ensureBootstrapCredential(t.Context(), logr.Discard(), "test-host", byohConfigPath)

		require.NoError(t, err)
	})

	t.Run("waits for the bootstrap kubeconfig before attempting the CSR exchange", func(t *testing.T) {
		original := bootstrapKubeConfig
		t.Cleanup(func() { bootstrapKubeConfig = original })

		dir := t.TempDir()
		bootstrapKubeConfig = filepath.Join(dir, "bootstrap-kubeconfig.yaml")
		byohConfigPath := filepath.Join(dir, "config")

		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		// The bootstrap kubeconfig file never appears, so this can only return once ctx is
		// done. If ensureBootstrapCredential skipped the wait and went straight into
		// handleBootstrapFlow, that call would fail immediately on the missing file instead
		// of blocking on it, and this would return well before the deadline.
		start := time.Now()
		err := ensureBootstrapCredential(ctx, logr.Discard(), "test-host", byohConfigPath)
		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
	})
}
