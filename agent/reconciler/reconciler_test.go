// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kindSecret                   = "Secret"
	nonExistentName              = "non-existent"
	testK8sVersion               = "1.22"
	testBundleLookupBaseRegistry = "projects.blah.com"
	uninstallScriptKey           = "uninstall"

	eventInstallScriptExecutionSucceeded = "Normal InstallScriptExecutionSucceeded install script executed"
	eventBootstrapK8sNodeSucceeded       = "Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped"
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
			PackagePull:         func(context.Context, string, string) error { return nil },
			Exit:                func() {},
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
				// host_reconciler.go returns before reading BootstrapSecret unless install is already marked done; mark it here since this test isn't exercising install.
				conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
					Namespace: nonExistentName,
					Name:      nonExistentName,
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
						Kind:      kindSecret,
						Namespace: bootstrapSecret.Namespace,
						Name:      bootstrapSecret.Name,
					}

					byoHost.Annotations = map[string]string{
						infrastructurev1beta1.K8sVersionAnnotation:               testK8sVersion,
						infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: testBundleLookupBaseRegistry,
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

					// only the join step's runCmd/write_files ran -- no install call, and no second reconcile was needed to reach it
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
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
						Kind:      kindSecret,
						Namespace: nonExistentName,
						Name:      nonExistentName,
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
						fmt.Sprintf("Warning ReadInstallationSecretFailed install script %s not found", byoHost.Spec.InstallationSecret.Name),
					}))
				})

				Context("When installation secret is ready", func() {
					BeforeEach(func() {
						installScript := `echo "install"`
						uninstallScript = `echo "uninstall"`

						installationSecret = builder.Secret(ns, "test-secret3").
							WithKeyData("install", installScript).
							WithKeyData(uninstallScriptKey, uninstallScript).
							Build()
						Expect(k8sClient.Create(ctx, installationSecret)).NotTo(HaveOccurred())

						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      kindSecret,
							Namespace: installationSecret.Namespace,
							Name:      installationSecret.Name,
						}

						byoHost.Annotations = map[string]string{
							infrastructurev1beta1.K8sVersionAnnotation:               testK8sVersion,
							infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: testBundleLookupBaseRegistry,
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
						// install lands on the first reconcile, join on the next (see host_reconciler.go)
						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

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
							eventInstallScriptExecutionSucceeded,
							eventBootstrapK8sNodeSucceeded,
						}))
					})

					// KAAP-2331: once install has succeeded, a later reconcile reads BootstrapSecret fresh with no slow step in between, so it already recovers once the secret is fixed -- no fix needed for this path.
					It("should recover on the next reconcile once the bootstrap secret has been fixed, even without any fix (KAAP-2331)", func() {
						staleSecretData := `write_files:
- path: fake/path
  content: blah
runCmd:
- echo 'stale-join'`
						freshSecretData := `write_files:
- path: fake/path
  content: blah2
runCmd:
- echo 'fresh-join'`

						latest := &corev1.Secret{}
						Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bootstrapSecret.Name, Namespace: bootstrapSecret.Namespace}, latest)).To(Succeed())
						latest.Data["value"] = []byte(staleSecretData)
						Expect(k8sClient.Update(ctx, latest)).To(Succeed())

						fakeCommandRunner.RunCmdStub = func(runCtx context.Context, cmd string) error {
							if cmd == "echo 'stale-join'" {
								return errors.New("token expired")
							}
							return nil
						}

						// first reconcile: install only (see host_reconciler.go -- join lands on the next reconcile)
						_, installErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(installErr).ToNot(HaveOccurred())

						// second reconcile: join fails on the stale/expired token
						_, firstErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(firstErr).To(HaveOccurred())

						afterFirst := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClient.Get(ctx, byoHostLookupKey, afterFirst)).To(Succeed())
						Expect(conditions.IsTrue(afterFirst, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(BeTrue(),
							"install must not be re-run on retry")
						k8sNodeBootstrapSucceeded := conditions.Get(afterFirst, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status:   corev1.ConditionFalse,
							Reason:   infrastructurev1beta1.CloudInitExecutionFailedReason,
							Severity: clusterv1.ConditionSeverityError,
						}))

						// simulate the secret getting fixed (e.g. CABPK recreating the token) before the next reconcile
						latest = &corev1.Secret{}
						Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bootstrapSecret.Name, Namespace: bootstrapSecret.Namespace}, latest)).To(Succeed())
						latest.Data["value"] = []byte(freshSecretData)
						Expect(k8sClient.Update(ctx, latest)).To(Succeed())

						// third reconcile: install is already done, so this attempt reads the secret fresh with no gap
						_, secondErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(secondErr).ToNot(HaveOccurred())

						afterSecond := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClient.Get(ctx, byoHostLookupKey, afterSecond)).To(Succeed())
						k8sNodeBootstrapSucceeded = conditions.Get(afterSecond, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
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
							Kind:      kindSecret,
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
							Kind:      kindSecret,
							Namespace: nonExistentName,
							Name:      nonExistentName,
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
							"Warning ReadInstallationSecretFailed install script non-existent not found",
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
								uninstallScriptKey: []byte(uninstallScript),
							},
						}
						Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())

						byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
							Kind:      kindSecret,
							Namespace: ns,
							Name:      uninstallSecretName,
						}
						Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						// requeues immediately after install rather than waiting on HeartbeatInterval -- see the Reconcile wrapper
						Expect(result).To(Equal(controllerruntime.Result{Requeue: true}))
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
						// requeues immediately rather than waiting on HeartbeatInterval -- see the Reconcile wrapper
						Expect(result).To(Equal(controllerruntime.Result{Requeue: true}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						K8sComponentsInstallationSucceeded := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(*K8sComponentsInstallationSucceeded).To(conditions.MatchCondition(clusterv1.Condition{
							Type:   infrastructurev1beta1.K8sComponentsInstallationSucceeded,
							Status: corev1.ConditionTrue,
						}))
					})

					It("should requeue immediately after install regardless of HeartbeatInterval", func() {
						hostReconciler.HeartbeatInterval = time.Hour // deliberately huge -- the post-install requeue must not inherit this

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())
						Expect(result).To(Equal(controllerruntime.Result{Requeue: true}))

						// assert events -- join lands on the next reconcile (see host_reconciler.go), so only the install event has fired so far
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							eventInstallScriptExecutionSucceeded,
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {
						// install lands on the first reconcile, join on the next (see host_reconciler.go)
						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

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
							eventInstallScriptExecutionSucceeded,
							eventBootstrapK8sNodeSucceeded,
						}))
					})

					It("keeps refreshing the heartbeat while the install script is still running (KAAP-2330)", func() {
						// See the second-precision comment on
						// TestHostReconciler_Heartbeat's refresh case — here
						// we additionally need to cross more than one
						// whole-second boundary, since we're asserting on
						// 2+ distinct timestamps.
						hostReconciler.HeartbeatInterval = 1300 * time.Millisecond
						hostReconciler.MaxBlockingDuration = 30 * time.Second
						installCallCount := 0
						fakeCommandRunner.RunCmdStub = func(_ context.Context, _ string) error {
							installCallCount++
							if installCallCount == 1 {
								// Only the install call needs to be slow —
								// the join call doesn't add anything this
								// test needs, so let it return immediately.
								time.Sleep(3 * time.Second)
							}
							return nil
						}

						done := make(chan error, 1)
						go func() {
							_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
								NamespacedName: byoHostLookupKey,
							})
							done <- err
						}()

						seen := map[time.Time]struct{}{}
						for i := 0; i < 7; i++ {
							time.Sleep(400 * time.Millisecond)
							h := &infrastructurev1beta1.ByoHost{}
							Expect(k8sClient.Get(ctx, byoHostLookupKey, h)).To(Succeed())
							if h.Status.LastHeartbeatTime != nil {
								seen[h.Status.LastHeartbeatTime.Time] = struct{}{}
							}
						}
						Expect(len(seen)).To(BeNumerically(">=", 2),
							"expected at least two distinct heartbeat timestamps while the install script was still running")

						Eventually(done, 10*time.Second).Should(Receive(BeNil()))
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
					infrastructurev1beta1.BundleLookupBaseRegistryAnnotation: testBundleLookupBaseRegistry,
					infrastructurev1beta1.K8sVersionAnnotation:               testK8sVersion,
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
						uninstallScriptKey: []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())

				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
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
				Expect(updatedByoHost.Spec.UninstallationSecret).ToNot(BeNil(),
					"UninstallationSecret reference should be cleared after successful cleanup")

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
				missingSecretName := "byoh-uninstall-missing-" + byoHost.Name
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
					Namespace: ns,
					Name:      missingSecretName,
				}
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).To(HaveOccurred())

				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).To(ContainElement("Warning ReadUninstallationSecretFailed uninstallation secret " + missingSecretName + " not found"))
			})

			It("should not run kubeadm reset a second time when uninstall secret is absent", func() {
				// First reconcile: kubeadm reset runs, uninstall is skipped (nil secret ref), reconcile returns nil
				byoHost.Spec.UninstallationSecret = nil
				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				_, firstErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(firstErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1), "kubeadm reset must run exactly once on first reconcile")
				_, firstCmd := fakeCommandRunner.RunCmdArgsForCall(0)
				Expect(firstCmd).To(Equal(reconciler.KubeadmResetCommand))

				// Reload host state as the reconciler patched it
				reloadedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, reloadedHost)).NotTo(HaveOccurred())
				k8sComponentsCond := conditions.Get(reloadedHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				Expect(k8sComponentsCond).NotTo(BeNil())
				Expect(k8sComponentsCond.Status).To(Equal(corev1.ConditionFalse),
					"K8sComponentsInstallationSucceeded must be False after reset so second reconcile skips kubeadm reset")

				// Second reconcile: must NOT run kubeadm reset again
				_, secondErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(secondErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1), "kubeadm reset must NOT be called again on second reconcile")
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
						uninstallScriptKey: []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
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
						uninstallScriptKey: []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
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
						uninstallScriptKey: []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
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
						uninstallScriptKey: []byte(uninstallScript),
					},
				}
				Expect(k8sClient.Create(ctx, uninstallSecret)).NotTo(HaveOccurred())
				byoHost.Spec.UninstallationSecret = &corev1.ObjectReference{
					Kind:      kindSecret,
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
				Expect(updatedByoHost.Spec.UninstallationSecret).ToNot(BeNil())
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

// newHostReconcilerForHeartbeatTest builds a minimal HostReconciler wired to the
// shared envtest k8sClient with SkipK8sInstallation=true so reconcile only
// exercises the heartbeat path without attempting real k8s installation.
func newHostReconcilerForHeartbeatTest(t *testing.T, interval time.Duration) *reconciler.HostReconciler {
	t.Helper()
	return &reconciler.HostReconciler{
		Client:              k8sClient,
		CmdRunner:           &cloudinitfakes.FakeICmdRunner{},
		FileWriter:          &cloudinitfakes.FakeIFileWriter{},
		TemplateParser:      &cloudinitfakes.FakeITemplateParser{},
		PackagePull:         func(context.Context, string, string) error { return nil },
		Exit:                func() {},
		Recorder:            record.NewFakeRecorder(32),
		SkipK8sInstallation: true,
		HeartbeatInterval:   interval,
	}
}

// newByoHostInAPIServer creates a ByoHost in the envtest API server and
// registers a t.Cleanup to delete it when the test ends.
func newByoHostInAPIServer(t *testing.T, name string) (*infrastructurev1beta1.ByoHost, types.NamespacedName) {
	t.Helper()
	byoHost := builder.ByoHost("default", name).Build()
	require.NoError(t, k8sClient.Create(t.Context(), byoHost))
	t.Cleanup(func() {
		// t.Context() is already canceled by the time Cleanup funcs run, so
		// this delete needs its own context rather than the test's.
		_ = client.IgnoreNotFound(k8sClient.Delete(context.Background(), byoHost))
	})
	return byoHost, types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
}

func TestHostReconciler_Heartbeat(t *testing.T) {
	t.Run("stamps LastHeartbeatTime and requeues at HeartbeatInterval", func(t *testing.T) {
		r := newHostReconcilerForHeartbeatTest(t, 1300*time.Millisecond)
		_, key := newByoHostInAPIServer(t, "heartbeat-stamps")

		result, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)
		assert.Equal(t, 1300*time.Millisecond, result.RequeueAfter)

		updated := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, updated))
		assert.NotNil(t, updated.Status.LastHeartbeatTime)
	})

	t.Run("does not re-stamp within the interval", func(t *testing.T) {
		r := newHostReconcilerForHeartbeatTest(t, 1300*time.Millisecond)
		_, key := newByoHostInAPIServer(t, "heartbeat-nowrite")

		// First reconcile — stamps the time
		_, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)

		first := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, first))
		require.NotNil(t, first.Status.LastHeartbeatTime)
		firstRV := first.ResourceVersion
		firstStamp := first.Status.LastHeartbeatTime.Time

		// Second reconcile immediately — within interval, must not re-stamp
		_, err = r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)

		second := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, second))
		assert.Equal(t, firstRV, second.ResourceVersion, "resourceVersion must not change on a no-op heartbeat")
		assert.Equal(t, firstStamp, second.Status.LastHeartbeatTime.Time, "LastHeartbeatTime must not change within interval")
	})

	t.Run("refreshes LastHeartbeatTime after interval elapses", func(t *testing.T) {
		interval := 1300 * time.Millisecond
		r := newHostReconcilerForHeartbeatTest(t, interval)
		_, key := newByoHostInAPIServer(t, "heartbeat-refresh")

		_, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)

		first := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, first))
		require.NotNil(t, first.Status.LastHeartbeatTime)

		// metav1.Time has second-precision through the API server; sleep enough
		// to cross a whole-second boundary regardless of test start alignment.
		time.Sleep(interval + 1200*time.Millisecond)

		_, err = r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)

		second := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, second))
		require.NotNil(t, second.Status.LastHeartbeatTime)
		assert.True(t, second.Status.LastHeartbeatTime.After(first.Status.LastHeartbeatTime.Time),
			"LastHeartbeatTime should advance after interval elapsed")
	})
}

func TestHostReconciler_AgentVersion(t *testing.T) {
	t.Run("stamps AgentVersion on the same tick as LastHeartbeatTime", func(t *testing.T) {
		original := version.GitVersion
		version.GitVersion = "v-test-1"
		t.Cleanup(func() { version.GitVersion = original })

		r := newHostReconcilerForHeartbeatTest(t, 1300*time.Millisecond)
		_, key := newByoHostInAPIServer(t, "agent-version-stamps")

		_, err := r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
		require.NoError(t, err)

		updated := &infrastructurev1beta1.ByoHost{}
		require.NoError(t, k8sClient.Get(t.Context(), key, updated))
		assert.Equal(t, "v-test-1", updated.Status.AgentVersion)
		assert.NotNil(t, updated.Status.LastHeartbeatTime)
	})
}
