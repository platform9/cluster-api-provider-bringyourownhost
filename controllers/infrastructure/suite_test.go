// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"

	//+kubebuilder:scaffold:imports

	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

// Shared test fixtures used across this package's test files.
const (
	nonExistentNamespace           = "non-existent-namespace"
	pausedAnnotationValue          = "paused"
	k8sInstallerConfigTemplateKind = "K8sInstallerConfigTemplate"
	testOSNameLinux                = "linux"
)

var (
	testEnv                               *envtest.Environment
	clientFake                            client.Client
	clientSetFake                         = fakeclientset.NewSimpleClientset()
	reconciler                            *controllers.ByoMachineReconciler
	byoClusterReconciler                  *controllers.ByoClusterReconciler
	byoAdmissionReconciler                *controllers.ByoAdmissionReconciler
	k8sInstallerConfigReconciler          *controllers.K8sInstallerConfigReconciler
	bootstrapKubeconfigReconciler         *controllers.BootstrapKubeconfigReconciler
	byoHostReconciler                     *controllers.ByoHostReconciler
	byoHostAgentUpgradeReconciler         *controllers.ByoHostAgentUpgradeReconciler
	recorder                              *record.FakeRecorder
	byoCluster                            *infrastructurev1beta1.ByoCluster
	capiCluster                           *clusterv1.Cluster
	defaultClusterName                    = "my-cluster"
	defaultNodeName                       = "my-host"
	defaultByoHostName                    = "my-host"
	defaultMachineName                    = "my-machine"
	defaultByoMachineName                 = "my-byomachine"
	defaultK8sInstallerConfigName         = "my-k8sinstallerconfig"
	defaultK8sInstallerConfigTemplateName = "my-installer-template"
	defaultNamespace                      = "default"
	fakeBootstrapSecret                   = "fakeBootstrapSecret"
	k8sManager                            ctrl.Manager
	cfg                                   *rest.Config
	ctx                                   context.Context
	cancel                                context.CancelFunc
)

