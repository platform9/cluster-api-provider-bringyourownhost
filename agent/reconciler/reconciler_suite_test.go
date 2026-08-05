// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"go/build"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var (
	cfg                *rest.Config
	k8sClient          client.Client
	k8sManager         manager.Manager
	patchHelper        *patch.Helper
	hostReconciler     *reconciler.HostReconciler
	testEnv            *envtest.Environment
	fakeCommandRunner  *cloudinitfakes.FakeICmdRunner
	fakeFileWriter     *cloudinitfakes.FakeIFileWriter
	fakeTemplateParser *cloudinitfakes.FakeITemplateParser
)

// TestMain bootstraps envtest once for the entire package, making k8sClient
// and other package-level vars available to both Ginkgo tests (via TestReconciler)
// and Go-native TestXxx functions.
func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api@v1.4.4", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil || cfg == nil {
		panic("failed to start envtest: " + err.Error())
	}

	scheme := runtime.NewScheme()
	if err = infrastructurev1beta1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err = corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err = clusterv1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil || k8sClient == nil {
		panic("failed to create k8sClient")
	}

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: ":6090",
	})
	if err != nil || k8sManager == nil {
		panic("failed to create k8sManager")
	}

	code := m.Run()

	if err = testEnv.Stop(); err != nil {
		panic(err)
	}

	os.Exit(code)
}

func TestReconciler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Reconciler Suite")
}
