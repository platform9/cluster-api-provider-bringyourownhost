// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	controllers "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/controllers/infrastructure"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	eventutils "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	warningNoAvailableByoHostEvent = "Warning ByoHostSelectionFailed No available ByoHost"
	testMapFuncClusterName         = "cluster-a"
)

var _ = Describe("Controllers/ByomachineController", func() {
	var (
		byoMachineLookupKey        types.NamespacedName
		byoHostLookupKey           types.NamespacedName
		byoMachine                 *infrastructurev1beta1.ByoMachine
		machine                    *clusterv1.Machine
		node                       *corev1.Node
		k8sClientUncached          client.Client
		k8sInstallerConfigTemplate *infrastructurev1beta1.K8sInstallerConfigTemplate
		k8sInstallerConfig         *infrastructurev1beta1.K8sInstallerConfig
		byoHost                    *infrastructurev1beta1.ByoHost
		testClusterVersion         = "v1.22.1_xyz"
	)

	BeforeEach(func() {
		ctx = context.Background()

		var clientErr error
		k8sClientUncached, clientErr = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(clientErr).NotTo(HaveOccurred())

		machine = builder.Machine(defaultNamespace, defaultMachineName).
			WithClusterName(defaultClusterName).
			WithClusterVersion(testClusterVersion).
			WithBootstrapDataSecret(fakeBootstrapSecret).
			Build()
		Expect(k8sClientUncached.Create(ctx, machine)).Should(Succeed())

		byoMachine = builder.ByoMachine(defaultNamespace, defaultByoMachineName).
			WithClusterLabel(defaultClusterName).
			WithOwnerMachine(machine).
			Build()
		Expect(k8sClientUncached.Create(ctx, byoMachine)).Should(Succeed())

		WaitForObjectsToBePopulatedInCache(ctx, machine, byoMachine)
		byoMachineLookupKey = types.NamespacedName{Name: byoMachine.Name, Namespace: byoMachine.Namespace}

		k8sInstallerConfigTemplate = builder.K8sInstallerConfigTemplate(defaultNamespace, defaultK8sInstallerConfigTemplateName).
			WithBundleRepo("projects.registry.vmware.com/cluster_api_provider_bringyourownhost").
			WithBundleType("k8s").
			Build()
		Expect(k8sClientUncached.Create(ctx, k8sInstallerConfigTemplate)).Should(Succeed())
		WaitForObjectsToBePopulatedInCache(ctx, k8sInstallerConfigTemplate)
	})

	AfterEach(func() {
		eventutils.DrainEvents(recorder.Events)
	})

	It("should ignore byomachine if it is not found", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-byomachine",
				Namespace: nonExistentNamespace}})
		Expect(err).NotTo(HaveOccurred())
	})

	It("should return error when cluster does not exist", func() {
		machineForByoMachineWithoutCluster := builder.Machine(defaultNamespace, "machine-for-a-byomachine-without-cluster").
			WithClusterName(defaultClusterName).
			Build()
		Expect(k8sClientUncached.Create(ctx, machineForByoMachineWithoutCluster)).Should(Succeed())

		byoMachineWithNonExistingCluster := builder.ByoMachine(defaultNamespace, defaultByoMachineName).
			WithClusterLabel("non-existent-cluster").
			WithOwnerMachine(machine).
			Build()
		Expect(k8sClientUncached.Create(ctx, byoMachineWithNonExistingCluster)).Should(Succeed())

		WaitForObjectsToBePopulatedInCache(ctx, machineForByoMachineWithoutCluster, byoMachineWithNonExistingCluster)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      byoMachineWithNonExistingCluster.Name,
				Namespace: byoMachineWithNonExistingCluster.Namespace}})
		Expect(err).To(MatchError("failed to get Cluster/non-existent-cluster: Cluster.cluster.x-k8s.io \"non-existent-cluster\" not found"))
	})

	Context("When cluster infrastructure is ready", func() {
		BeforeEach(func() {
			ph, err := patch.NewHelper(capiCluster, k8sClientUncached)
			Expect(err).ShouldNot(HaveOccurred())
			capiCluster.Status.InfrastructureReady = true
			Expect(ph.Patch(ctx, capiCluster, patch.WithStatusObservedGeneration{})).Should(Succeed())

			WaitForObjectToBeUpdatedInCache(ctx, capiCluster, func(object client.Object) bool {
				return object.(*clusterv1.Cluster).Status.InfrastructureReady == true
			})
		})

		It("should return error when node is not available", func() {
			byoHost = builder.ByoHost(defaultNamespace, "host-with-node-missing").Build()
			Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

			WaitForObjectsToBePopulatedInCache(ctx, byoHost)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
			Expect(err).To(MatchError("nodes \"" + byoHost.Name + "\" not found"))
		})

		Context("When node.Spec.ProviderID is already set", func() {

			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "test-node-providerid-host").Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())
				WaitForObjectsToBePopulatedInCache(ctx, byoHost)
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should not return error when node.Spec.ProviderID is with correct value", func() {
				node = builder.Node(defaultNamespace, byoHost.Name).
					WithProviderID(fmt.Sprintf("%s%s/%s", controllers.ProviderIDPrefix, byoHost.Name, util.RandomString(controllers.ProviderIDSuffixLength))).
					Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())
			})

			It("should return error when node.Spec.ProviderID has stale value", func() {
				node = builder.Node(defaultNamespace, byoHost.Name).
					WithProviderID(fmt.Sprintf("%sanother-host/%s", controllers.ProviderIDPrefix, util.RandomString(controllers.ProviderIDSuffixLength))).
					Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(MatchError("invalid format for node.Spec.ProviderID"))
			})
		})

		Context("When BYO Hosts are not available", func() {
			It("should mark BYOHostReady as False", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(MatchError("no hosts found"))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)

				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					warningNoAvailableByoHostEvent,
				}))
			})

			It("should add MachineFinalizer on ByoMachine", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(HaveOccurred())

				updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(controllerutil.ContainsFinalizer(updatedByoMachine, infrastructurev1beta1.MachineFinalizer)).To(BeTrue())
			})

			It("should be able to delete ByoMachine", func() {
				ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				controllerutil.AddFinalizer(byoMachine, infrastructurev1beta1.MachineFinalizer)
				Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())

				Expect(k8sClientUncached.Delete(ctx, byoMachine)).Should(Succeed())
				WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
					return !object.(*infrastructurev1beta1.ByoMachine).DeletionTimestamp.IsZero()
				})
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(0))

				// assert ByoMachine does not exists
				deletedByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, deletedByoMachine)
				Expect(err).To(MatchError(fmt.Sprintf("byomachines.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))

			})
		})

		Context("When a single BYO Host is available", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "single-available-default-host").Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				node = builder.Node(defaultNamespace, byoHost.Name).Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				WaitForObjectsToBePopulatedInCache(ctx, byoHost)

				byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("claims the first available host", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoHost := &infrastructurev1beta1.ByoHost{}
				err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoHost.Status.MachineRef.Namespace).To(Equal(byoMachine.Namespace))
				Expect(createdByoHost.Status.MachineRef.Name).To(Equal(byoMachine.Name))

				// Assert labels on byohost
				createdByoHostLabels := createdByoHost.GetLabels()
				Expect(createdByoHostLabels[clusterv1.ClusterNameLabel]).To(Equal(capiCluster.Name))

				createdByoHostAnnotations := createdByoHost.GetAnnotations()
				Expect(createdByoHostAnnotations[infrastructurev1beta1.K8sVersionAnnotation]).To(Equal(strings.Split(testClusterVersion, "+")[0]))
				Expect(createdByoHostAnnotations[infrastructurev1beta1.BundleLookupBaseRegistryAnnotation]).To(Equal(byoCluster.Spec.BundleLookupBaseRegistry))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoMachine.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					fmt.Sprintf("Normal ByoHostAttachSucceeded Attached to ByoMachine %s", createdByoMachine.Name),
					fmt.Sprintf("Normal NodeProvisionedSucceeded Provisioned Node %s", createdByoHost.Name),
					fmt.Sprintf("Normal ByoHostAttachSucceeded Attached ByoHost %s", createdByoHost.Name),
				}))

				node := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost.Name, Namespace: defaultNamespace}, &node)
				Expect(err).NotTo(HaveOccurred())

				Expect(node.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
			})

			Context("When ByoMachine is attached to a host", func() {
				BeforeEach(func() {
					ph, err := patch.NewHelper(byoHost, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					byoHost.Status.MachineRef = &corev1.ObjectReference{
						Kind:       "ByoMachine",
						Namespace:  byoMachine.Namespace,
						Name:       byoMachine.Name,
						UID:        byoMachine.UID,
						APIVersion: byoHost.APIVersion,
					}
					if byoHost.Labels == nil {
						byoHost.Labels = make(map[string]string)
					}
					byoHost.Labels[infrastructurev1beta1.AttachedByoMachineLabel] = byoMachine.Namespace + "." + byoMachine.Name
					Expect(ph.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).Should(Succeed())

					WaitForObjectToBeUpdatedInCache(ctx, byoHost, func(object client.Object) bool {
						return object.(*infrastructurev1beta1.ByoHost).Status.MachineRef != nil
					})
				})

				It("should mark host as paused when the ByoMachine is paused", func() {
					ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					pauseAnnotations := map[string]string{
						clusterv1.PausedAnnotation: pausedAnnotationValue,
					}
					annotations.AddAnnotations(byoMachine, pauseAnnotations)
					Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())
					WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
						return annotations.HasPaused(object.(*infrastructurev1beta1.ByoMachine))
					})

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())

					createdByoHost := &infrastructurev1beta1.ByoHost{}
					err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdByoHost.Annotations).To(HaveKey(clusterv1.PausedAnnotation))
				})

				It("should set paused status of byohost to false when byomachine is not paused", func() {

					ph, err := patch.NewHelper(byoHost, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					pauseAnnotations := map[string]string{
						clusterv1.PausedAnnotation: "",
					}

					annotations.AddAnnotations(byoHost, pauseAnnotations)
					Expect(ph.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).Should(Succeed())
					WaitForObjectToBeUpdatedInCache(ctx, byoHost, func(object client.Object) bool {
						return annotations.HasPaused(object.(*infrastructurev1beta1.ByoHost))
					})
					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())
					createdByoHost := &infrastructurev1beta1.ByoHost{}
					err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdByoHost.Annotations).NotTo(HaveKey(clusterv1.PausedAnnotation))

				})

				It("should set host platform info from byohost to byomachine", func() {
					ph, err := patch.NewHelper(byoHost, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					byoHost.Status.HostDetails = infrastructurev1beta1.HostInfo{
						OSName:       testOSNameLinux,
						OSImage:      "Ubuntu 20.04.4 LTS",
						Architecture: "arm64",
					}
					if byoHost.Labels == nil {
						byoHost.Labels = make(map[string]string)
					}
					byoHost.Labels[infrastructurev1beta1.AttachedByoMachineLabel] = byoMachine.Namespace + "." + byoMachine.Name
					Expect(ph.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).Should(Succeed())

					WaitForObjectToBeUpdatedInCache(ctx, byoHost, func(object client.Object) bool {
						return object.(*infrastructurev1beta1.ByoHost).Status.HostDetails == infrastructurev1beta1.HostInfo{
							OSName:       testOSNameLinux,
							OSImage:      "Ubuntu 20.04.4 LTS",
							Architecture: "arm64",
						}
					})
					WaitForObjectToBeUpdatedInCache(ctx, byoHost, func(object client.Object) bool {
						return object.(*infrastructurev1beta1.ByoHost).Status.MachineRef != nil
					})

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())
					patchedByoMachine := &infrastructurev1beta1.ByoMachine{}
					err = k8sClientUncached.Get(ctx, byoMachineLookupKey, patchedByoMachine)
					Expect(err).ToNot(HaveOccurred())
					Expect(patchedByoMachine.Status.HostInfo).To(Equal(byoHost.Status.HostDetails))

				})

				Context("When ByoMachine is deleted", func() {
					BeforeEach(func() {
						ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						controllerutil.AddFinalizer(byoMachine, infrastructurev1beta1.MachineFinalizer)
						Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())

						Expect(k8sClientUncached.Delete(ctx, byoMachine)).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
							return !object.(*infrastructurev1beta1.ByoMachine).DeletionTimestamp.IsZero()
						})
					})

					It("should add cleanup annotation on byohost so that the host agent can cleanup", func() {
						_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						createdByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)).NotTo(HaveOccurred())

						Expect(createdByoHost.Annotations[infrastructurev1beta1.HostCleanupAnnotation]).Should(Equal(""))
					})

					It("should delete the byomachine object", func() {
						deletedByoMachine := &infrastructurev1beta1.ByoMachine{}
						// assert ByoMachine Exists before reconcile
						Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, deletedByoMachine)).Should(Not(HaveOccurred()))
						_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							fmt.Sprintf("Normal ByoHostReleaseSucceeded Released ByoHost %s", byoHost.Name),
							fmt.Sprintf("Normal ByoHostReleaseSucceeded ByoHost Released by %s", deletedByoMachine.Name),
						}))

						// assert ByoMachine does not exists
						err = k8sClientUncached.Get(ctx, byoMachineLookupKey, deletedByoMachine)
						Expect(err).To(MatchError(fmt.Sprintf("byomachines.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))
					})
				})

				Context("When installer config exists", func() {
					BeforeEach(func() {
						k8sInstallerConfig = builder.K8sInstallerConfig(defaultNamespace, "").
							WithName(byoMachine.Name).
							WithBundleRepo("projects.registry.vmware.com/cluster_api_provider_bringyourownhost").
							WithBundleType("k8s").
							Build()
						Expect(k8sClientUncached.Create(ctx, k8sInstallerConfig)).Should(Succeed())
						WaitForObjectsToBePopulatedInCache(ctx, k8sInstallerConfig)

						ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
							Kind:       k8sInstallerConfigTemplateKind,
							Namespace:  defaultNamespace,
							Name:       defaultK8sInstallerConfigTemplateName,
							APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
						}
						Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
							return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
						})
					})

					It("should requeue the request if installer config is not ready", func() {
						res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())
						Expect(res.RequeueAfter).To(Equal(controllers.RequeueInstallerConfigTime))
					})

					It("should patch byohost if installer config is ready", func() {
						ph, err := patch.NewHelper(k8sInstallerConfig, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						k8sInstallerConfig.Status = infrastructurev1beta1.K8sInstallerConfigStatus{
							Ready: true,
							InstallationSecret: &corev1.ObjectReference{
								Kind:       "Secret",
								Namespace:  defaultNamespace,
								Name:       "K8sInstallationSecret",
								APIVersion: "v1",
							},
							UninstallationSecret: &corev1.ObjectReference{
								Kind:       "Secret",
								Namespace:  defaultNamespace,
								Name:       "K8sUninstallationSecret",
								APIVersion: "v1",
							},
						}
						Expect(ph.Patch(ctx, k8sInstallerConfig, patch.WithStatusObservedGeneration{})).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(ctx, k8sInstallerConfig, func(object client.Object) bool {
							return object.(*infrastructurev1beta1.K8sInstallerConfig).Status.Ready == true
						})

						_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						patchedHost := &infrastructurev1beta1.ByoHost{}
						err = k8sClientUncached.Get(ctx, byoHostLookupKey, patchedHost)
						Expect(err).ShouldNot(HaveOccurred())

						Expect(k8sInstallerConfig.Status.InstallationSecret).To(Equal(patchedHost.Spec.InstallationSecret))
					})

					AfterEach(func() {
						Expect(k8sClientUncached.Delete(ctx, k8sInstallerConfig)).Should(Succeed())
					})
				})

			})

			It("should mark BYOHostReady as False when byomachine is paused", func() {
				ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())

				pauseAnnotations := map[string]string{
					clusterv1.PausedAnnotation: pausedAnnotationValue,
				}
				annotations.AddAnnotations(byoMachine, pauseAnnotations)

				Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
					return annotations.HasPaused(object.(*infrastructurev1beta1.ByoMachine))
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.ClusterOrResourcePausedReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})

			It("should mark BYOHostReady as False when cluster is paused", func() {
				pausedCluster := builder.Cluster(defaultNamespace, "paused-cluster").
					WithPausedField(true).
					WithInfrastructureRef(byoCluster).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedCluster)).Should(Succeed())

				pausedMachine := builder.Machine(defaultNamespace, "paused-machine").
					WithClusterName(pausedCluster.Name).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedMachine)).Should(Succeed())

				pausedByoMachine := builder.ByoMachine(defaultNamespace, "paused-byo-machine").
					WithClusterLabel(pausedCluster.Name).
					WithOwnerMachine(pausedMachine).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedByoMachine)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(ctx, pausedCluster, pausedMachine, pausedByoMachine)

				pausedByoMachineLookupKey := types.NamespacedName{Name: pausedByoMachine.Name, Namespace: pausedByoMachine.Namespace}

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: pausedByoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, pausedByoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.ClusterOrResourcePausedReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))

				Expect(k8sClientUncached.Delete(ctx, pausedCluster)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, pausedMachine)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, pausedByoMachine)).Should(Succeed())
			})

			It("should mark BYOHostReady as False when machine.Spec.Bootstrap.DataSecretName is not set", func() {
				ph, err := patch.NewHelper(machine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())

				machine.Spec.Bootstrap = clusterv1.Bootstrap{DataSecretName: nil}
				Expect(ph.Patch(ctx, machine, patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(ctx, machine, func(object client.Object) bool {
					return object.(*clusterv1.Machine).Spec.Bootstrap.DataSecretName == nil
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(BeNil())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ShouldNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.WaitingForBootstrapDataSecretReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})

			It("should mark BYOHostReady condition as False when the InstallationSecret is not available", func() {
				// making the node unavailable by deleting it so that the reason persists
				Expect(clientFake.Delete(ctx, node)).Should(Succeed())

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).Should(MatchError(fmt.Sprintf("nodes %q not found", byoHost.Name)))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ShouldNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.InstallationSecretNotAvailableReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})
		})

		Context("When no matching BYO Hosts are available", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "byohost-with-different-label").
					WithLabels(map[string]string{"CPUs": "2"}).
					Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				byoMachine = builder.ByoMachine(defaultNamespace, "byomachine-with-label-selector").
					WithClusterLabel(defaultClusterName).
					WithOwnerMachine(machine).
					WithLabelSelector(map[string]string{"CPUs": "4"}).
					Build()
				Expect(k8sClientUncached.Create(ctx, byoMachine)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(ctx, byoHost, byoMachine)
				byoMachineLookupKey = types.NamespacedName{Name: byoMachine.Name, Namespace: byoMachine.Namespace}
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should mark BYOHostReady as False when BYOHosts is available but label mismatch", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(MatchError("no hosts found"))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					warningNoAvailableByoHostEvent,
				}))
			})
		})

		Context("When all ByoHost are attached", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "byohost-attached-different-cluster").
					WithLabels(map[string]string{clusterv1.ClusterNameLabel: capiCluster.Name}).
					Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(ctx, byoHost)
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should mark BYOHostReady as False", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(MatchError("no hosts found"))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					warningNoAvailableByoHostEvent,
				}))
			})
		})

		Context("When multiple BYO Host are available", func() {
			var (
				byoHost1 *infrastructurev1beta1.ByoHost
				byoHost2 *infrastructurev1beta1.ByoHost
			)

			BeforeEach(func() {
				byoHost1 = builder.ByoHost(defaultNamespace, defaultByoHostName).Build()
				Expect(k8sClientUncached.Create(ctx, byoHost1)).Should(Succeed())
				byoHost2 = builder.ByoHost(defaultNamespace, defaultByoHostName).Build()
				Expect(k8sClientUncached.Create(ctx, byoHost2)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(ctx, byoHost1, byoHost2)

				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost1.Name).Build())).Should(Succeed())
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost2.Name).Build())).Should(Succeed())
			})

			It("claims one of the available host", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				readyCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*readyCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(3))

				node1 := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, &node1)
				Expect(err).NotTo(HaveOccurred())

				node2 := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost2.Name, Namespace: defaultNamespace}, &node2)
				Expect(err).NotTo(HaveOccurred())

				var nodeTagged bool
				if strings.Contains(node1.Spec.ProviderID, controllers.ProviderIDPrefix) || strings.Contains(node2.Spec.ProviderID, controllers.ProviderIDPrefix) {
					nodeTagged = true
				}
				Expect(nodeTagged).To(Equal(true))
			})

			It("does not claims the attached host", func() {
				ph, err := patch.NewHelper(byoHost2, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoHost2.Labels = map[string]string{clusterv1.ClusterNameLabel: capiCluster.Name}
				Expect(ph.Patch(ctx, byoHost2, patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(ctx, byoHost2, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoHost).Labels[clusterv1.ClusterNameLabel] == capiCluster.Name
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoHost := &infrastructurev1beta1.ByoHost{}
				err = k8sClientUncached.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, createdByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoHost.Status.MachineRef.Namespace).To(Equal(defaultNamespace))
				Expect(createdByoHost.Status.MachineRef.Name).To(Equal(byoMachine.Name))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				readyCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*readyCondition).To(conditions.MatchCondition(clusterv1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(3))

				node := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, &node)
				Expect(err).NotTo(HaveOccurred())

				Expect(node.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost1)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, byoHost2)).Should(Succeed())
			})
		})

		Context("When installer config template exists", func() {
			It("should create installer config from the template", func() {
				ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
					Kind:       k8sInstallerConfigTemplateKind,
					Namespace:  k8sInstallerConfigTemplate.Namespace,
					Name:       k8sInstallerConfigTemplate.Name,
					UID:        k8sInstallerConfigTemplate.UID,
					APIVersion: infrastructurev1beta1.GroupVersion.String(),
				}
				Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).Should(MatchError("no hosts found"))

				createdK8sInstallerConfig := &infrastructurev1beta1.K8sInstallerConfig{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdK8sInstallerConfig)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(k8sInstallerConfigTemplate.Spec.Template.Spec).To(Equal(createdK8sInstallerConfig.Spec))
				Expect(createdK8sInstallerConfig.GetAnnotations()[infrastructurev1beta1.K8sVersionAnnotation]).To(Equal(*machine.Spec.Version))
			})
		})

		Context("When installer config template resource does not exists", func() {
			BeforeEach(func() {
				// delete k8sinstallerconfigtemplate resource
				Expect(k8sClientUncached.Delete(ctx, k8sInstallerConfigTemplate)).Should(Succeed())

				ph, err := patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
					Kind:       k8sInstallerConfigTemplateKind,
					Namespace:  defaultNamespace,
					Name:       defaultK8sInstallerConfigTemplateName,
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				}
				Expect(ph.Patch(ctx, byoMachine, patch.WithStatusObservedGeneration{})).Should(Succeed())
				WaitForObjectToBeUpdatedInCache(ctx, byoMachine, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
				})
			})

			It("should fail create installer config from the template", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).Should(MatchError(fmt.Sprintf("k8sinstallerconfigtemplates.infrastructure.cluster.x-k8s.io %q not found", defaultK8sInstallerConfigTemplateName)))

				createdK8sInstallerConfig := &infrastructurev1beta1.K8sInstallerConfig{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdK8sInstallerConfig)
				Expect(err).Should(MatchError(fmt.Sprintf("k8sinstallerconfigs.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))
			})
		})
	})

	Context("When cluster infrastructure is not ready", func() {
		BeforeEach(func() {
			ph, err := patch.NewHelper(capiCluster, k8sClientUncached)
			Expect(err).ShouldNot(HaveOccurred())
			capiCluster.Status.InfrastructureReady = false
			err = ph.Patch(ctx, capiCluster, patch.WithStatusObservedGeneration{})
			Expect(err).ShouldNot(HaveOccurred())

			WaitForObjectToBeUpdatedInCache(ctx, capiCluster, func(object client.Object) bool {
				return object.(*clusterv1.Cluster).Status.InfrastructureReady == false
			})
		})

		It("should mark BYOHostReady as False", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
			Expect(err).To(BeNil())

			createdByoMachine := &infrastructurev1beta1.ByoMachine{}
			err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
			Expect(err).ShouldNot(HaveOccurred())

			actualCondition := conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
			Expect(*actualCondition).To(conditions.MatchCondition(clusterv1.Condition{
				Type:     infrastructurev1beta1.BYOHostReady,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForClusterInfrastructureReason,
				Severity: clusterv1.ConditionSeverityInfo,
			}))

			// assert events
			events := eventutils.CollectEvents(recorder.Events)
			Expect(len(events)).Should(Equal(0))
		})
	})
})

func newMapFuncTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	testScheme := runtime.NewScheme()
	require.NoError(t, infrastructurev1beta1.AddToScheme(testScheme))
	require.NoError(t, clusterv1.AddToScheme(testScheme))
	return testScheme
}

func TestClusterToByoMachines(t *testing.T) {
	testScheme := newMapFuncTestScheme(t)

	matching := &infrastructurev1beta1.ByoMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "byomachine-in-cluster",
			Namespace: defaultNamespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: testMapFuncClusterName},
		},
	}
	other := &infrastructurev1beta1.ByoMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "byomachine-in-other-cluster",
			Namespace: defaultNamespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "cluster-b"},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(matching, other).Build()
	r := &controllers.ByoMachineReconciler{Client: fakeClient}

	mapFunc := r.ClusterToByoMachines(logr.Discard())

	t.Run("returns requests only for ByoMachines labeled with the cluster", func(t *testing.T) {
		cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: testMapFuncClusterName, Namespace: defaultNamespace}}
		requests := mapFunc(t.Context(), cluster)
		require.Len(t, requests, 1)
		assert.Equal(t, "byomachine-in-cluster", requests[0].Name)
		assert.Equal(t, defaultNamespace, requests[0].Namespace)
	})

	t.Run("returns nothing for a cluster with no matching ByoMachines", func(t *testing.T) {
		cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-with-no-machines", Namespace: defaultNamespace}}
		assert.Empty(t, mapFunc(t.Context(), cluster))
	})

	t.Run("skips clusters with a deletion timestamp", func(t *testing.T) {
		now := metav1.Now()
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:              testMapFuncClusterName,
				Namespace:         defaultNamespace,
				DeletionTimestamp: &now,
				Finalizers:        []string{"keep-for-test"},
			},
		}
		assert.Empty(t, mapFunc(t.Context(), cluster))
	})

	t.Run("returns nil for a non-Cluster object", func(t *testing.T) {
		assert.Nil(t, mapFunc(t.Context(), &corev1.Pod{}))
	})

	t.Run("returns nil when listing ByoMachines fails", func(t *testing.T) {
		listErr := errors.New("list failed")
		erroringClient := fake.NewClientBuilder().
			WithScheme(testScheme).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return listErr
				},
			}).
			Build()
		erroringReconciler := &controllers.ByoMachineReconciler{Client: erroringClient}
		erroringMapFunc := erroringReconciler.ClusterToByoMachines(logr.Discard())

		cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: testMapFuncClusterName, Namespace: defaultNamespace}}
		assert.Nil(t, erroringMapFunc(t.Context(), cluster))
	})
}

