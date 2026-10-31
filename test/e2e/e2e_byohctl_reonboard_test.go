// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/platform9/cluster-api-provider-bringyourownhost/agent/registration"
	infrastructurev1beta1 "github.com/platform9/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	"github.com/platform9/cluster-api-provider-bringyourownhost/common/identity"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	agentStateDirInContainer  = "/root/" + service.ByohConfigDir
	agentStateFileInContainer = agentStateDirInContainer + "/" + registration.StateFileName
	agentKeyFileInContainer   = agentStateDirInContainer + "/" + registration.PrivateKeyFileName

	// byohctlExecTimeout bounds one byohctl invocation, which installs or purges the agent package.
	byohctlExecTimeout = 10 * time.Minute
)

var _ = Describe("When a host re-onboards via byohctl after a decommission [Byohctl] [Reonboard]", func() {
	var (
		ctx              context.Context
		specName         = "byohctl-reonboard"
		namespace        *corev1.Namespace
		cancelWatches    context.CancelFunc
		dockerClient     *dockerclient.Client
		byohostContainer *container.CreateResponse
		hostName         string
		logFile          string
	)

	BeforeEach(func() {
		ctx = suiteCtx

		if byohAgentBundleURLForContainers == "" {
			reason := fmt.Sprintf("no agent bundle available for byohctl's SetupAgent to install "+
				"(neither %s nor a local `make build-host-agent-deb` output was found; see "+
				"ensureLocalAgentBundleRegistry in e2e_agent_bundle_registry.go)", byohAgentBundleURLEnvVar)
			if os.Getenv("CI") != "" {
				// Should never happen in CI -- fail loudly instead of skipping quietly.
				Fail("in CI, this should never happen: " + reason)
			}
			Skip(reason)
		}

		Expect(bootstrapClusterProxy).NotTo(BeNil(), "Invalid argument. bootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(os.MkdirAll(artifactFolder, 0755)).To(Succeed())

		namespace, cancelWatches = setupSpecNamespace(ctx, specName, bootstrapClusterProxy, artifactFolder)

		var err error
		dockerClient, err = dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		hostName = fmt.Sprintf("example-worker-%s", util.RandomString(6))
		logFile = fmt.Sprintf("/tmp/byohctl-reonboard-%s.log", util.RandomString(6))
	})

	It("Should create a new ByoHost with a new suffix and identity on the second onboarding", func() {
		mgmtClient := bootstrapClusterProxy.GetClient()
		firstKubeconfig := generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)

		runner := ByoHostRunner{
			Context:               ctx,
			clusterConName:        clusterConName,
			ByoHostName:           hostName,
			Namespace:             namespace.Name,
			DockerClient:          dockerClient,
			NetworkInterface:      dockerNetworkInterfaceKind,
			Env:                   byoHostRunnerEnv(),
			bootstrapClusterProxy: bootstrapClusterProxy,
			CommandArgs: map[string]string{
				agentFlagBootstrapKubeconfig: byohctlBootstrapConfPath,
			},
			BootstrapKubeconfigData: firstKubeconfig,
		}

		created, err := runner.createDockerContainer()
		Expect(err).NotTo(HaveOccurred())
		byohostContainer = &created
		Expect(dockerClient.ContainerStart(ctx, byohostContainer.ID, container.StartOptions{})).To(Succeed())
		Expect(runner.raiseInotifyInstanceLimit(byohostContainer.ID)).To(Succeed())

		By("Copying byohctl into the host container")
		binConfig := cpConfig{sourcePath: pathToByohctlBinary, destPath: byohctlContainerPath, container: byohostContainer.ID}
		Expect(copyToContainer(ctx, dockerClient, binConfig)).To(Succeed())

		By("Onboarding the host with the first bootstrap kubeconfig")
		Expect(runner.copyKubeconfig(binConfig, container.ListOptions{Filters: filters.NewArgs()})).To(Succeed())
		runByohctlOnboard(ctx, dockerClient, byohostContainer.ID, namespace.Name, logFile)

		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName,
			infrastructurev1beta1.AgentConnected,
		)
		firstHost := waitForByoHost(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName)
		firstSuffix := strings.TrimPrefix(firstHost.Name, hostName+"-")
		Expect(firstSuffix).To(HaveLen(identity.SuffixLength), "ByoHost %s should be named <hostname>-<suffix>", firstHost.Name)
		Expect(firstHost.Spec.Identity).To(Equal("byoh:host:" + namespace.Name + ":" + hostName + "-" + firstSuffix))

		By("Checking the state the agent left on the host")
		assertOnboardingState(ctx, dockerClient, mgmtClient, byohostContainer.ID, firstKubeconfig, firstHost)

		By("Checking an in-place identity change is rejected")
		patchBase := firstHost.DeepCopy()
		firstHost.Spec.Identity += "-changed"
		err = mgmtClient.Patch(ctx, firstHost, ctrlclient.MergeFrom(patchBase))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("identity is immutable"))

		By("Decommissioning the host")
		runByohctl(ctx, dockerClient, byohostContainer.ID, logFile, nil, byohctlContainerPath, "decommission")
		firstHostKey := k8stypes.NamespacedName{Name: patchBase.Name, Namespace: patchBase.Namespace}
		Eventually(func() bool {
			getErr := mgmtClient.Get(ctx, firstHostKey, &infrastructurev1beta1.ByoHost{})
			return apierrors.IsNotFound(getErr)
		}, e2eConfig.GetIntervals(specName, "wait-controllers")...).Should(BeTrue(),
			"expected ByoHost %s to be deleted by decommission", firstHostKey)
		_, exitCode, err := execInContainer(ctx, dockerClient, byohostContainer.ID, []string{"test", "!", "-e", agentStateDirInContainer})
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(BeZero(), "expected %s to be removed by decommission", agentStateDirInContainer)

		By("Planting a leftover state file from an unrelated onboarding")
		plantedState := registration.State{
			Version:          1,
			Phase:            registration.PhasePending,
			BootstrapTokenID: "planted",
			Identity:         "byoh:host:" + namespace.Name + ":" + hostName + "-plant",
			CSRName:          "byoh-csr-" + hostName + "-planted",
		}
		plantedJSON, err := json.Marshal(plantedState)
		Expect(err).NotTo(HaveOccurred())
		output, exitCode, err := execInContainer(ctx, dockerClient, byohostContainer.ID, []string{"mkdir", "-p", agentStateDirInContainer})
		Expect(err).NotTo(HaveOccurred())
		Expect(exitCode).To(BeZero(), "failed to create %s: %s", agentStateDirInContainer, output)
		plantedPath, err := uniqueTempFilePath("planted-state-*.json")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.Remove, plantedPath)
		Expect(os.WriteFile(plantedPath, plantedJSON, 0600)).To(Succeed())
		planted := cpConfig{sourcePath: plantedPath, destPath: agentStateFileInContainer, container: byohostContainer.ID}
		Expect(copyToContainer(ctx, dockerClient, planted)).To(Succeed())

		By("Onboarding the same host with a second bootstrap kubeconfig")
		secondKubeconfig := generateBootstrapKubeconfig(ctx, bootstrapClusterProxy, clusterConName, namespace.Name, hostName)
		runner.BootstrapKubeconfigData = secondKubeconfig
		Expect(runner.copyKubeconfig(binConfig, container.ListOptions{Filters: filters.NewArgs()})).To(Succeed())
		runByohctlOnboard(ctx, dockerClient, byohostContainer.ID, namespace.Name, logFile)

		AssertByoHostConditionsTrue(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName,
			infrastructurev1beta1.AgentConnected,
		)
		secondHost := waitForByoHost(ctx, bootstrapClusterProxy, namespace.Name, hostName, specName)
		secondSuffix := strings.TrimPrefix(secondHost.Name, hostName+"-")
		Expect(secondSuffix).To(HaveLen(identity.SuffixLength), "ByoHost %s should be named <hostname>-<suffix>", secondHost.Name)
		Expect(secondSuffix).NotTo(Equal(firstSuffix), "the second onboarding should get a new suffix")
		Expect(secondHost.UID).NotTo(Equal(firstHost.UID))
		Expect(secondHost.Spec.Identity).To(Equal("byoh:host:" + namespace.Name + ":" + hostName + "-" + secondSuffix))

		By("Checking the planted state file was replaced")
		assertOnboardingState(ctx, dockerClient, mgmtClient, byohostContainer.ID, secondKubeconfig, secondHost)
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			ShowInfo([]string{logFile})
		}
	})

	AfterEach(func() {
		dumpSpecResourcesAndCleanup(ctx, specName, bootstrapClusterProxy, artifactFolder, namespace, cancelWatches, nil, e2eConfig.GetIntervals, skipCleanup)

		if dockerClient != nil && byohostContainer != nil {
			if err := dockerClient.ContainerStop(ctx, byohostContainer.ID, container.StopOptions{}); err != nil {
				Showf("error stopping container %s: %v", byohostContainer.ID, err)
			}
			if err := dockerClient.ContainerRemove(ctx, byohostContainer.ID, container.RemoveOptions{}); err != nil {
				Showf("error removing container %s: %v", byohostContainer.ID, err)
			}
		}

		if logFile != "" {
			if err := os.Remove(logFile); err != nil {
				Showf("error removing file %s: %v", logFile, err)
			}
		}
	})
})

