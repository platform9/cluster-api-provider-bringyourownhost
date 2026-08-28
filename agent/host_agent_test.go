// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/jackpal/gateway"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/builder"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/test/e2e"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
)

// getHostAgentCSRs returns every certificate signing request the agent created
// for hostName. Each request carries a random name suffix, so they can only
// be found through their label.
func getHostAgentCSRs(ctx SpecContext, hostName string) ([]certv1.CertificateSigningRequest, error) {
	list, err := clientSet.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", infrastructurev1beta1.HostCSRLabel, hostName),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// getHostAgentCSR returns the one certificate signing request the agent created
// for hostName, and errors when there is any other number of them.
func getHostAgentCSR(ctx SpecContext, hostName string) (*certv1.CertificateSigningRequest, error) {
	items, err := getHostAgentCSRs(ctx, hostName)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("expected exactly one certificate signing request for host %s, got %d", hostName, len(items))
	}
	return &items[0], nil
}

var _ = Describe("Agent", func() {

	Context("When the host is unable to register with the API server", func() {
		var (
			ns               *corev1.Namespace
			err              error
			hostName         string
			runner           *e2e.ByoHostRunner
			byoHostContainer *container.CreateResponse
		)

		BeforeEach(func(ctx SpecContext) {
			ns = builder.Namespace("testns").Build()
			Expect(k8sClient.Create(ctx, ns)).NotTo(HaveOccurred(), "failed to create test namespace")

			hostName, err = os.Hostname()
			Expect(err).NotTo(HaveOccurred())
			hostName += "-" + hostNameSuffix
			runner = setupTestInfra(suiteCtx, hostName, getKubeConfig().Name(), ns)

			byoHostContainer, err = runner.SetupByoDockerHost()
			Expect(err).NotTo(HaveOccurred())

		})

		AfterEach(func() {
			cleanup(runner.Context, byoHostContainer, ns, agentLogFile)
		})

		It("should not error out if the host already exists", func(ctx SpecContext) {
			// not using the builder method here
			// because builder makes use of GenerateName that generates random names
			// For the below byoHost we need the name to be deterministic
			byoHost := &infrastructurev1beta1.ByoHost{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ByoHost",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      hostName,
					Namespace: ns.Name,
				},
				Spec: infrastructurev1beta1.ByoHostSpec{},
			}
			Expect(k8sClient.Create(ctx, byoHost)).NotTo(HaveOccurred())

			runner.CommandArgs["--downloadpath"] = fakeDownloadPath
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())

			defer e2e.StreamDockerLog(output, agentLogFile)()
			Consistently(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), "\"msg\"=\"error\"") {
						return true
					}
				}
				return false
			}).Should(BeFalse())
		})
	})

	Context("When the host agent is able to connect to API Server", func() {

		var (
			ns               *corev1.Namespace
			hostName         string
			fakeDownloadPath = "fake-download-path"
			runner           *e2e.ByoHostRunner
			byoHostContainer *container.CreateResponse
			output           dockertypes.HijackedResponse
		)

		BeforeEach(func(ctx SpecContext) {
			ns = builder.Namespace("testns").Build()
			Expect(k8sClient.Create(ctx, ns)).NotTo(HaveOccurred(), "failed to create test namespace")
			var err error
			hostName, err = os.Hostname()
			Expect(err).NotTo(HaveOccurred())
			hostName += "-" + hostNameSuffix

			runner = setupTestInfra(suiteCtx, hostName, getKubeConfig().Name(), ns)
			runner.CommandArgs["--label"] = "site=apac"
			runner.CommandArgs["--downloadpath"] = fakeDownloadPath

			byoHostContainer, err = runner.SetupByoDockerHost()
			Expect(err).NotTo(HaveOccurred())

			output, _, err = runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())

			// wait until the agent process starts inside the byoh host container
			Eventually(func(ctx SpecContext) bool {
				containerTop, _ := runner.DockerClient.ContainerTop(ctx, byoHostContainer.ID, []string{})
				for _, proc := range containerTop.Processes {
					if strings.Contains(proc[len(containerTop.Titles)-1], "agent") {
						return true
					}

				}
				return false
			}, 60).WithContext(ctx).Should(BeTrue())
		})

		AfterEach(func() {
			cleanup(runner.Context, byoHostContainer, ns, agentLogFile)
		})

		It("should register the BYOHost with the management cluster", func(ctx SpecContext) {
			byoHostLookupKey := types.NamespacedName{Name: hostName, Namespace: ns.Name}
			createdByoHost := &infrastructurev1beta1.ByoHost{}
			Eventually(func(ctx SpecContext) *infrastructurev1beta1.ByoHost {
				err := k8sClient.Get(ctx, byoHostLookupKey, createdByoHost)
				if err != nil {
					return nil
				}
				return createdByoHost
			}).WithContext(ctx).ShouldNot(BeNil())
		})

		It("should register the BYOHost with the passed labels", func(ctx SpecContext) {
			byoHostLookupKey := types.NamespacedName{Name: hostName, Namespace: ns.Name}
			createdByoHost := &infrastructurev1beta1.ByoHost{}
			Eventually(func(ctx SpecContext) map[string]string {
				err := k8sClient.Get(ctx, byoHostLookupKey, createdByoHost)
				if err != nil {
					return nil
				}
				return createdByoHost.ObjectMeta.Labels
			}).WithContext(ctx).Should(HaveKeyWithValue("site", "apac"))
		})

		It("should mirror HostInfo.Architecture and the detected package family onto labels", func(ctx SpecContext) {
			byoHostLookupKey := types.NamespacedName{Name: hostName, Namespace: ns.Name}
			createdByoHost := &infrastructurev1beta1.ByoHost{}
			Eventually(func(ctx SpecContext) map[string]string {
				err := k8sClient.Get(ctx, byoHostLookupKey, createdByoHost)
				if err != nil {
					return nil
				}
				return createdByoHost.ObjectMeta.Labels
			}).WithContext(ctx).Should(And(
				HaveKeyWithValue(infrastructurev1beta1.HostArchitectureLabel, runtime.GOARCH),
				HaveKeyWithValue(infrastructurev1beta1.HostOSFamilyLabel, infrastructurev1beta1.HostOSFamilyDebian),
			))
		})

		It("should skip bootstrap kubeconfig flow in default mode", func() {
			defer e2e.StreamDockerLog(output, agentLogFile)()

			Consistently(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && !strings.Contains(string(data), "initiated bootstrap kubeconfig flow") {
						return true
					}
				}
				return false
			}, time.Second*2).Should(BeTrue())
		})

		It("should fetch networkstatus when register the BYOHost with the management cluster", func(ctx SpecContext) {
			byoHostLookupKey := types.NamespacedName{Name: hostName, Namespace: ns.Name}
			defaultIP, err := gateway.DiscoverInterface()
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(ctx SpecContext) bool {
				createdByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, createdByoHost)
				if err != nil {
					return false
				}
				// check if default ip and networkInterfaceName is right
				for _, item := range createdByoHost.Status.Network {
					if item.IsDefault {
						iface, err := net.InterfaceByName(item.NetworkInterfaceName)
						if err != nil {
							return false
						}

						addrs, err := iface.Addrs()
						if err != nil {
							return false
						}

						for _, addr := range addrs {
							var ip net.IP
							switch v := addr.(type) {
							case *net.IPNet:
								ip = v.IP
							case *net.IPAddr:
								ip = v.IP
							}
							if ip.String() == defaultIP.String() {
								return true
							}
						}
					}
				}
				return false
			}).WithContext(ctx).Should(BeTrue())

		})

		It("should only reconcile ByoHost resource that the agent created", func(ctx SpecContext) {
			byoHost := builder.ByoHost(ns.Name, "random-second-host").Build()
			Expect(k8sClient.Create(ctx, byoHost)).NotTo(HaveOccurred(), "failed to create byohost")

			defer e2e.StreamDockerLog(output, agentLogFile)()
			Consistently(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), byoHost.Name) {
						return true
					}
				}
				return false
			}, 10, 1).ShouldNot(BeTrue())
		})

		Context("when machineref, bootstrap & installation secret is assigned", func() {
			var (
				byoMachine *infrastructurev1beta1.ByoMachine
				namespace  types.NamespacedName
			)
			BeforeEach(func(ctx SpecContext) {
				byoMachine = builder.ByoMachine(ns.Name, defaultByoMachineName).Build()
				Expect(k8sClient.Create(ctx, byoMachine)).Should(Succeed())
				byoHost := &infrastructurev1beta1.ByoHost{}
				namespace = types.NamespacedName{Name: hostName, Namespace: ns.Name}
				Eventually(func(ctx SpecContext) (err error) {
					err = k8sClient.Get(ctx, namespace, byoHost)
					return err
				}).WithContext(ctx).Should(BeNil())

				patchHelper, _ := patch.NewHelper(byoHost, k8sClient)
				byoHost.Status.MachineRef = &corev1.ObjectReference{
					APIVersion: byoMachine.APIVersion,
					Kind:       byoMachine.Kind,
					Namespace:  byoMachine.Namespace,
					Name:       byoMachine.Name,
					UID:        byoMachine.UID,
				}
				byoHost.Annotations = map[string]string{}
				byoHost.Annotations[infrastructurev1beta1.K8sVersionAnnotation] = K8sVersion
				byoHost.Annotations[infrastructurev1beta1.BundleLookupBaseRegistryAnnotation] = bundleLookupBaseRegistry

				fakeBootstrapSecret := builder.Secret(ns.Name, fakeBootstrapSecret).Build()
				err := k8sClient.Create(ctx, fakeBootstrapSecret)
				Expect(err).ToNot(HaveOccurred())
				byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: byoMachine.Namespace,
					Name:      fakeBootstrapSecret.Name,
				}

				fakeInstallationSecret := builder.Secret(ns.Name, fakeInstallationSecret).WithKeyData("install", "echo install-k8s").Build()
				err = k8sClient.Create(ctx, fakeInstallationSecret)
				Expect(err).ToNot(HaveOccurred())

				byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
					APIVersion: fakeInstallationSecret.APIVersion,
					Kind:       fakeInstallationSecret.Kind,
					Namespace:  fakeInstallationSecret.Namespace,
					Name:       fakeInstallationSecret.Name,
					UID:        fakeInstallationSecret.UID,
				}

				Expect(patchHelper.Patch(ctx, byoHost, patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			It("should run the script to install k8s components", func(ctx SpecContext) {
				defer e2e.StreamDockerLog(output, agentLogFile)()
				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Eventually(func(ctx SpecContext) (condition corev1.ConditionStatus) {
					err := k8sClient.Get(ctx, namespace, updatedByoHost)
					if err == nil {
						kubeInstallStatus := conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						if kubeInstallStatus != nil {
							return kubeInstallStatus.Status
						}
					}
					return corev1.ConditionFalse
				}, 100).WithContext(ctx).Should(Equal(corev1.ConditionTrue)) // installing K8s components is a lengthy operation, setting the timeout to 100s
			})
		})
	})

	Context("When host agent is executed with --version flag", func() {
		var (
			tmpHostAgentBinary string
		)
		BeforeEach(func() {
			date, err := exec.Command("date").Output()
			Expect(err).NotTo(HaveOccurred())

			version.GitMajor = "1"
			version.GitMinor = "2"
			version.GitVersion = "v1.2.3"
			version.GitCommit = "abc"
			version.GitTreeState = "clean"
			version.BuildDate = string(date)

			ldflags := fmt.Sprintf("-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitMajor=%s'"+
				"-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitMinor=%s'"+
				"-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitVersion=%s'"+
				"-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitCommit=%s'"+
				"-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.GitTreeState=%s'"+
				"-X 'github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version.BuildDate=%s'",
				version.GitMajor, version.GitMinor, version.GitVersion, version.GitCommit, version.GitTreeState, version.BuildDate)

			tmpHostAgentBinary, err = gexec.Build("github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent", "-ldflags", ldflags)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			version.GitMajor = ""
			version.GitMinor = ""
			version.GitVersion = ""
			version.GitCommit = ""
			version.GitTreeState = ""
			version.BuildDate = ""
			tmpHostAgentBinary = ""
		})

		It("Shows the appropriate version of the agent", func() {
			expectedStruct := version.Info{
				Major:        "1",
				Minor:        "2",
				GitVersion:   "v1.2.3",
				GitCommit:    "abc",
				GitTreeState: "clean",
				BuildDate:    version.BuildDate,
				GoVersion:    runtime.Version(),
				Compiler:     runtime.Compiler,
				Platform:     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			}
			expected := fmt.Sprintf("byoh-hostagent version: %#v\n", expectedStruct)
			out, err := exec.Command(tmpHostAgentBinary, "--version").Output()
			Expect(err).NotTo(HaveOccurred())
			output := string(out)
			Expect(output).Should(Equal(expected))
		})
	})

	Context("When --version flag is created using 'version.sh' script", func() {
		var (
			tmpHostAgentBinary string
			gitMajor           string
			gitMinor           string
			gitVersion         string
			err                error
		)
		BeforeEach(func() {
			command := exec.Command("/bin/sh", "-c", "git describe --tags --abbrev=14 --match 'v[0-9]*' 2>/dev/null")
			command.Stderr = os.Stderr
			cmdOut, _ := command.Output()
			gitVersion = strings.TrimSuffix(string(cmdOut), "\n")

			gitVersion = strings.Split(gitVersion, "-")[0]
			gitVars := strings.Split(gitVersion, ".")
			if len(gitVars) > 1 {
				gitMajor = gitVars[0][1:]
				gitMinor = gitVars[1]
			}

			root, _ := exec.Command("/bin/sh", "-c", "git rev-parse --show-toplevel").Output()
			cmd := exec.Command("/bin/sh", "-c", strings.TrimSuffix(string(root), "\n")+"/hack/version.sh")
			ldflags, _ := cmd.Output()
			tmpHostAgentBinary, err = gexec.Build("github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent", "-ldflags", string(ldflags))
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			tmpHostAgentBinary = ""
			gitMajor = ""
			gitMinor = ""
			gitVersion = ""
		})

		It("should match local generated git values", func() {
			out, err := exec.Command(tmpHostAgentBinary, "--version").Output()
			Expect(err).NotTo(HaveOccurred())

			majorExpected := "Major:\"" + gitMajor + "\""
			Expect(out).Should(ContainSubstring(majorExpected))

			minorExpected := "Minor:\"" + gitMinor + "\""
			Expect(out).Should(ContainSubstring(minorExpected))

			gitVersionExpected := "GitVersion:\"" + gitVersion
			Expect(out).Should(ContainSubstring(gitVersionExpected))

		})
	})

	Context("When the host agent is executed with --skip-installation flag", func() {
		var (
			ns               *corev1.Namespace
			err              error
			hostName         string
			fakeDownloadPath = "fake-download-path"
			runner           *e2e.ByoHostRunner
			byoHostContainer *container.CreateResponse
		)

		BeforeEach(func(ctx SpecContext) {
			ns = builder.Namespace("testns").Build()
			Expect(k8sClient.Create(ctx, ns)).NotTo(HaveOccurred(), "failed to create test namespace")

			hostName, err = os.Hostname()
			Expect(err).NotTo(HaveOccurred())
			hostName += "-" + hostNameSuffix
			runner = setupTestInfra(suiteCtx, hostName, getKubeConfig().Name(), ns)

			byoHostContainer, err = runner.SetupByoDockerHost()
			Expect(err).NotTo(HaveOccurred())

		})

		AfterEach(func() {
			cleanup(runner.Context, byoHostContainer, ns, agentLogFile)
		})

		It("should skip installation of k8s components", func() {
			runner.CommandArgs["--downloadpath"] = fakeDownloadPath
			runner.CommandArgs["--skip-installation"] = ""
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())

			defer e2e.StreamDockerLog(output, agentLogFile)()
			Eventually(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), "\"msg\"=\"skip-installation flag set, skipping installer initialisation\"") {
						return true
					}
				}
				return false
			}, 30).Should(BeTrue())
		})
	})

	Context("When the host agent is executed with --bootstrap-kubeconfig", func() {

		var (
			ns               *corev1.Namespace
			hostName         string
			runner           *e2e.ByoHostRunner
			byoHostContainer *container.CreateResponse
			output           dockertypes.HijackedResponse
		)

		BeforeEach(func(ctx SpecContext) {
			ns = builder.Namespace("testns").Build()
			Expect(k8sClient.Create(ctx, ns)).NotTo(HaveOccurred(), "failed to create test namespace")

			var err error
			hostName, err = os.Hostname()
			Expect(err).NotTo(HaveOccurred())
			hostName += "-" + hostNameSuffix

			runner = setupTestInfra(suiteCtx, hostName, getKubeConfig().Name(), ns)
			runner.CommandArgs["--bootstrap-kubeconfig"] = "/root/config"
			byoHostContainer, err = runner.SetupByoDockerHost()
			Expect(err).NotTo(HaveOccurred())

			// Clean for any CSR present
			var csrList certv1.CertificateSigningRequestList
			Expect(k8sClient.List(ctx, &csrList)).ShouldNot(HaveOccurred())
			for _, csr := range csrList.Items {
				Expect(k8sClient.Delete(ctx, &csr)).ShouldNot(HaveOccurred())
			}
		})

		JustAfterEach(func() {
			if CurrentGinkgoTestDescription().Failed {
				e2e.ShowFileContent(agentLogFile)
			}
		})

		AfterEach(func() {
			cleanup(runner.Context, byoHostContainer, ns, agentLogFile)
		})

		It("should enable the bootstrap kubeconfig flow if the ~/.byoh/config does not exist", func() {
			// start agent
			var err error
			output, _, err = runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()
			Eventually(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), "\"msg\"=\"initiated bootstrap kubeconfig flow\"") {
						return true
					}
				}
				return false
			}, time.Second*2).Should(BeTrue())
		})
		It("should skip bootstrap kubeconfig flow if the ~/.byoh/config exists", func(ctx SpecContext) {
			cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
			Expect(err).ShouldNot(HaveOccurred())
			// create the directory to place the kubeconfig
			execCommand, err := cli.ContainerExecCreate(ctx, byoHostContainer.ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"sh", "-c", "mkdir ${HOME}/.byoh"},
			})
			Expect(err).ShouldNot(HaveOccurred())
			err = cli.ContainerExecStart(ctx, execCommand.ID, container.ExecStartOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			// copy the kubeconfig
			execCommand, err = cli.ContainerExecCreate(ctx, byoHostContainer.ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"sh", "-c", "cp /root/config ${HOME}/.byoh/"},
			})
			Expect(err).ShouldNot(HaveOccurred())
			err = cli.ContainerExecStart(ctx, execCommand.ID, container.ExecStartOptions{})
			Expect(err).ShouldNot(HaveOccurred())

			// start agent
			output, _, err = runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())

			defer e2e.StreamDockerLog(output, agentLogFile)()
			Consistently(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), "\"msg\"=\"initiated bootstrap kubeconfig flow\"") {
						return false
					}
				}
				return true
			}, time.Second*2).Should(BeTrue())
		})
		It("should not register the BYOHost with the management cluster", func(ctx SpecContext) {
			// start agent
			_, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			byoHostLookupKey := types.NamespacedName{Name: hostName, Namespace: ns.Name}
			createdByoHost := &infrastructurev1beta1.ByoHost{}
			Consistently(func(ctx SpecContext) *infrastructurev1beta1.ByoHost {
				err := k8sClient.Get(ctx, byoHostLookupKey, createdByoHost)
				if err != nil {
					return nil
				}
				return createdByoHost
			}).WithContext(ctx).Should(BeNil())
		})
		It("should create ByoHost CSR in the management cluster", func(ctx SpecContext) {
			// start agent
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()
			Eventually(func(ctx SpecContext) string {
				byohCSR, kerr := getHostAgentCSR(ctx, hostName)
				if kerr != nil {
					return kerr.Error()
				}
				return byohCSR.Name
			}, 10, 1).WithContext(ctx).Should(HavePrefix(registration.ByohCSRNamePrefix + hostName))
		})
		It("should persist private key", func(ctx SpecContext) {
			// start agent
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()
			// exec in container to check the file
			cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
			Expect(err).ShouldNot(HaveOccurred())
			time.Sleep(4 * time.Second)
			response, err := cli.ContainerExecCreate(ctx, byoHostContainer.ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"cat", registration.TmpPrivateKey},
			})
			Expect(err).ShouldNot(HaveOccurred())
			result, err := cli.ContainerExecAttach(ctx, response.ID, container.ExecAttachOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			defer e2e.StreamDockerLog(result, execLogFile)()
			Eventually(func() (done bool) {
				_, err := os.Stat(execLogFile)
				if err == nil {
					data, err := os.ReadFile(execLogFile)
					if err == nil && strings.Contains(string(data), "PRIVATE KEY") {
						return true
					}
				}
				return false
			}).Should(BeTrue())
			Expect(os.Remove(execLogFile)).ShouldNot(HaveOccurred())
		})
		It("should wait for the certificate to be issued", func() {
			// start agent
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()
			Eventually(func() (done bool) {
				_, err := os.Stat(agentLogFile)
				if err == nil {
					data, err := os.ReadFile(agentLogFile)
					if err == nil && strings.Contains(string(data), "\"msg\"=\"waiting for client certificate to be issued\"") {
						return true
					}
				}
				return false
			}, time.Second*4).Should(BeTrue())
		})
		It("should create a new CSR when the previous one was denied", func(ctx SpecContext) {
			// start agent
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()

			By("denying the certificate signing request the agent created on startup")
			var deniedName string
			Eventually(func(ctx SpecContext) error {
				byohCSR, kerr := getHostAgentCSR(ctx, hostName)
				if kerr != nil {
					return kerr
				}
				byohCSR.Status.Conditions = append(byohCSR.Status.Conditions, certv1.CertificateSigningRequestCondition{
					Type:    certv1.CertificateDenied,
					Reason:  "denied",
					Message: "denied",
					Status:  corev1.ConditionTrue,
				})
				_, kerr = clientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, byohCSR.Name, byohCSR, metav1.UpdateOptions{})
				if kerr != nil {
					return kerr
				}
				deniedName = byohCSR.Name
				return nil
			}, time.Second*10, time.Second).WithContext(ctx).Should(Succeed(), "the agent never created a certificate signing request to deny")

			By("waiting for the agent to create a replacement certificate signing request")
			// The agent has no permission to delete the denied request, so
			// recovering from it can only show up as a second, differently
			// named one.
			Eventually(func(ctx SpecContext) []string {
				items, kerr := getHostAgentCSRs(ctx, hostName)
				if kerr != nil {
					return nil
				}
				var replacements []string
				for _, item := range items {
					if item.Name != deniedName {
						replacements = append(replacements, item.Name)
					}
				}
				return replacements
			}, time.Second*60, time.Second).WithContext(ctx).Should(HaveLen(1), "the agent did not replace the denied certificate signing request")
		})
		It("should create kubeconfig if the csr is approved", func(ctx SpecContext) {
			// start agent
			output, _, err := runner.ExecByoDockerHost(byoHostContainer)
			Expect(err).NotTo(HaveOccurred())
			defer e2e.StreamDockerLog(output, agentLogFile)()

			// Approve CSR
			Eventually(func() (done bool) {
				byohCSR, kerr := getHostAgentCSR(ctx, hostName)
				if kerr != nil {
					return false
				}
				byohCSR.Status.Conditions = append(byohCSR.Status.Conditions, certv1.CertificateSigningRequestCondition{
					Type:    certv1.CertificateApproved,
					Reason:  "approved",
					Message: "approved",
					Status:  corev1.ConditionTrue,
				})
				_, err = clientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, byohCSR.Name, byohCSR, metav1.UpdateOptions{})
				return err == nil
			}, time.Second*4).Should(BeTrue())
			// Issue Certificate
			byohCSR, err := getHostAgentCSR(ctx, hostName)
			Expect(err).ShouldNot(HaveOccurred())
			var FakeCert = `
-----BEGIN CERTIFICATE-----
MIIBvzCCAWWgAwIBAgIRAMd7Mz3fPrLm1aFUn02lLHowCgYIKoZIzj0EAwIwIzEh
MB8GA1UEAwwYazNzLWNsaWVudC1jYUAxNjE2NDMxOTU2MB4XDTIxMDQxOTIxNTMz
MFoXDTIyMDQxOTIxNTMzMFowMjEVMBMGA1UEChMMc3lzdGVtOm5vZGVzMRkwFwYD
VQQDExBzeXN0ZW06bm9kZTp0ZXN0MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE
Xd9aZm6nftepZpUwof9RSUZqZDgu7dplIiDt8nnhO5Bquy2jn7/AVx20xb0Xz0d2
XLn3nn5M+lR2p3NlZmqWHaNrMGkwDgYDVR0PAQH/BAQDAgWgMBMGA1UdJQQMMAoG
CCsGAQUFBwMBMAwGA1UdEwEB/wQCMAAwHwYDVR0jBBgwFoAU/fZa5enijRDB25DF
NT1/vPUy/hMwEwYDVR0RBAwwCoIIRE5TOnRlc3QwCgYIKoZIzj0EAwIDSAAwRQIg
b3JL5+Q3zgwFrciwfdgtrKv8MudlA0nu6EDQO7eaJbwCIQDegFyC4tjGPp/5JKqQ
kovW9X7Ook/tTW0HyX6D6HRciA==
-----END CERTIFICATE-----
`
			byohCSR.Status.Certificate = []byte(FakeCert)
			_, err = clientSet.CertificatesV1().CertificateSigningRequests().UpdateStatus(ctx, byohCSR, metav1.UpdateOptions{})
			Expect(err).ShouldNot(HaveOccurred())

			cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
			Expect(err).ShouldNot(HaveOccurred())
			time.Sleep(2 * time.Second)
			response, err := cli.ContainerExecCreate(ctx, byoHostContainer.ID, container.ExecOptions{
				AttachStdin:  false,
				AttachStdout: true,
				AttachStderr: true,
				Cmd:          []string{"cat", "/root/.byoh/config"},
			})
			Expect(err).ShouldNot(HaveOccurred())
			result, err := cli.ContainerExecAttach(ctx, response.ID, container.ExecAttachOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			defer e2e.StreamDockerLog(result, execLogFile)()
			Eventually(func() (done bool) {
				_, err := os.Stat(execLogFile)
				if err == nil {
					data, err := os.ReadFile(execLogFile)
					if err == nil && strings.Contains(string(data), "name: default-cluster") && strings.Contains(string(data), "client-certificate-data:") {
						return true
					}
				}
				return false
			}, time.Second*4).Should(BeTrue())
			Expect(os.Remove(execLogFile)).ShouldNot(HaveOccurred())
		})
	})
})

