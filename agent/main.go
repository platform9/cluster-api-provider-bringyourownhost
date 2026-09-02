// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-logr/logr"
	pflag "github.com/spf13/pflag"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/feature"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	// DefaultHeartbeatInterval is how often the agent refreshes its ByoHost
	// heartbeat timestamp, unless overridden via the --heartbeat-interval flag.
	DefaultHeartbeatInterval = 30 * time.Second

	// DefaultMaxBlockingDuration bounds how long the agent keeps pulsing
	// heartbeats during a single kubeadm install/join call, unless overridden
	// via the --max-blocking-duration flag. Comfortably above any legitimate
	// install or join duration, so it only ever kicks in for a genuinely
	// wedged call.
	DefaultMaxBlockingDuration = 30 * time.Minute

	// DefaultExpirationSeconds defines the expiry time for Certificates
	// which is currently set to 1 year aligned with kubeadm defaults.
	DefaultExpirationSeconds = 86400 * 365

	// bootstrapRetryInitialDelay is how long the agent waits before its first retry of the
	// bootstrap flow, and the value the delay doubles from after that. Lowering it recovers sooner
	// once an operator fixes the cause, at the cost of more requests against the management cluster
	// while a host is stuck.
	bootstrapRetryInitialDelay = 5 * time.Second
	// bootstrapRetryMaxDelay defines how much the retry delay is allowed to double. For example,
	// the agent does not have permissions to delete its own certificate requests, so if a host's
	// requests keep getting denied, it leaves one CSR per retry. Raising this cap slows the
	// accumulation of such resources but it slows recovery by the same amount.
	bootstrapRetryMaxDelay = 30 * time.Minute

	// csrApprovalTimeout bounds how long the agent waits for its certificate
	// request to be approved and issued before giving up on this attempt. The
	// bootstrap flow retries, so a timeout here costs a retry rather than the
	// agent's whole run.
	csrApprovalTimeout = 5 * time.Minute
)

var (
	namespace           string
	scheme              *runtime.Scheme
	labels              = make(labelFlags)
	metricsbindaddress  string
	downloadpath        string
	skipInstallation    bool
	printVersion        bool
	bootstrapKubeConfig string
	certExpiryDuration  int64
	heartbeatInterval   time.Duration
	maxBlockingDuration time.Duration
)

// labelFlags is a flag that holds a map of label key values.
// One or more key value pairs can be passed using the same flag
// The following example sets labelFlags with two items:
//
//	-label "key1=value1" -label "key2=value2"
type labelFlags map[string]string

// String implements flag.Value interface
func (l *labelFlags) String() string {
	var result []string
	for key, value := range *l {
		result = append(result, fmt.Sprintf("%s=%s", key, value))
	}
	return strings.Join(result, ",")
}