// assertOnboardingState checks that the agent state on the host belongs to the onboarding that
// used bootstrapKubeconfigData and produced byoHost. It never prints the token secret.
func assertOnboardingState(ctx context.Context, dockerClient *dockerclient.Client, mgmtClient ctrlclient.Client, containerID, bootstrapKubeconfigData string, byoHost *infrastructurev1beta1.ByoHost) {
	GinkgoHelper()

	config, err := clientcmd.Load([]byte(bootstrapKubeconfigData))
	Expect(err).NotTo(HaveOccurred())
	Expect(config.AuthInfos).NotTo(BeEmpty())
	var tokenID, tokenSecret string
	for _, authInfo := range config.AuthInfos {
		tokenID, tokenSecret, err = bootstraptoken.GetTokenIDSecretFromBootstrapToken(authInfo.Token)
		Expect(err).NotTo(HaveOccurred())
		break
	}

	raw, exitCode, err := execInContainer(ctx, dockerClient, containerID, []string{"cat", agentStateFileInContainer})
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(BeZero(), "failed to read %s", agentStateFileInContainer)
	Expect(strings.Contains(raw, tokenSecret)).To(BeFalse(), "state file must not hold the bootstrap token secret")

	state := registration.State{}
	Expect(json.Unmarshal([]byte(raw), &state)).To(Succeed(), "state file is not valid JSON")

	csr := &certv1.CertificateSigningRequest{}
	Expect(mgmtClient.Get(ctx, k8stypes.NamespacedName{Name: state.CSRName}, csr)).To(Succeed(),
		"state file names a CSR that does not exist")
	Expect(csr.Status.Certificate).NotTo(BeEmpty(), "CSR %s was not issued", state.CSRName)

	expectedState := registration.State{
		Version:          1,
		Phase:            registration.PhaseIssued,
		BootstrapTokenID: tokenID,
		Identity:         byoHost.Spec.Identity,
		CSRName:          state.CSRName,
		CSRUID:           string(csr.UID),
	}
	Expect(state).To(Equal(expectedState), "state file does not describe this onboarding")

	mode, exitCode, err := execInContainer(ctx, dockerClient, containerID, []string{"stat", "--format=%a", agentKeyFileInContainer})
	Expect(err).NotTo(HaveOccurred())
	Expect(exitCode).To(BeZero(), "failed to stat %s", agentKeyFileInContainer)
	Expect(strings.TrimSpace(mode)).To(Equal("600"))
}

