// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit/cloudinitfakes"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/reconciler"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/cluster-api/util/conditions"
	controllerruntime "sigs.k8s.io/controller-runtime"
)

// dpkg/rpm are the binary names GetOSFamily probes for on PATH; the fake
// LookPath below matches on the binary name, so tests select a family by
// passing one of these two constants.
const (
	familyDebianBinary = "dpkg"
	familyRHELBinary   = "rpm"
)

// fakePackagePuller is a hand-rolled stand-in for the old counterfeiter fake,
// now that PackagePull is a plain func field rather than an interface -
// covers the handful of behaviors these tests actually use: canned errors,
// per-test stubs, and a call count.
type fakePackagePuller struct {
	callCount int
	returnErr error
	PullStub  func(ctx context.Context, ref, destDir string) error
}

func (f *fakePackagePuller) Pull(ctx context.Context, ref, destDir string) error {
	f.callCount++
	if f.PullStub != nil {
		return f.PullStub(ctx, ref, destDir)
	}
	return f.returnErr
}

func (f *fakePackagePuller) PullCallCount() int    { return f.callCount }
func (f *fakePackagePuller) PullReturns(err error) { f.returnErr = err }

// upgradeTestReconciler bundles a HostReconciler wired for agent-upgrade
// tests with direct access to its fakes, so each test can configure
// behavior and assert call counts/args without reaching through the
// package-level suite vars used by the Ginkgo specs in reconciler_test.go.
type upgradeTestReconciler struct {
	r             *reconciler.HostReconciler
	cmdRunner     *cloudinitfakes.FakeICmdRunner
	packagePuller *fakePackagePuller
	exitCallCount int
}

func newUpgradeTestReconciler(familyBinary string) *upgradeTestReconciler {
	u := &upgradeTestReconciler{
		cmdRunner:     &cloudinitfakes.FakeICmdRunner{},
		packagePuller: &fakePackagePuller{},
	}
	u.r = &reconciler.HostReconciler{
		Client:              k8sClient,
		CmdRunner:           u.cmdRunner,
		FileWriter:          &cloudinitfakes.FakeIFileWriter{},
		TemplateParser:      &cloudinitfakes.FakeITemplateParser{},
		PackagePull:         u.packagePuller.Pull,
		Exit:                func() { u.exitCallCount++ },
		Recorder:            record.NewFakeRecorder(32),
		SkipK8sInstallation: true,
		LookPath: func(bin string) (string, error) {
			if bin == familyBinary {
				return "/usr/bin/" + bin, nil
			}
			return "", os.ErrNotExist
		},
	}
	return u
}

// setupUpgradeCase pins the running agent version to "v-running" (every case
// below compares against this), creates a fresh upgradeTestReconciler for
// familyBinary, and creates hostName in the API server with DesiredAgent set
// to desired (nil leaves it unset, matching a host with no upgrade
// assigned). Case-specific behavior - PullStub/PullReturns/RunCmdReturns -
// is still configured by each test, since that's where they genuinely
// differ; this only removes the setup steps that were identical everywhere.
func setupUpgradeCase(t *testing.T, familyBinary, hostName string, desired *infrastructurev1beta1.DesiredAgentSpec) (*upgradeTestReconciler, *infrastructurev1beta1.ByoHost, types.NamespacedName) {
	t.Helper()
	setRunningVersion(t, "v-running")
	u := newUpgradeTestReconciler(familyBinary)
	byoHost, key := newByoHostInAPIServer(t, hostName)
	if desired != nil {
		byoHost.Spec.DesiredAgent = desired
		require.NoError(t, k8sClient.Update(t.Context(), byoHost))
	}
	return u, byoHost, key
}

// writeFixtureArtifact drops a file with the given content into destDir,
// standing in for what a real `imgpkg pull` would extract there.
func writeFixtureArtifact(t *testing.T, destDir, name, content string) string {
	t.Helper()
	path := filepath.Join(destDir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func sha256Of(content string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}

// setRunningVersion pins version.Get().GitVersion for the duration of t, so
// executeAgentUpgrade's convergence check has a known value to compare
// against.
func setRunningVersion(t *testing.T, v string) {
	t.Helper()
	original := version.GitVersion
	version.GitVersion = v
	t.Cleanup(func() { version.GitVersion = original })
}

func TestHostReconciler_ExecuteAgentUpgrade_NoOpWhenUnset(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-unset", nil)

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	assert.Equal(t, 0, u.packagePuller.PullCallCount())
	assert.Equal(t, 0, u.exitCallCount)
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	assert.False(t, conditions.Has(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded))
}

func TestHostReconciler_ExecuteAgentUpgrade_MarksSucceededOnceConverged(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-converged",
		&infrastructurev1beta1.DesiredAgentSpec{Version: "v-running"})

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	assert.Equal(t, 0, u.packagePuller.PullCallCount())
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	assert.True(t, conditions.IsTrue(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded))
}

func TestHostReconciler_ExecuteAgentUpgrade_WaitsForPackageURL(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-waiting",
		&infrastructurev1beta1.DesiredAgentSpec{Version: "v-new"})

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	assert.Equal(t, 0, u.packagePuller.PullCallCount())
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	assert.False(t, conditions.Has(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded))
}