// Set implements flag.Value interface
// nolint: mnd
func (l *labelFlags) Set(value string) error {
	// account for comma-separated key-value pairs in a single invocation
	if len(strings.Split(value, ",")) > 1 {
		for _, s := range strings.Split(value, ",") {
			if s == "" {
				continue
			}
			parts := strings.SplitN(s, "=", 2)
			if len(parts) < 2 {
				return fmt.Errorf("invalid argument value. expect key=value, got %s", value)
			}
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			(*l)[k] = v
		}
		return nil
	} else {
		// account for only one key-value pair in a single invocation
		parts := strings.SplitN(value, "=", 2)
		if len(parts) < 2 {
			return fmt.Errorf("invalid argument value. expect key=value, got %s", value)
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		(*l)[k] = v
		return nil
	}
}

// agentOptions is the flag-derived configuration the manager and the host
// reconciler need.
type agentOptions struct {
	hostName            string
	namespace           string
	metricsBindAddress  string
	downloadPath        string
	skipInstallation    bool
	heartbeatInterval   time.Duration
	maxBlockingDuration time.Duration

	// exit terminates the process after a successful agent upgrade install,
	// so the process supervisor (systemd's Restart=always) relaunches from
	// the same fixed ExecStart path, which by then points at the newly
	// installed binary. See docs/proposals/agent-self-upgrade-adr.md §2.2
	// step 5 for why this is preferred over syscall.Exec.
	exit func()
}

func setupflags() {
	klog.InitFlags(nil)
	// clear any discard loggers set by dependecies
	klog.ClearLogger()

	flag.StringVar(&namespace, "namespace", "default", "Namespace in the management cluster where you would like to register this host")
	flag.Int64Var(&certExpiryDuration, "certExpiryDuration", DefaultExpirationSeconds, "Duration (in seconds) for the expiration of the host certificates")
	flag.Var(&labels, "label", "labels to attach to the ByoHost CR in the form labelname=labelVal for e.g. '--label site=apac --label cores=2'")
	flag.StringVar(&metricsbindaddress, "metricsbindaddress", ":8080", "metricsbindaddress is the TCP address that the controller should bind to for serving prometheus metrics.It can be set to \"0\" to disable the metrics serving")
	flag.StringVar(&downloadpath, "downloadpath", "/var/lib/byoh/bundles", "File System path to keep the downloads")
	flag.BoolVar(&skipInstallation, "skip-installation", false, "If you want to skip installation of the kubernetes component binaries")
	flag.BoolVar(&printVersion, "version", false, "Print the version of the agent")
	flag.StringVar(&bootstrapKubeConfig, "bootstrap-kubeconfig", "", "Provide bootstrap kubeconfig for bootstrap token workflow")
	flag.DurationVar(&heartbeatInterval, "heartbeat-interval", DefaultHeartbeatInterval, "How often the agent refreshes its ByoHost heartbeat timestamp")
	flag.DurationVar(&maxBlockingDuration, "max-blocking-duration", DefaultMaxBlockingDuration, "How long the agent keeps pulsing heartbeats during a single kubeadm install/join call before giving up")

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	hiddenFlags := []string{"log-flush-frequency", "alsologtostderr", "log-backtrace-at", "log-dir", "logtostderr", "stderrthreshold", "vmodule", "azure-container-registry-config",
		"log_backtrace_at", "log_dir", "log_file", "log_file_max_size", "add_dir_header", "skip_headers", "skip_log_headers", "one_output", "kubeconfig",
		"alsologtostderrthreshold", "legacy_stderr_threshold_behavior"}
	for _, hiddenFlag := range hiddenFlags {
		_ = pflag.CommandLine.MarkHidden(hiddenFlag)
	}
	feature.MutableGates.AddFlag(pflag.CommandLine)
}

// newScheme returns the scheme covering every type the agent reads or writes.
// A registration failure means a type was built wrong, which no caller can
// recover from, so it panics rather than reporting.
func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(infrastructurev1beta1.AddToScheme(s))
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(clusterv1.AddToScheme(s))
	utilruntime.Must(certv1.AddToScheme(s))

	return s
}

func setupTemplateParser() *cloudinit.TemplateParser {
	var templateParser *cloudinit.TemplateParser
	if registration.LocalHostRegistrar.ByoHostInfo.DefaultNetworkInterfaceName == "" {
		templateParser = nil
	} else {
		templateParser = &cloudinit.TemplateParser{
			Template: registration.HostInfo{
				DefaultNetworkInterfaceName: registration.LocalHostRegistrar.ByoHostInfo.DefaultNetworkInterfaceName,
			},
		}
	}
	return templateParser
}

// newManager builds the agent's controller manager. Its cache is scoped to
// this host's namespace, and within that namespace to this host's own ByoHost,
// so an agent never caches objects belonging to other hosts.
func newManager(config *rest.Config, opts *agentOptions) (ctrl.Manager, error) {
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{opts.namespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&infrastructurev1beta1.ByoHost{}: {
					Field: fields.SelectorFromSet(fields.Set{"metadata.name": opts.hostName}),
				},
			},
		},
		Metrics: metricsserver.Options{BindAddress: opts.metricsBindAddress},
	})
	if err != nil {
		return nil, fmt.Errorf("create manager: %w", err)
	}

	return mgr, nil
}