// setupReconcilers wires all controllers to k8sManager and waits for the cache
// to sync. Extracted from TestMain to keep statement count within funlen limits.
func setupReconcilers() {
	cl := k8sManager.GetClient()

	byoCluster = builder.ByoCluster(defaultNamespace, defaultClusterName).
		WithBundleBaseRegistry("projects.registry.vmware.com/cluster_api_provider_bringyourownhost").
		WithBundleTag("1.0").
		Build()
	if err := cl.Create(context.Background(), byoCluster); err != nil {
		panic(err)
	}

	capiCluster = builder.Cluster(defaultNamespace, defaultClusterName).WithInfrastructureRef(byoCluster).Build()
	if err := cl.Create(context.Background(), capiCluster); err != nil {
		panic(err)
	}

	node := builder.Node(defaultNamespace, defaultNodeName).Build()
	clientFake = fake.NewClientBuilder().WithObjects(capiCluster, node).Build()

	recorder = record.NewFakeRecorder(32)
	reconciler = &controllers.ByoMachineReconciler{
		Client:   cl,
		Tracker:  remote.NewTestClusterCacheTracker(logr.New(logf.NullLogSink{}), cl, clientFake, scheme.Scheme, client.ObjectKey{Name: capiCluster.Name, Namespace: capiCluster.Namespace}), //nolint: staticcheck // mirrors the deprecated-but-supported remote.ClusterCacheTracker used by ByoMachineReconciler.Tracker
		Recorder: recorder,
	}
	if err := reconciler.SetupWithManager(context.TODO(), k8sManager); err != nil {
		panic(err)
	}

	byoClusterReconciler = &controllers.ByoClusterReconciler{Client: cl}
	if err := byoClusterReconciler.SetupWithManager(context.TODO(), k8sManager); err != nil {
		panic(err)
	}

	byoAdmissionReconciler = &controllers.ByoAdmissionReconciler{ClientSet: clientSetFake}
	if err := byoAdmissionReconciler.SetupWithManager(k8sManager); err != nil {
		panic(err)
	}

	k8sInstallerConfigReconciler = &controllers.K8sInstallerConfigReconciler{Client: cl}
	if err := k8sInstallerConfigReconciler.SetupWithManager(k8sManager); err != nil {
		panic(err)
	}

	bootstrapKubeconfigReconciler = &controllers.BootstrapKubeconfigReconciler{Client: cl}
	if err := bootstrapKubeconfigReconciler.SetupWithManager(k8sManager); err != nil {
		panic(err)
	}

	// byoHostReconciler uses a direct (non-cached) client so tests can patch
	// objects and call Reconcile immediately without waiting for cache sync.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic(err)
	}
	byoHostReconciler = &controllers.ByoHostReconciler{
		Client: directClient,
		Scheme: scheme.Scheme,
		// Long enough that this controller's own safety-net RequeueAfter
		// never fires during a test run — heartbeat-specific tests in
		// byohost_controller_test.go set their own short value directly on
		// this struct where needed.
		HeartbeatTimeoutPeriod: 10 * time.Minute,
		Recorder:               record.NewFakeRecorder(32),
	}
	if err := byoHostReconciler.SetupWithManager(k8sManager); err != nil {
		panic(err)
	}

	// byoHostAgentUpgradeReconciler also uses the direct client, for the same
	// reason as byoHostReconciler above: tests call Reconcile directly and
	// need to see their own just-written state without waiting on cache sync.
	byoHostAgentUpgradeReconciler = &controllers.ByoHostAgentUpgradeReconciler{
		Client:   directClient,
		Scheme:   scheme.Scheme,
		Recorder: record.NewFakeRecorder(32),
	}
	if err := byoHostAgentUpgradeReconciler.SetupWithManager(k8sManager); err != nil {
		panic(err)
	}

	go func() {
		if err := k8sManager.GetCache().Start(ctx); err != nil {
			panic(err)
		}
	}()

	if !k8sManager.GetCache().WaitForCacheSync(context.TODO()) {
		panic("cache never synced")
	}
}

// TestMain bootstraps envtest once for the entire package, making k8sManager
// and all other package-level vars available to both Ginkgo tests (via TestAPIs)
// and Go-native TestXxx functions.
func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.10.10", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.10.10", "bootstrap", "kubeadm", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil || cfg == nil {
		panic("failed to start envtest: " + err.Error())
	}

	if err = infrastructurev1beta1.AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
	if err = clusterv1.AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
	if err = bootstrapv1.AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: ":6080"},
	})
	if err != nil {
		panic(err)
	}

	setupReconcilers()

	code := m.Run()

	cancel()
	if err = testEnv.Stop(); err != nil {
		panic(err)
	}

	os.Exit(code)
}

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{})
}

func WaitForObjectsToBePopulatedInCache(objects ...client.Object) {
	for _, object := range objects {
		objectCopy := object.DeepCopyObject().(client.Object)
		key := client.ObjectKeyFromObject(object)
		Eventually(func() (done bool) {
			if err := reconciler.Client.Get(context.TODO(), key, objectCopy); err != nil {
				return false
			}
			return true
		}).Should(BeTrue())
	}
}

// WaitForObjectToBeUpdatedInCache polls until the object in the manager's cache
// satisfies testObjectUpdatedFunc. It uses a plain polling loop so it works in
// both Ginkgo tests and Go-native TestXxx functions (Gomega's Eventually
// requires RegisterFailHandler which is only set up inside TestAPIs).
func WaitForObjectToBeUpdatedInCache(object client.Object, testObjectUpdatedFunc func(client.Object) bool) {
	objectCopy := object.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(object)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := reconciler.Get(context.TODO(), key, objectCopy); err == nil {
			if testObjectUpdatedFunc(objectCopy) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	panic("WaitForObjectToBeUpdatedInCache: condition not met within 10s for " + key.String())
}
