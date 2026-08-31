// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// wantControllers is the number of runnables setupControllers adds to the
// manager when CSR auto-approval is on: one per controller
// (ClusterCacheReconciler, ByoMachine, ByoHost, ByoHostAgentUpgrade,
// ByoMachineTemplate, ByoCluster, ByoAdmission, ByoHostEnrollment,
// K8sInstallerConfig, BootstrapKubeconfig). Webhooks register handler paths on
// the webhook server instead, so they do not show up here.
const wantControllers = 10

var (
	testEnv *envtest.Environment
	testCfg *rest.Config
)

// errAddRunnable is the failure failingManager injects, so a test can assert
// setupControllers wraps the underlying error rather than flattening it.
var errAddRunnable = errors.New("cannot add runnable")

// recordingManager counts the runnables added to the manager it wraps while
// still forwarding them, so tests observe the real registrations rather than
// a stub.
type recordingManager struct {
	ctrl.Manager

	mu    sync.Mutex
	added []manager.Runnable
}

func (m *recordingManager) Add(r manager.Runnable) error {
	m.mu.Lock()
	m.added = append(m.added, r)
	m.mu.Unlock()

	return m.Manager.Add(r)
}

func (m *recordingManager) addedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.added)
}

// failingManager rejects every runnable, which is the simplest way to make a
// controller's SetupWithManager fail.
type failingManager struct {
	ctrl.Manager
}

func (m *failingManager) Add(manager.Runnable) error {
	return errAddRunnable
}

// TestMain owns the control plane. Every test in this package needs it, so it
// starts eagerly here rather than on first use.
func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.10.10", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.10.10", "bootstrap", "kubeadm", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
		// Generates the serving certificate the manager's webhook server
		// needs; setupControllers always registers webhook handlers.
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("config", "webhook")},
		},
	}

	var err error
	testCfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
		code = 1
	}

	os.Exit(code)
}

// newTestManager builds a manager against the envtest control plane. Metrics
// and health endpoints are off so parallel tests never fight over a port.
func newTestManager(t *testing.T) ctrl.Manager {
	t.Helper()

	webhookOpts := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookOpts.LocalServingHost,
			Port:    webhookOpts.LocalServingPort,
			CertDir: webhookOpts.LocalServingCertDir,
		}),
		LeaderElection: false,
		// Controller names are validated against a process-wide registry,
		// not a per-manager one, so every test after the first would fail
		// on a duplicate name. Only production needs that guard.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)

	return mgr
}

func testOptions() controllerOptions {
	return controllerOptions{
		heartbeatTimeout: DefaultHeartbeatTimeout,
		csrClientSet:     fake.NewSimpleClientset(),
	}
}

func TestSetupControllers(t *testing.T) {
	testCases := []struct {
		name string
		// csrClientSet nil is how MANUAL_CSR_APPROVAL=enable reaches
		// setupControllers.
		csrClientSet clientset.Interface
		want         int
	}{
		{
			name:         "CSR auto-approval on registers every controller",
			csrClientSet: fake.NewSimpleClientset(),
			want:         wantControllers,
		},
		{
			name:         "CSR auto-approval off drops only ByoAdmission",
			csrClientSet: nil,
			want:         wantControllers - 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingManager{Manager: newTestManager(t)}

			opts := testOptions()
			opts.csrClientSet = tc.csrClientSet

			err := setupControllers(t.Context(), rec, opts)
			require.NoError(t, err)

			assert.Equal(t, tc.want, rec.addedCount())
		})
	}
}

// setupControllers must hand registration failures back to main() instead of
// exiting, so main() stays the only place that decides to terminate.
func TestSetupControllersReturnsRegistrationError(t *testing.T) {
	mgr := &failingManager{Manager: newTestManager(t)}

	err := setupControllers(t.Context(), mgr, testOptions())
	require.Error(t, err)
	assert.ErrorIs(t, err, errAddRunnable)
	assert.Contains(t, err.Error(), "ClusterCacheReconciler")
}

// The context main() builds from the signal handler is the only stop signal
// the controllers have. Canceling it must bring the whole manager down: if any
// setup call captured a different context (context.TODO(), say), the watches
// it owns outlive the manager and Start never returns cleanly.
func TestManagerStopsWhenSetupContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr := newTestManager(t)

	err := setupControllers(ctx, mgr, testOptions())
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