func runByohctlOnboard(ctx context.Context, dockerClient *dockerclient.Client, containerID, namespaceName, logFile string) {
	GinkgoHelper()

	env := []string{
		byohAgentBundleURLEnvVar + "=" + byohAgentBundleURLForContainers,
		byohAgentBundleInsecureEnvVar + "=1",
	}
	runByohctl(ctx, dockerClient, containerID, logFile, env,
		byohctlContainerPath, "onboard",
		"--bootstrap-kubeconfig", byohctlBootstrapConfPath,
		"--namespace", namespaceName,
		"--region", "e2e-byohctl",
	)
}

// runByohctl runs a byohctl command to completion and appends its output to logFile, so a
// failure shows what byohctl printed.
func runByohctl(ctx context.Context, dockerClient *dockerclient.Client, containerID, logFile string, env []string, cmd ...string) {
	GinkgoHelper()

	execCtx, cancel := context.WithTimeout(ctx, byohctlExecTimeout)
	defer cancel()

	output, exitCode, err := execInContainerWithEnv(execCtx, dockerClient, containerID, env, cmd)
	Expect(err).NotTo(HaveOccurred())

	logHandle, openErr := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, DefaultFileMode) // #nosec G304 -- fixed /tmp path owned by the spec
	Expect(openErr).NotTo(HaveOccurred())
	_, writeErr := fmt.Fprintf(logHandle, "$ %s\n%s\n", strings.Join(cmd, " "), output)
	Expect(writeErr).NotTo(HaveOccurred())
	Expect(logHandle.Close()).To(Succeed())

	Expect(exitCode).To(BeZero(), "%s exited with a failure, see %s", strings.Join(cmd, " "), logFile)
}

func execInContainer(ctx context.Context, dockerClient *dockerclient.Client, containerID string, cmd []string) (output string, exitCode int, err error) {
	return execInContainerWithEnv(ctx, dockerClient, containerID, nil, cmd)
}

// execInContainerWithEnv runs cmd in the container, waits for it to finish and returns its
// stdout and stderr together with its exit code.
func execInContainerWithEnv(ctx context.Context, dockerClient *dockerclient.Client, containerID string, env, cmd []string) (output string, exitCode int, err error) {
	execResp, err := dockerClient.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Env:          env,
		Cmd:          cmd,
	})
	if err != nil {
		return "", 0, fmt.Errorf("create exec for %v: %w", cmd, err)
	}

	attached, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("attach to exec of %v: %w", cmd, err)
	}
	defer attached.Close()

	var combined bytes.Buffer
	if _, err = stdcopy.StdCopy(&combined, &combined, attached.Reader); err != nil {
		return "", 0, fmt.Errorf("read output of %v: %w", cmd, err)
	}

	inspect, err := dockerClient.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", 0, fmt.Errorf("inspect exec of %v: %w", cmd, err)
	}

	return combined.String(), inspect.ExitCode, nil
}