// setupHostReconciler registers the host reconciler with mgr. ctx must be the
// context that also stops mgr: the reconciler's event filter captures it, so
// one with a different lifetime leaves that filter running past the manager,
// or dead before it.
func setupHostReconciler(ctx context.Context, mgr ctrl.Manager, k8sClient client.Client, opts *agentOptions) error {
	hostReconciler := &reconciler.HostReconciler{
		Client:              k8sClient,
		CmdRunner:           cloudinit.CmdRunner{},
		FileWriter:          cloudinit.FileWriter{},
		TemplateParser:      setupTemplateParser(),
		LookPath:            exec.LookPath,
		PackagePull:         cloudinit.Pull,
		Exit:                opts.exit,
		Recorder:            mgr.GetEventRecorderFor("hostagent-controller"),
		SkipK8sInstallation: opts.skipInstallation,
		DownloadPath:        opts.downloadPath,
		HeartbeatInterval:   opts.heartbeatInterval,
		MaxBlockingDuration: opts.maxBlockingDuration,
	}

	if err := hostReconciler.SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("create host reconciler: %w", err)
	}

	return nil
}

func handleBootstrapFlow(ctx context.Context, logger logr.Logger, hostName string) error {
	logger.Info("initiated bootstrap kubeconfig flow")
	bootstrapClientConfig, err := registration.LoadRESTClientConfig(bootstrapKubeConfig)
	if err != nil {
		return fmt.Errorf("client config load failed: %v", err)
	}
	byohCSR, err := registration.NewByohCSR(bootstrapClientConfig, logger, certExpiryDuration)
	if err != nil {
		return fmt.Errorf("ByohCSR intialization failed: %v", err)
	}
	err = byohCSR.BootstrapKubeconfig(ctx, hostName, csrApprovalTimeout)
	if err != nil {
		return fmt.Errorf("kubeconfig generation failed: %v", err)
	}
	return nil
}

// retryWithBackoff calls attempt until it succeeds, waiting longer after
// each failure up to maxDelay. It gives up only when ctx is done, and
// returns that context's error when it does.
func retryWithBackoff(ctx context.Context, logger logr.Logger, initialDelay, maxDelay time.Duration, attemptFn func() error) error {
	delay := initialDelay
	attemptCount := 0
	for {
		attemptCount += 1
		err := attemptFn()
		if err == nil {
			return nil
		}
		logger.Error(err, "attempt failed, retrying", "attempt", attemptCount, "retryIn", delay)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		delay = min(2*delay, maxDelay)
	}
}

func certificateRotation(ctx context.Context, logger logr.Logger, hostName string, config *rest.Config) error {
	var pollDuration = 5 * time.Second
	for {
		if err := certRotation(ctx, logger, hostName, config); err != nil {
			return err
		}
		// Poll after every few seconds
		time.Sleep(pollDuration)
	}
}

func certRotation(ctx context.Context, logger logr.Logger, hostName string, config *rest.Config) error {
	block, _ := pem.Decode(config.CertData)
	if block == nil || block.Type != "CERTIFICATE" {
		logger.Info("failed to decode PEM block containing certificate")
		return nil
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		logger.Error(err, "Certificate parse failed")
		return err
	}

	totalTimeCert := cert.NotAfter.Sub(cert.NotBefore)

	// if less than 20% time left, renew the certs.
	// https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20210222-kubelet-authentication.md#kubelet-authenticator-flow
	if time.Now().After(cert.NotAfter.Add(totalTimeCert / -5)) {
		logger.Info("certificate expiration time left is less than 20%, renewing")
		if err = handleBootstrapFlow(ctx, logger, hostName); err != nil {
			logger.Error(err, "bootstrap flow failed")
		}
	} else {
		logger.Info("certificate are valid", "will be renewed after", cert.NotAfter.Add(totalTimeCert/-5))
	}
	return nil
}

// getConfig loads the kubeconfig the agent runs with.
//
// NOTE: An invalid kubeconfig is not a retryable fix and will terminate the process instead of
// returning an error.
func getConfig(logger logr.Logger) *rest.Config {
	config, err := registration.LoadRESTClientConfig(registration.GetBYOHConfigPath())
	if err != nil {
		logger.Error(err, "client config load failed")
		os.Exit(1)
	}
	return config
}

