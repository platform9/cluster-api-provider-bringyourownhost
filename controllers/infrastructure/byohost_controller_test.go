// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Controllers/ByohostController", func() {
	var (
		byoHost          *infrastructurev1beta1.ByoHost
		byoHostLookupKey types.NamespacedName
	)

	BeforeEach(func() {
		byoHost = builder.ByoHost(defaultNamespace, "byohost-controller-test").Build()
		Expect(k8sManager.GetClient().Create(context.Background(), byoHost)).To(Succeed())
		byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
	})

	// Other suites (e.g. Controllers/ByomachineController) list all unattached
	// ByoHosts in defaultNamespace and greedily pick the first one, so a ByoHost
	// left behind here can be attached by an unrelated test elsewhere in the
	// suite. Always clean up, matching the convention used everywhere else a
	// ByoHost is created in this package's tests.
	AfterEach(func() {
		Expect(k8sManager.GetClient().Delete(context.Background(), byoHost)).To(Succeed())
	})

	Context("uninstallation secret cleanup", func() {
		var uninstallSecret *corev1.Secret

		BeforeEach(func() {
			uninstallSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-uninstall-secret",
					Namespace: byoHost.Namespace,
				},
				Data: map[string][]byte{"uninstall": []byte("echo uninstall")},
			}
			Expect(k8sManager.GetClient().Create(context.Background(), uninstallSecret)).To(Succeed())

			helper, err := patch.NewHelper(byoHost, k8sManager.GetClient())
			Expect(err).NotTo(HaveOccurred())
			byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
				Name:      uninstallSecret.Name,
				Namespace: uninstallSecret.Namespace,
			}
			Expect(helper.Patch(context.Background(), byoHost)).To(Succeed())

			// The reconciler reads through the manager's cache, so wait for the
			// cache to observe this patch before invoking Reconcile below —
			// otherwise Reconcile may act on a stale (pre-patch) ByoHost, and its
			// later full status Update can then conflict with the resourceVersion
			// the direct API-server patch above already advanced to.
			WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
				return obj.(*infrastructurev1beta1.ByoHost).Spec.UninstallationSecret != nil
			})
		})

		It("deletes the uninstall secret and clears the reference when MachineRef is nil and no cleanup annotation is set", func() {
			_, err := byoHostReconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: byoHostLookupKey})
			Expect(err).NotTo(HaveOccurred())

			WaitForObjectToBeUpdatedInCache(byoHost, func(obj client.Object) bool {
				return obj.(*infrastructurev1beta1.ByoHost).Spec.UninstallationSecret == nil
			})

			err = k8sManager.GetClient().Get(context.Background(),
				types.NamespacedName{Name: uninstallSecret.Name, Namespace: uninstallSecret.Namespace}, &corev1.Secret{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})