func TestByoHostToByoMachineMapFunc(t *testing.T) {
	byoMachineGVK := infrastructurev1beta1.GroupVersion.WithKind("ByoMachine")
	mapFunc := controllers.ByoHostToByoMachineMapFunc(byoMachineGVK)

	t.Run("maps to the referenced ByoMachine when the GroupKind matches", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{
			Status: infrastructurev1beta1.ByoHostStatus{
				MachineRef: &corev1.ObjectReference{
					APIVersion: byoMachineGVK.GroupVersion().String(),
					Kind:       byoMachineGVK.Kind,
					Namespace:  defaultNamespace,
					Name:       "referenced-byomachine",
				},
			},
		}
		requests := mapFunc(t.Context(), host)
		require.Len(t, requests, 1)
		assert.Equal(t, client.ObjectKey{Namespace: defaultNamespace, Name: "referenced-byomachine"}, requests[0].NamespacedName)
	})

	t.Run("returns nil when MachineRef is not set", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{}
		assert.Nil(t, mapFunc(t.Context(), host))
	})

	t.Run("returns nil when MachineRef points at a different GroupKind", func(t *testing.T) {
		host := &infrastructurev1beta1.ByoHost{
			Status: infrastructurev1beta1.ByoHostStatus{
				MachineRef: &corev1.ObjectReference{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Machine",
					Namespace:  defaultNamespace,
					Name:       "some-machine",
				},
			},
		}
		assert.Nil(t, mapFunc(t.Context(), host))
	})

	t.Run("returns nil for a non-ByoHost object", func(t *testing.T) {
		assert.Nil(t, mapFunc(t.Context(), &corev1.Pod{}))
	})
}