// getClient builds the management cluster client.
//
// NOTE: A failed attempt at building the client implies an invalid config and is not a retryable
// fix. As a result it will terminates the process.
func getClient(logger logr.Logger, config *rest.Config) client.Client {
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "k8s client creation failed")
		os.Exit(1)
	}

	return k8sClient
}

// TODO - fix logging
func main() {
	setupflags()
	pflag.Parse()
	if printVersion {
		info := version.Get()
		fmt.Printf("byoh-hostagent version: %#v\n", info)
		return
	}
	scheme = newScheme()

	// klogr predates the textlogger migration; swapping formats is a separate, deliberate
	// change outside this dependency bump's scope (see main.go for the same rationale).
	logger := klogr.New() //nolint: staticcheck
	ctrl.SetLogger(logger)

	// SetupSignalHandler panics on a second call, so one context is built
	// here for both the reconciler registration and mgr.Start.
	ctx := ctrl.SetupSignalHandler()

	hostName, err := os.Hostname()
	if err != nil {
		logger.Error(err, "could not determine hostname")
		return
	}

	_, err = os.Stat(registration.GetBYOHConfigPath())
	// Enable bootstrap flow if --bootstrap-kubeconfig is provided
	// and config doesn't already exists in ~/.byoh/
	if bootstrapKubeConfig != "" && errors.Is(err, os.ErrNotExist) {
		// Instead of failing hard and exiting on errors during the bootstrap steps, we should retry
		// with a backoff. There should be very few ways where we exit because of a failure, because
		// it burns through the systemd start limit and leaves the agent in a state where it is not
		// running on the host at all.
		bootstrapErr := retryWithBackoff(ctx, logger, bootstrapRetryInitialDelay, bootstrapRetryMaxDelay, func() error {
			return handleBootstrapFlow(ctx, logger, hostName)
		})

		// An error here means either we received a shutdown signal or we failed to bootstrap the host within bootstrapRetryMaxDelay.
		if bootstrapErr != nil {
			logger.Error(bootstrapErr, "bootstrap flow abandoned")
			return
		}
	}
	// Either the agent was restarted or bootstrap was successful during first time the agent was
	// started. Either way we should have ~/.byoh/config available on the host now.
	config := getConfig(logger)
	k8sClient := getClient(logger, config)
	registration.LocalHostRegistrar = &registration.HostRegistrar{K8sClient: k8sClient}
	err = registration.LocalHostRegistrar.Register(ctx, hostName, namespace, labels)
	if err != nil {
		logger.Error(err, "error registering host %s registration in namespace %s", hostName, namespace)
		return
	}

	// Start certificate rotation goroutine.
	// This is behind a feature flag for now. Set 'CERTIFICATE_ROTATION=true' to enable it.
	// FIXME: This needs to be auto detected and not behind a feature flag.
	if os.Getenv("CERTIFICATE_ROTATION") == "true" {
		go func() {
			err = certificateRotation(ctx, logger, hostName, config)
			if err != nil {
				logger.Error(err, "certificate rotation failed")
				return
			}
		}()
	}

	opts := &agentOptions{
		hostName:            hostName,
		namespace:           namespace,
		metricsBindAddress:  metricsbindaddress,
		downloadPath:        downloadpath,
		skipInstallation:    skipInstallation,
		heartbeatInterval:   heartbeatInterval,
		maxBlockingDuration: maxBlockingDuration,
		exit:                func() { os.Exit(0) },
	}

	mgr, err := newManager(config, opts)
	if err != nil {
		logger.Error(err, "unable to start manager")
		return
	}

	if skipInstallation {
		logger.Info("skip-installation flag set, skipping installer initialisation")
	}

	if err := setupHostReconciler(ctx, mgr, k8sClient, opts); err != nil {
		logger.Error(err, "unable to create controller")
		return
	}

	if err := mgr.Start(ctx); err != nil {
		logger.Error(err, "problem running manager")
		return
	}
}