func TestHostReconciler_ExecuteAgentUpgrade_PullFailure(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-pull-failed",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	u.packagePuller.PullReturns(assert.AnError)

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.Error(t, err)

	assert.Equal(t, 0, u.cmdRunner.RunCmdCallCount())
	assert.Equal(t, 0, u.exitCallCount)
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	cond := conditions.Get(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.AgentPackagePullFailedReason, cond.Reason)
}

func TestHostReconciler_ExecuteAgentUpgrade_EmptyBundle(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-empty-bundle",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	u.packagePuller.PullStub = func(_ context.Context, _, _ string) error { return nil } // empty bundle

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.Error(t, err)

	assert.Equal(t, 0, u.cmdRunner.RunCmdCallCount())
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	cond := conditions.Get(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.PackageBundleInvalidReason, cond.Reason)
}

func TestHostReconciler_ExecuteAgentUpgrade_AmbiguousBundle(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-ambiguous-bundle",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	u.packagePuller.PullStub = func(_ context.Context, _, destDir string) error {
		writeFixtureArtifact(t, destDir, "agent-a.deb", "a")
		writeFixtureArtifact(t, destDir, "agent-b.deb", "b")
		return nil
	}

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.Error(t, err)

	assert.Equal(t, 0, u.cmdRunner.RunCmdCallCount())
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	cond := conditions.Get(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.PackageBundleInvalidReason, cond.Reason)
}

func TestHostReconciler_ExecuteAgentUpgrade_ChecksumMismatch(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-checksum-mismatch",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:         "v-new",
			PackageURL:      "registry.example.com/agent-bundle:v-new",
			PackageChecksum: sha256Of("different content"),
		})
	u.packagePuller.PullStub = func(_ context.Context, _, destDir string) error {
		writeFixtureArtifact(t, destDir, "agent.deb", "real content")
		return nil
	}

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.Error(t, err)

	assert.Equal(t, 0, u.cmdRunner.RunCmdCallCount())
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	cond := conditions.Get(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.PackageChecksumMismatchReason, cond.Reason)
}

func TestHostReconciler_ExecuteAgentUpgrade_InstallFailure(t *testing.T) {
	u, byoHost, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-install-failed",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	u.packagePuller.PullStub = func(_ context.Context, _, destDir string) error {
		writeFixtureArtifact(t, destDir, "agent.deb", "content")
		return nil
	}
	u.cmdRunner.RunCmdReturns(assert.AnError)

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.Error(t, err)

	assert.Equal(t, 0, u.exitCallCount)
	require.NoError(t, k8sClient.Get(t.Context(), key, byoHost))
	cond := conditions.Get(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
	require.NotNil(t, cond)
	assert.Equal(t, infrastructurev1beta1.PackageInstallFailedReason, cond.Reason)
}

func TestHostReconciler_ExecuteAgentUpgrade_SuccessDebian(t *testing.T) {
	u, _, key := setupUpgradeCase(t, familyDebianBinary, "upgrade-success-debian",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	var artifactPath string
	u.packagePuller.PullStub = func(_ context.Context, ref, destDir string) error {
		assert.Equal(t, "registry.example.com/agent-bundle:v-new", ref)
		artifactPath = writeFixtureArtifact(t, destDir, "agent.deb", "content")
		return nil
	}

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	require.Equal(t, 1, u.cmdRunner.RunCmdCallCount())
	_, cmd := u.cmdRunner.RunCmdArgsForCall(0)
	assert.Equal(t, fmt.Sprintf("dpkg -i %s", artifactPath), cmd)
	require.Equal(t, 1, u.exitCallCount)
}

func TestHostReconciler_ExecuteAgentUpgrade_SuccessRHEL(t *testing.T) {
	u, _, key := setupUpgradeCase(t, familyRHELBinary, "upgrade-success-rhel",
		&infrastructurev1beta1.DesiredAgentSpec{
			Version:    "v-new",
			PackageURL: "registry.example.com/agent-bundle:v-new",
		})
	var artifactPath string
	u.packagePuller.PullStub = func(_ context.Context, _, destDir string) error {
		artifactPath = writeFixtureArtifact(t, destDir, "agent.rpm", "content")
		return nil
	}

	_, err := u.r.Reconcile(t.Context(), controllerruntime.Request{NamespacedName: key})
	require.NoError(t, err)

	require.Equal(t, 1, u.cmdRunner.RunCmdCallCount())
	_, cmd := u.cmdRunner.RunCmdArgsForCall(0)
	assert.Equal(t, fmt.Sprintf("rpm -Uvh %s", artifactPath), cmd)
	assert.NotContains(t, cmd, "--oldpackage")
	require.Equal(t, 1, u.exitCallCount)
}