var _ = Describe("Agent Unit Tests", func() {
	Context("When the handleBootstrap func is called", func() {
		var (
			bootstrapKubeConf *os.File
			err               error
		)
		BeforeEach(func() {
			bootstrapKubeConf, err = os.CreateTemp("", "bootstrap-kubeconfig")
			Expect(err).NotTo(HaveOccurred())
			bootstrapKubeConfig = bootstrapKubeConf.Name()
		})
		AfterEach(func() {
			Expect(os.Remove(bootstrapKubeConf.Name())).ShouldNot(HaveOccurred())
		})
		It("should return if bootstrap kubeconfig is not valid", func(ctx SpecContext) {
			testbootstrapKubeconfigInvalid := []byte(`abc`)

			_, err = bootstrapKubeConf.Write(testbootstrapKubeconfigInvalid)
			Expect(err).NotTo(HaveOccurred())
			err = handleBootstrapFlow(ctx, klogr.New(), "test-host") //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("client config load failed"))
		})
		It("should return if hostName is not valid", func(ctx SpecContext) {
			testbootstrapKubeconfigValid := []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUN5RENDQWJDZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcwQkFRc0ZBREFWTVJNd0VRWURWUVFERXdwcmRXSmwKY201bGRHVnpNQjRYRFRFNU1URXlNREF3TkRrME1sb1hEVEk1TVRFeE56QXdORGswTWxvd0ZURVRNQkVHQTFVRQpBeE1LYTNWaVpYSnVaWFJsY3pDQ0FTSXdEUVlKS29aSWh2Y05BUUVCQlFBRGdnRVBBRENDQVFvQ2dnRUJBTXFRCmN0RUN6QTh5RlN1Vll1cE9VWWdyVG1mUWVLZS85QmFEV2FnYXE3b3c5K0kySXZzZldGdmxyRDhRUXI4c2VhNnEKeGpxN1RWNjdWYjRSeEJhb1lEQSt5STV2SWN1aldVeFVMdW42NGx1M1E2aUMxc2oyVW5tVXBJZGdhelJYWEVrWgp2eEE2RWJBbm94QTArbEJPbjFDWldsMjNJUTRzNzBvMmhaN3dJcC92ZXZCODhSUlJqcXR2Z2M1ZWxzanNibURGCkxTN0wxWnV5ZThjNmdTOTNiUitWalZtU0lmcjFJRXEwNzQ4dElJeVhqQVZDV1BWQ3Z1UDQxTWxmUGMvSlZwWkQKdUQyK3BPNlpZUkVjZEFuT2YyZUQ0L2VMT01La280TDFkU0Z5OUpLTTVQTG5PQzBaazBBWU9kMXZTOERUQWZ4agpYUEVJWThPQllGaGxzeGY0VEU4Q0F3RUFBYU1qTUNFd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRFd0VCCi93UUZNQU1CQWY4d0RRWUpLb1pJaHZjTkFRRUxCUUFEZ2dFQkFIL09ZcTh6eWwxK3pTVG11b3czeUkvMTVQTDEKZGw4aEI3SUtuWk5XbUMvTFRkbS8rbm9oM1NiMUlkUnY2SGtLZy9HVW4wVU11UlVuZ0xoanUzRU80b3pKUFFjWApxdWF4emdtVEtOV0o2RXJEdlJ2V2hHWDBaY2JkQmZaditkb3d5UnF6ZDVubEo0OWhDK05ydEZGUXE2UDA1QlluCjdTZW1ndXFlWG1Yd0lqMlNhKzFEZVI2bFJtOW84c2hBWWpueVRoVUZxYU1uMThrSTNTQU5KNXZrLzNERnJQRU8KQ0tDOUV6Rmt1Mmt1eGcyZE0xMlBiUkdaUTJvMEs2SEVaZ3JySUtUUE95M29jYjhyOU0wYVNGaGpPVi9OcUdBNApTYXVwWFNXNlhmdklpL1VIb0liVTNwTmNzblVKR25RZlF2aXA5NVhLay9ncWNVcittNTB2eGd1bXh0QT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQ==
    server: https://cluster-a.com
  name: cluster-a
contexts:
- context:
    cluster: cluster-a
    namespace: ns-a
    user: user-a
  name: context-a
current-context: context-a
users:
- name: user-a
  user:
    token: mytoken-a
`)
			_, err = bootstrapKubeConf.Write(testbootstrapKubeconfigValid)
			Expect(err).NotTo(HaveOccurred())
			err = handleBootstrapFlow(ctx, klogr.New(), "") //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("kubeconfig generation failed: hostname is not valid"))
		})
	})
	Context("When the certRotation func is called", func() {
		var (
			kubeConfig *os.File
			err        error
		)
		BeforeEach(func() {
			kubeConfig, err = os.CreateTemp("", "bootstrap-kubeconfig")
			Expect(err).NotTo(HaveOccurred())
			bootstrapKubeConfig = kubeConfig.Name()
		})
		AfterEach(func() {
			Expect(os.Remove(bootstrapKubeConfig)).ShouldNot(HaveOccurred())
		})
		It("should return if certificate data is not valid", func(ctx SpecContext) {
			testKubeconfigInvalid := []byte(`
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ZHVtbXkgY2VydGlmaWNhdGU=
    server: https://cluster-a.com
  name: default-cluster
contexts:
- context:
    cluster: default-cluster
    namespace: default
    user: default-auth
  name: default-context
current-context: default-context
kind: Config
preferences: {}
users:
- name: default-auth
  user:
    client-certificate-data: aW5WYWxpZERhdGE=
    client-key-data: aW5WYWxpZERhdGE=
`)
			_, err = kubeConfig.Write(testKubeconfigInvalid)
			Expect(err).NotTo(HaveOccurred())
			var config *restclient.Config
			config, err = registration.LoadRESTClientConfig(bootstrapKubeConfig)
			Expect(err).NotTo(HaveOccurred())
			err = certRotation(ctx, klogr.New(), "test-host", config) //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).ShouldNot(HaveOccurred())
		})
		It("should return if certificate needs rotation", func(ctx SpecContext) {
			testKubeconfig := []byte(`
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUMvakNDQWVhZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcwQkFRc0ZBREFWTVJNd0VRWURWUVFERXdwcmRXSmwKY201bGRHVnpNQjRYRFRJeU1UQXpNVEF5TkRjd05Gb1hEVE15TVRBeU9EQXlORGN3TkZvd0ZURVRNQkVHQTFVRQpBeE1LYTNWaVpYSnVaWFJsY3pDQ0FTSXdEUVlKS29aSWh2Y05BUUVCQlFBRGdnRVBBRENDQVFvQ2dnRUJBSjZvCnpZSnhVUnhnK2VHMkM4TzJOQXZ2OU9KZXIzL0lZTVBqcXh3cTNKMHZEbm02L3lHK0RQNzFxNWo5MzdlWmdMSncKZkI1YVJMMzFjdUw3N2RFUjZXc3UwVGRCRGN3MGJ6TWFEQ1lRT016TGdESnlrYmhlUmNtRkNDb1Z1MHVqaXlSbApyL3U2YllEZVcveWxmeTgxS1RJaWRJU0U1RUhoYjBlYWZyT0htbTY1aEVYSU5CbkF0WmJiUjZXaVk0NGt5SG1KCnh4bXBORGdHRHh4cVJQZ2ZCbDhrS2NkSDYvZSt1b3dPVlJtalY3a3ZsNzdtUi9uOEEvTUM5Ujc3enh0TmVCcXcKYzlmK3hmODRrMXlVT0xSZHM4M1JrNUFHZjB3Q2VjSmxMUmwwakNyNkJvSStZRm8xUWtMSzluQVJKZGtFS0xvbApaVFNkbHRodnBWd0MvTHdHeklzQ0F3RUFBYU5aTUZjd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRFd0VCCi93UUZNQU1CQWY4d0hRWURWUjBPQkJZRUZDUmh3bm1rcW9ncUZwS09ObmNUaFgvV2VvQTlNQlVHQTFVZEVRUU8KTUF5Q0NtdDFZbVZ5Ym1WMFpYTXdEUVlKS29aSWh2Y05BUUVMQlFBRGdnRUJBSnFWOHYxdC9kR0NrV0tXRzhxKwpWUTdqTHNZWkc5dUwyN2x6MVovYXFidUcxMURoemZCUHBxVDVSMHNkNXYwVFJUQ1lpK1JTVCs2UnB6Kzd1bytZCndWWUpBREwrcEE0UExTTGRGb1RUcHBFL1ZXcE5rbE0rZnhKa1U5cU5YUkRpN28xTzR4YWJOQUxRZWRwb3RWaVUKSFAxV3IxT3VNbDI0SW5vNkJYWU5CQ0JjT21mOWV3ZjV4Z2R5T2Z5eEh6WXI0dWJUb1dqT0hlaE1xUnQ1SUVqdApJRGQ2R0V0cG43VWVwdnFZNE50aU91UzFReUZodW13UGc2VmhtTjZZMFRrcjJrY2RYU3RtWVBxYzNGcU12RkRYClhDOTMzMkZrTjNxRWlNUFJvbUw0cDQvbHN2aUpLY0VpUG5iTTBCZzRWV1R0bjFqNWtwTVFNbC9TMU8rRkR6U20KWVlVPQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg==
    server: https://cluster-a.com
  name: default-cluster
contexts:
- context:
    cluster: default-cluster
    namespace: default
    user: default-auth
  name: default-context
current-context: default-context
kind: Config
preferences: {}
users:
- name: default-auth
  user:
    client-certificate-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUNTVENDQVRHZ0F3SUJBZ0lRU0FZM0xBbCs1WTRpY1M5Qy9GTXFRVEFOQmdrcWhraUc5dzBCQVFzRkFEQVYKTVJNd0VRWURWUVFERXdwcmRXSmxjbTVsZEdWek1CNFhEVEl5TVRBek1UQXpNREEwTkZvWERUSXlNVEF6TVRBegpNVFUwTkZvd0x6RVRNQkVHQTFVRUNoTUtZbmx2YURwb2IzTjBjekVZTUJZR0ExVUVBeE1QWW5sdmFEcG9iM04wCk9taHZjM1F3TUZrd0V3WUhLb1pJemowQ0FRWUlLb1pJemowREFRY0RRZ0FFdUl1QlRrSmJMdW5YV21FZ052WTAKbUUyUjFTRzBnNkhvZjVtR2c1OWVNTUQxQUM5a0oyZXZOTmFKZnVUNk92NFhQQ09Hdm91SWVPRE1KcVh4R2s0TQpVYU5HTUVRd0V3WURWUjBsQkF3d0NnWUlLd1lCQlFVSEF3SXdEQVlEVlIwVEFRSC9CQUl3QURBZkJnTlZIU01FCkdEQVdnQlFrWWNKNXBLcUlLaGFTampaM0U0Vi8xbnFBUFRBTkJna3Foa2lHOXcwQkFRc0ZBQU9DQVFFQVFEMk4KZ3c1dklBV012K3VERWxraGp1TzY5WjFadksvUG5TWmdNazhUTGQxYkl6N24yNS9zbzN1WFZBR0t0YnFQRW5pSwp2YUVpeWNTRVhDQWVqbnRSUUQ2WnVPOTFwNlhWWEJzcnppU0NhY0IwM3AvTVdobmVLNCtEWnpBVW1QeS9pMEM4ClZ2R0xiL2dIQU9zNTFldVQ1WU9zNWhVUVJyVlgrOGFHVVhBZHJPZG1YdkJZRnJEYkFuOHJoTkhIMldGNmxENXAKM2drZEhLKzdIT3BnUVlWQ21wMWszSldvRzZwT2JocUEzdmQ3NDVNWEhHR3MxbnR3clFTU2wyOFBmblRjbHJoUQpYZXFXZ0d4RkluczRzN05QcW5qYm41RlZPYWgxRFFWbGdQSFduaFVQeWp2MFBLYjUzQXZaY3A4d0cwRzNPTm5OClRuYWdDRDk5T2NsRmpFM1VmUT09Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0K
    client-key-data: aW5WYWxpZERhdGE=
`)
			_, err = kubeConfig.Write(testKubeconfig)
			Expect(err).NotTo(HaveOccurred())
			var config *restclient.Config
			config, err = registration.LoadRESTClientConfig(bootstrapKubeConfig)
			Expect(err).NotTo(HaveOccurred())
			err = certRotation(ctx, klogr.New(), "test-host", config) //nolint: staticcheck // klogr predates the textlogger migration; see main.go
			Expect(err).ShouldNot(HaveOccurred())
		})
	})
})
