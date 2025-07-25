// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	eventutils "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Byohost Agent Tests", func() {

	var (
		ctx                = context.TODO()
		ns                 = "default"
		hostName           = "test-host"
		byoHost            *infrastructurev1beta1.ByoHost
		byoMachine         *infrastructurev1beta1.ByoMachine
		byoHostLookupKey   types.NamespacedName
		bootstrapSecret    *corev1.Secret
		installationSecret *corev1.Secret
		recorder           *record.FakeRecorder
		uninstallScript    string
	)

	BeforeEach(func() {
		fakeCommandRunner = &cloudinitfakes.FakeICmdRunner{}
		fakeFileWriter = &cloudinitfakes.FakeIFileWriter{}
		fakeTemplateParser = &cloudinitfakes.FakeITemplateParser{}
		recorder = record.NewFakeRecorder(32)
		hostReconciler = &reconciler.HostReconciler{
			Client:              k8sClient,
			CmdRunner:           fakeCommandRunner,
			FileWriter:          fakeFileWriter,
			TemplateParser:      fakeTemplateParser,
			Recorder:            recorder,
			SkipK8sInstallation: false,
		}
	})

	It("should return an error if ByoHost is not found", func() {
		_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-host",
				Namespace: ns},
		})
		Expect(err).To(MatchError("byohosts.infrastructure.cluster.x-k8s.io \"non-existent-host\" not found"))
	})

	Context("When ByoHost exists", func() {
		BeforeEach(func() {
			byoHost = builder.ByoHost(ns, hostName).Build()
			Expect(k8sClient.Create(ctx, byoHost)).NotTo(HaveOccurred(), "failed to create byohost")
			var err error
			patchHelper, err = patch.NewHelper(byoHost, k8sClient)
			Expect(err).ShouldNot(HaveOccurred())

			byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: ns}
		})

		It("should set the Reason to WaitingForMachineRefReason if MachineRef isn't found", func() {
			result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: byoHostLookupKey,
			})

			Expect(result).To(Equal(controllerruntime.Result{}))
			Expect(reconcilerErr).ToNot(HaveOccurred())

			updatedByoHost := &infrastructurev1beta1.ByoHost{}
			err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
			Expect(err).ToNot(HaveOccurred())
			k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
			Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
				Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForMachineRefReason,
				Severity: clusterv1.ConditionSeverityInfo,
			}))
		})

		Context("When MachineRef is set", func() {
			BeforeEach(func() {
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Status.MachineRef = &corev1.ObjectReference{
					Kind:       "ByoMachine",
					Namespace:  byoMachine.Namespace,
					Name:       byoMachine.Name,
					UID:        byoMachine.UID,
					APIVersion: byoHost.APIVersion,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			It("should set the Reason to BootstrapDataSecretUnavailableReason", func() {
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				byoHostRegistrationSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*byoHostRegistrationSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BootstrapDataSecretUnavailableReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})

			It("should return an error if we fail to load the bootstrap secret", func() {
				byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: "non-existent",
					Name:      "non-existent",
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					fmt.Sprintf("Warning ReadBootstrapSecretFailed bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name),
				}))
			})

			Context("When bootstrap secret is ready", func() {
				BeforeEach(func() {
					secretData := `write_files:
- path: fake/path
  content: blah
runCmd:
- echo 'run some command'`

					bootstrapSecret = builder.Secret(ns, "test-secret").
						WithData(secretData).
						Build()
					Expect(k8sClient.Create(ctx, bootstrapSecret)).NotTo(HaveOccurred())

					byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: bootstrapSecret.Namespace,
						Name:      bootstrapSecret.Name,
					}

					byoHost.Annotations = map[string]string{
						infrastructurev1beta1.K8sVersionAnnotation:               "1.22",
						infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: "projects.blah.com",
					}

					Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				})

				It("should skip k8s installation if skip-installation is set", func() {
					hostReconciler.SkipK8sInstallation = true
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
					Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
						Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status: corev1.ConditionTrue,
					}))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).ShouldNot(ContainElement(
						"Normal k8sComponentInstalled Successfully Installed K8s components",
					))
				})

				It("should set the Reason to InstallationSecretUnavailableReason", func() {
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					byoHostRegistrationSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
					Expect(*byoHostRegistrationSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sInstallationSecretUnavailableReason,
						Severity: clusterv1.ConditionSeverityInfo,
					}))
				})

				It("should return an error if we fail to load the installation secret", func() {
					byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: "non-existent",
						Name:      "non-existent",
					}
					Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						fmt.Sprintf("Warning ReadInstallationSecretFailed install and uninstall script %s not found", byoHost.Spec.InstallationSecret.Name),
					}))
				})

				Context("When installation secret is ready", func() {
					BeforeEach(func() {
						installScript := `echo "install"`
						uninstallScript = `echo "uninstall"`

						installationSecret = builder.Secret(ns, "test-secret3").
							WithKeyData("install", installScript).
							WithKeyData("uninstall", uninstallScript).
							Build()
						Expect(k8sClient.Create(ctx, installationSecret)).NotTo(HaveOccurred())

						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: installationSecret.Namespace,
							Name:      installationSecret.Name,
						}

						byoHost.Annotations = map[string]string{
							infrastructurev1beta1.K8sVersionAnnotation:               "1.22",
							infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: "projects.blah.com",
						}

						Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
					})

					It("should execute bootstrap secret only once ", func() {

						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						_, reconcilerErr = hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to false with Reason CloudInitExecutionFailedReason if the bootstrap execution fails", func() {
						conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						fakeCommandRunner.RunCmdReturns(errors.New("I failed"))

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})

						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status:   corev1.ConditionFalse,
							Reason:   infrastructurev1beta1.CloudInitExecutionFailedReason,
							Severity: clusterv1.ConditionSeverityError,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning BootstrapK8sNodeFailed k8s Node Bootstrap failed",
							// TODO: improve test to remove this event
							"Warning ResetK8sNodeFailed k8s Node Reset failed",
						}))
					})

					It("should return error if install script execution failed", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						invalidInstallationSecret := builder.Secret(ns, "invalid-test-secret").
							WithKeyData("install", "test").
							Build()
						Expect(k8sClient.Create(ctx, invalidInstallationSecret)).NotTo(HaveOccurred())
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: invalidInstallationSecret.Namespace,
							Name:      invalidInstallationSecret.Name,
						}
						Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning InstallScriptExecutionFailed install script execution failed",
						}))
					})

					It("should return error if installation secrent does not exists", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: "non-existent",
							Name:      "non-existent",
						}
						Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning ReadInstallationSecretFailed install and uninstall script non-existent not found",
						}))

					})

					It("should set uninstall script in byohost spec", func() {
						uninstallSecretName := "byoh-uninstall-" + byoHost.Name
						uninstallSecret := &corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      uninstallSecretName,
								Namespace: ns,
							},
							Data: map[string][]byte{
								"uninstall": []byte(uninstallScript),
							},
						}
						Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())

						byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: ns,
							Name:      uninstallSecretName,
						}

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).NotTo(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())
						Expect(updatedByoHost.Spec.UninstallationSecret).NotTo(BeNil())
						Expect(updatedByoHost.Spec.UninstallationSecret.Name).To(Equal(uninstallSecretName))
					})

					It("should set K8sComponentsInstallationSucceeded to true if Install succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						K8sComponentsInstallationSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(*K8sComponentsInstallationSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:   infrastructurev1beta1.K8sComponentsInstallationSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})
					AfterEach(func() {
						Expect(k8sClient.Delete(ctx, installationSecret)).NotTo(HaveOccurred())
					})
				})

				AfterEach(func() {
					Expect(k8sClient.Delete(ctx, bootstrapSecret)).NotTo(HaveOccurred())
					hostReconciler.SkipK8sInstallation = false
				})
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(ctx, byoMachine)).NotTo(HaveOccurred())
			})
		})

		Context("When the ByoHost is marked for cleanup", func() {
			BeforeEach(func() {
				uninstallScript = `echo "uninstall success script"`
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Status.MachineRef = &corev1.ObjectReference{
					Kind:       "ByoMachine",
					Namespace:  byoMachine.Namespace,
					Name:       byoMachine.Name,
					UID:        byoMachine.UID,
					APIVersion: byoHost.APIVersion,
				}
				byoHost.Labels = map[string]string{clusterv1.ClusterNameLabel: "test-cluster"}
				byoHost.Annotations = map[string]string{
					infrastructurev1beta1.HostCleanupAnnotation:              "",
					infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: "projects.blah.com",
					infrastructurev1beta1.K8sVersionAnnotation:               "1.22",
				}
				conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			It("should skip node reset if k8s component installation failed", func() {
				var err error
				patchHelper, err = patch.NewHelper(byoHost, k8sClient)
				Expect(err).ShouldNot(HaveOccurred())

				conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded,
					infrastructurev1beta1.K8sComponentsInstallationFailedReason, clusterv1.ConditionSeverityInfo, "")
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				// assert kubeadm reset is not called
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))
			})

			It("should reset the node and set the Reason to K8sNodeAbsentReason", func() {
				uninstallSecretName := "byoh-uninstall-" + byoHost.Name
				uninstallSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      uninstallSecretName,
						Namespace: ns,
					},
					Data: map[string][]byte{
						"uninstall": []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())

				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: ns,
					Name:      uninstallSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				// assert kubeadm reset & uninstall script is called
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
				_, resetCommand := fakeCommandRunner.RunCmdArgsForCall(0)
				Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))
				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				Expect(updatedByoHost.Labels).NotTo(HaveKey(clusterv1.ClusterNameLabel))
				Expect(updatedByoHost.Status.MachineRef).To(BeNil())
				Expect(updatedByoHost.Annotations).NotTo(HaveKey(infrastructurev1beta1.HostCleanupAnnotation))
				Expect(updatedByoHost.Annotations).NotTo(HaveKey(infrastructurev1beta1.EndPointIPAnnotation))
				Expect(updatedByoHost.Annotations).NotTo(HaveKey(infrastructurev1beta1.K8sVersionAnnotation))
				Expect(updatedByoHost.Annotations).NotTo(HaveKey(infrastructurev1beta1.BundleLookupBaseRegistryAnnotation))
				Expect(updatedByoHost.Spec.UninstallationSecret).NotTo(BeNil())
				Expect(updatedByoHost.Spec.UninstallationSecret.Name).To(Equal(uninstallSecretName))

				k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
				}))
			})

			It("should return an error if we fail to load the uninstallation secret", func() {
				byoHost.Spec.UninstallationSecret = nil
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).To(MatchError("UninstallationSecret not found in Byohost " + byoHost.Name))
			})

			It("should return error if uninstall script execution failed", func() {
				fakeCommandRunner.RunCmdReturnsOnCall(1, errors.New("failed to execute uninstall script"))
				uninstallScript = `testcommand`
				uninstallSecretName := "byoh-uninstall-" + byoHost.Name
				uninstallSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      uninstallSecretName,
						Namespace: ns,
					},
					Data: map[string][]byte{
						"uninstall": []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: ns,
					Name:      uninstallSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).To(HaveOccurred())

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
					"Warning UninstallScriptExecutionFailed uninstall script execution failed",
				}))
			})

			It("should set K8sComponentsInstallationSucceeded to false if uninstall succeeds", func() {
				uninstallSecretName := "byoh-uninstall-" + byoHost.Name
				uninstallSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      uninstallSecretName,
						Namespace: ns,
					},
					Data: map[string][]byte{
						"uninstall": []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: ns,
					Name:      uninstallSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				K8sComponentsInstallationSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				Expect(*K8sComponentsInstallationSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})

			It("It should reset byoHost.Spec.InstallationSecret if uninstall succeeds", func() {
				uninstallSecretName := "byoh-uninstall-" + byoHost.Name
				uninstallSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      uninstallSecretName,
						Namespace: ns,
					},
					Data: map[string][]byte{
						"uninstall": []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: ns,
					Name:      uninstallSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedByoHost.Spec.InstallationSecret).To(BeNil())
			})

			It("It should reset byoHost.Spec.UninstallationSecret if uninstall succeeds", func() {
				uninstallSecretName := "byoh-uninstall-" + byoHost.Name
				uninstallSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      uninstallSecretName,
						Namespace: ns,
					},
					Data: map[string][]byte{
						"uninstall": []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: ns,
					Name:      uninstallSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedByoHost.Spec.UninstallationSecret).To(BeNil())
			})

			It("should skip uninstallation if skip-installation flag is set", func() {
				hostReconciler.SkipK8sInstallation = true
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
					Severity: clusterv1.ConditionSeverityInfo,
				}))
			})

			It("should return error if host cleanup failed", func() {
				fakeCommandRunner.RunCmdReturns(errors.New("failed to cleanup host"))

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr.Error()).To(Equal("failed to exec kubeadm reset: failed to cleanup host"))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				k8sNodeBootstrapSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
					Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Warning ResetK8sNodeFailed k8s Node Reset failed",
				}))
			})
		})

		Context("When the ByoHost has deletion timestamp set", func() {
			BeforeEach(func() {
				byoHost.SetFinalizers([]string{"test"})
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(context.TODO(), byoHost)).NotTo(HaveOccurred())
			})
			It("should trigger reconcile delete", func() {
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

			})

			AfterEach(func() {
				byoHost.SetFinalizers([]string{})
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, byoHost)).NotTo(HaveOccurred())
			hostReconciler.SkipK8sInstallation = false
		})
	})
})
