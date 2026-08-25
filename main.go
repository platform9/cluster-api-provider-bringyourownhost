// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	klog "k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"

	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	byohcontrollers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"

	//+kubebuilder:scaffold:imports
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// DefaultHeartbeatTimeout is the duration after which a ByoHost is
// considered disconnected if no agent heartbeat has been received, unless
// overridden via the --byohostagent-heartbeat-timeout flag.
const DefaultHeartbeatTimeout = 120 * time.Second

var (
	scheme                       = runtime.NewScheme()
	setupLog                     = ctrl.Log.WithName("setup")
	metricsAddr                  string
	enableLeaderElection         bool
	probeAddr                    string
	byohostAgentHeartbeatTimeout time.Duration
)

// controllerOptions is the flag- and environment-derived configuration
// setupControllers needs.
type controllerOptions struct {
	heartbeatTimeout time.Duration

	// Leaves overlay and br_netfilter loaded during uninstall.
	skipKernelModuleCleanup bool

	// Nil disables the ByoAdmission controller, leaving CSRs to be approved
	// manually.
	csrClientSet clientset.Interface
}

func init() {
	klog.InitFlags(nil)
	// clear any discard loggers set by dependecies
	klog.ClearLogger()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(infrastructurev1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme

	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(admissionv1beta1.AddToScheme(scheme))
}

func setFlags() {
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.DurationVar(&byohostAgentHeartbeatTimeout, "byohostagent-heartbeat-timeout", DefaultHeartbeatTimeout, "The duration after which the agent is considered to be disconnected.")
	flag.Parse()
}

// setupControllers registers every controller and webhook with mgr. ctx must be
// the context that also stops mgr: the controllers built here capture it for
// their watches and map functions, so one with a different lifetime leaves
// those watches running past the manager, or dead before it.
func setupControllers(ctx context.Context, mgr ctrl.Manager, opts controllerOptions) error {
	remoteLogger := ctrl.Log.WithName("remote").WithName("ClusterCacheTracker")
	options := remote.ClusterCacheTrackerOptions{Log: &remoteLogger} //nolint: staticcheck
	tracker, err := remote.NewClusterCacheTracker(mgr, options)      //nolint: staticcheck
	if err != nil {
		return fmt.Errorf("create cluster cache tracker: %w", err)
	}

	if err := (&remote.ClusterCacheReconciler{ //nolint: staticcheck
		Client:  mgr.GetClient(),
		Tracker: tracker,
	}).SetupWithManager(ctx, mgr, concurrency(0)); err != nil {
		return fmt.Errorf("create ClusterCacheReconciler controller: %w", err)
	}

	if err := (&byohcontrollers.ByoMachineReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Tracker:  tracker,
		Recorder: mgr.GetEventRecorderFor("byomachine-controller"),
	}).SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("create ByoMachine controller: %w", err)
	}

	if err := (&byohcontrollers.ByoHostReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		HeartbeatTimeoutPeriod: opts.heartbeatTimeout,
		Recorder:               mgr.GetEventRecorderFor("byohost-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create ByoHost controller: %w", err)
	}

	if err := (&byohcontrollers.ByoHostAgentUpgradeReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("byohostagentupgrade-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create ByoHostAgentUpgrade controller: %w", err)
	}

	if err := (&byohcontrollers.ByoMachineTemplateReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create ByoMachineTemplate controller: %w", err)
	}

	if err := (&byohcontrollers.ByoClusterReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("create ByoCluster controller: %w", err)
	}

	if opts.csrClientSet != nil {
		if err := (&byohcontrollers.ByoAdmissionReconciler{
			ClientSet: opts.csrClientSet,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("create ByoAdmission controller: %w", err)
		}
	}

	if err := (&byohcontrollers.K8sInstallerConfigReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		SkipKernelModuleCleanup: opts.skipKernelModuleCleanup,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create K8sInstallerConfig controller: %w", err)
	}

	mgr.GetWebhookServer().Register("/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost", &webhook.Admission{Handler: &infrastructurev1beta1.ByoHostValidator{
		Client:  mgr.GetClient(),
		Decoder: admission.NewDecoder(mgr.GetScheme()),
	}})

	if err := (&byohcontrollers.BootstrapKubeconfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create BootstrapKubeconfig controller: %w", err)
	}

	if err := (&infrastructurev1beta1.BootstrapKubeconfig{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("create BootstrapKubeconfig webhook: %w", err)
	}
	//+kubebuilder:scaffold:builder

	return nil
}

func concurrency(c int) controller.Options {
	return controller.Options{MaxConcurrentReconciles: c}
}

func main() {
	setFlags()
	ctrl.SetLogger(klogr.New()) //nolint: staticcheck // klogr predates the textlogger migration;

	// SetupSignalHandler panics on a second call, so one context is built
	// here for both the controller registrations and mgr.Start.
	ctx := ctrl.SetupSignalHandler()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443}),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "controller-leader-election-caph",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	opts := controllerOptions{
		heartbeatTimeout: byohostAgentHeartbeatTimeout,
		// Set 'BYOH_SKIP_KERNEL_MODULE_CLEANUP=enable' to skip unloading overlay/br_netfilter kernel
		// modules during uninstall. Real BYO hosts own their kernel and must unload these modules;
		// e2e's containerized hosts share Docker's kernel, so unloading them there breaks Docker's
		// own bridge networking and hangs cluster deletion.
		//
		// Uses 'enable'/'disable' (matching MANUAL_CSR_APPROVAL below), not 'true'/'false': kustomize
		// re-serializes manager.yaml and drops the quotes around "${VAR:=default}", so an unquoted
		// true/false would parse as a YAML bool instead of a string, breaking clusterctl's conversion
		// of the rendered Deployment's env value back into a typed corev1.EnvVar.
		skipKernelModuleCleanup: os.Getenv("BYOH_SKIP_KERNEL_MODULE_CLEANUP") == "enable",
	}
	// Set 'MANUAL_CSR_APPROVAL=enable' to disable ByoAdmission controller. Now CSRs should be approved manually.
	if os.Getenv("MANUAL_CSR_APPROVAL") != "enable" {
		opts.csrClientSet = clientset.NewForConfigOrDie(ctrl.GetConfigOrDie())
	}

	if err := setupControllers(ctx, mgr, opts); err != nil {
		setupLog.Error(err, "unable to set up controllers")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
