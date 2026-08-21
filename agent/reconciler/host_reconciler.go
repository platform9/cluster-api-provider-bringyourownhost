// Copyright 2021 VMware, Inc. All Rights Reserved.
// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/cloudinit"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/version"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// HostReconciler encapsulates the data/logic needed to reconcile a ByoHost
type HostReconciler struct {
	Client              client.Client
	CmdRunner           cloudinit.ICmdRunner
	FileWriter          cloudinit.IFileWriter
	TemplateParser      cloudinit.ITemplateParser
	Recorder            record.EventRecorder
	SkipK8sInstallation bool
	DownloadPath        string
	// LookPath resolves a binary on PATH, used to detect this host's
	// package manager for agent upgrades (see registration.GetOSFamily).
	// Defaults to exec.LookPath in agent/main.go; overridden in tests so
	// package-family detection doesn't depend on what's actually installed
	// on the machine running the test suite.
	LookPath func(string) (string, error)
	// PackagePull fetches an OCI-packaged artifact for an agent upgrade.
	// Defaults to cloudinit.Pull in agent/main.go; overridden in tests.
	PackagePull func(ctx context.Context, ref, destDir string) error
	// Exit terminates the process after a successful agent upgrade install.
	// Defaults to os.Exit(0) in agent/main.go; overridden in tests so a
	// successful upgrade doesn't kill the test binary.
	Exit func()
	// HeartbeatInterval is the minimum time between LastHeartbeatTime writes,
	// and the interval this reconciler self-requeues on. Wired from the
	// --heartbeat-interval flag in agent/main.go.
	HeartbeatInterval time.Duration
	// MaxBlockingDuration bounds a heartbeat pulse (heartbeat.go) during one
	// kubeadm install/join call; past it, a wedged call reads the same as
	// a dead agent. Wired from --max-blocking-duration in agent/main.go.
	// <= 0 disables pulsing.
	MaxBlockingDuration time.Duration
}

const (
	bootstrapSentinelFile = "/run/cluster-api/bootstrap-success.complete"
	// KubeadmResetCommand is the command to run to force reset/remove nodes' local file system of the files created by kubeadm
	KubeadmResetCommand = "kubeadm reset --force"
)

// Reconcile handles events for the ByoHost that is registered by this agent process
func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Reconcile request received")

	// Fetch the ByoHost instance
	byoHost := &infrastructurev1beta1.ByoHost{}
	err := r.Client.Get(ctx, req.NamespacedName, byoHost)
	if err != nil {
		logger.Error(err, "error getting ByoHost")
		return ctrl.Result{}, err
	}

	helper, _ := patch.NewHelper(byoHost, r.Client)
	defer func() {
		err = helper.Patch(ctx, byoHost)
		if err != nil && reterr == nil {
			logger.Error(err, "failed to patch byohost")
			reterr = err
		}
	}()

	// Check for host cleanup annotation
	hostAnnotations := byoHost.GetAnnotations()
	_, ok := hostAnnotations[infrastructurev1beta1.HostCleanupAnnotation]
	if ok {
		err = r.hostCleanUp(ctx, byoHost)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Handle deleted machines
	if !byoHost.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, byoHost)
	}

	result, err := r.reconcileNormal(ctx, byoHost)
	if err == nil {
		result.RequeueAfter = r.HeartbeatInterval
	}
	return result, err
}

func (r *HostReconciler) reconcileNormal(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger = logger.WithValues("ByoHost", byoHost.Name)
	logger.Info("reconcile normal")

	// NOTE: written using the agent's own (host) clock. The management
	// controller no longer compares this directly against its own clock for
	// liveness -- unsafe under host/management clock skew, see ADR 0001
	// (docs/proposals/0001-clock-skew-resistant-heartbeat-liveness.md).
	//
	// This patches the API server directly (heartbeat.go) rather than
	// stamping byoHost in memory for the deferred Patch above to pick up:
	// that would tie the write to however long the rest of this reconcile
	// takes, which is exactly what startHeartbeatPulse below exists to
	// avoid during a long kubeadm install/join.
	if err := r.patchHeartbeat(ctx, byoHost.Name, byoHost.Namespace); err != nil {
		logger.Error(err, "failed to patch heartbeat")
	}

	if err := r.executeAgentUpgrade(ctx, byoHost); err != nil {
		return ctrl.Result{}, err
	}

	if byoHost.Status.MachineRef == nil {
		logger.Info("Machine ref not yet set")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.WaitingForMachineRefReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	if byoHost.Spec.BootstrapSecret == nil {
		logger.Info("BootstrapDataSecret not ready")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.BootstrapDataSecretUnavailableReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	if !conditions.IsTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded) {
		bootstrapScript, err := r.getBootstrapScript(ctx, byoHost.Spec.BootstrapSecret.Name, byoHost.Spec.BootstrapSecret.Namespace)
		if err != nil {
			logger.Error(err, "error getting bootstrap script")
			r.Recorder.Eventf(byoHost, corev1.EventTypeWarning, "ReadBootstrapSecretFailed", "bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name)
			return ctrl.Result{}, err
		}

		if r.SkipK8sInstallation {
			logger.Info("Skipping installation of k8s components")
		} else if !conditions.IsTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded) {
			if byoHost.Spec.InstallationSecret == nil {
				logger.Info("InstallationSecret not ready")
				conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sInstallationSecretUnavailableReason, clusterv1.ConditionSeverityInfo, "")
				return ctrl.Result{}, nil
			}
			err = r.executeInstallerController(ctx, byoHost)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Event(byoHost, corev1.EventTypeNormal, "InstallScriptExecutionSucceeded", "install script executed")
			conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
		} else {
			logger.Info("install script already executed")
		}

		err = r.cleank8sdirectories(ctx)
		if err != nil {
			logger.Error(err, "error cleaning up k8s directories, please delete it manually for reconcile to proceed.")
			r.Recorder.Event(byoHost, corev1.EventTypeWarning, "CleanK8sDirectoriesFailed", "clean k8s directories failed")
			conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CleanK8sDirectoriesFailedReason, clusterv1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}

		err = r.bootstrapK8sNode(ctx, bootstrapScript, byoHost)
		if err != nil {
			logger.Error(err, "error in bootstrapping k8s node")
			r.Recorder.Event(byoHost, corev1.EventTypeWarning, "BootstrapK8sNodeFailed", "k8s Node Bootstrap failed")
			_ = r.resetNode(ctx, byoHost)
			conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CloudInitExecutionFailedReason, clusterv1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}
		logger.Info("k8s node successfully bootstrapped")
		r.Recorder.Event(byoHost, corev1.EventTypeNormal, "BootstrapK8sNodeSucceeded", "k8s Node Bootstraped")
		conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
	}

	return ctrl.Result{}, nil
}

func (r *HostReconciler) executeInstallerController(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	if byoHost.Spec.InstallationSecret == nil {
		return fmt.Errorf("InstallationSecret not found in ByoHost %s", byoHost.Name)
	}
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      byoHost.Spec.InstallationSecret.Name,
		Namespace: byoHost.Spec.InstallationSecret.Namespace,
	}, secret)
	if err != nil {
		logger.Error(err, "error getting install script")
		r.Recorder.Eventf(byoHost, corev1.EventTypeWarning, "ReadInstallationSecretFailed", "install script %s not found", byoHost.Spec.InstallationSecret.Name)
		return err
	}
	installScriptBytes, ok := secret.Data["install"]
	if !ok {
		return fmt.Errorf("install script not found in secret %s", secret.Name)
	}
	installScript := string(installScriptBytes)
	installScript, err = r.parseScript(ctx, installScript)
	if err != nil {
		return err
	}
	logger.Info("executing install script")
	stopPulse := r.startHeartbeatPulse(ctx, byoHost)
	defer stopPulse()
	err = r.CmdRunner.RunCmd(ctx, installScript)
	if err != nil {
		logger.Error(err, "error executing installation script")
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "InstallScriptExecutionFailed", "install script execution failed")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsInstallationFailedReason, clusterv1.ConditionSeverityInfo, "")
		return err
	}
	return nil
}

// executeAgentUpgrade compares the host's running agent version against
// Spec.DesiredAgent.Version and, on a mismatch, pulls, verifies, and installs
// the package at Spec.DesiredAgent.PackageURL, then re-execs into the newly
// installed binary. See docs/proposals/agent-self-upgrade-adr.md §2.2.
func (r *HostReconciler) executeAgentUpgrade(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)

	desiredAgent := byoHost.Spec.DesiredAgent
	if desiredAgent == nil {
		return nil
	}
	if desiredAgent.Version == version.Get().GitVersion {
		// Converged - clear any stale failure from a prior attempt. A
		// successful upgrade itself never reaches this line directly: it
		// re-execs and never returns, so convergence is only ever observed
		// on the tick after.
		conditions.MarkTrue(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded)
		return nil
	}
	if desiredAgent.PackageURL == "" {
		logger.Info("DesiredAgent.PackageURL not ready")
		return nil
	}

	tempDir, err := os.MkdirTemp("", "byoh-agent-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir) //nolint:errcheck // best-effort cleanup of a temp dir

	artifactPath, family, err := r.fetchAgentUpgradePackage(ctx, byoHost, tempDir)
	if err != nil {
		return err
	}

	installCmd, err := agentPackageInstallCommand(family, artifactPath)
	if err != nil {
		return r.failAgentUpgrade(ctx, byoHost, err, "invalid agent upgrade package bundle",
			"PackageBundleInvalid", infrastructurev1beta1.PackageBundleInvalidReason)
	}

	logger.Info("installing agent upgrade package", "family", family)
	if err = r.CmdRunner.RunCmd(ctx, installCmd); err != nil {
		return r.failAgentUpgrade(ctx, byoHost, err, "error installing agent upgrade package",
			"PackageInstallFailed", infrastructurev1beta1.PackageInstallFailedReason)
	}

	logger.Info("install succeeded, exiting for the process supervisor to relaunch the upgraded binary")
	r.Exit()
	// Unreachable on a real os.Exit(0): it terminates this process
	// immediately and never returns. Only reachable with a stub Exit func in
	// tests.
	return nil
}

func (r *HostReconciler) fetchAgentUpgradePackage(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost, destDir string) (artifactPath, family string, err error) {
	logger := ctrl.LoggerFrom(ctx)

	logger.Info("pulling agent upgrade package", "ref", byoHost.Spec.DesiredAgent.PackageURL)
	if err = r.PackagePull(ctx, byoHost.Spec.DesiredAgent.PackageURL, destDir); err != nil {
		return "", "", r.failAgentUpgrade(ctx, byoHost, err, "error pulling agent upgrade package",
			"AgentPackagePullFailed", infrastructurev1beta1.AgentPackagePullFailedReason)
	}

	family = registration.GetOSFamily(r.LookPath)
	artifactPath, err = findPackageArtifact(destDir, family)
	if err != nil {
		return "", "", r.failAgentUpgrade(ctx, byoHost, err, "invalid agent upgrade package bundle",
			"PackageBundleInvalid", infrastructurev1beta1.PackageBundleInvalidReason)
	}

	if byoHost.Spec.DesiredAgent.PackageChecksum != "" {
		if err = verifyChecksum(artifactPath, byoHost.Spec.DesiredAgent.PackageChecksum); err != nil {
			return "", "", r.failAgentUpgrade(ctx, byoHost, err, "agent upgrade package checksum mismatch",
				"PackageChecksumMismatch", infrastructurev1beta1.PackageChecksumMismatchReason)
		}
	}
	return artifactPath, family, nil
}

// failAgentUpgrade is the shared failure tail for every executeAgentUpgrade
// branch. Always returns err unchanged, so callers can `return
// r.failAgentUpgrade(...)` directly.
func (r *HostReconciler) failAgentUpgrade(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost, err error, logMsg, eventReason, conditionReason string) error {
	ctrl.LoggerFrom(ctx).Error(err, logMsg)
	r.Recorder.Event(byoHost, corev1.EventTypeWarning, eventReason, logMsg)
	conditions.MarkFalse(byoHost, infrastructurev1beta1.AgentUpgradeSucceeded, conditionReason, clusterv1.ConditionSeverityWarning, "")
	return err
}

// findPackageArtifact returns the single *.deb (family ==
// infrastructurev1beta1.HostOSFamilyDebian) or *.rpm (family ==
// infrastructurev1beta1.HostOSFamilyRHEL) file in dir. Zero or more than one
// match is an error — an ambiguous bundle, not something to guess at.
func findPackageArtifact(dir, family string) (string, error) {
	ext := ".rpm"
	if family == infrastructurev1beta1.HostOSFamilyDebian {
		ext = ".deb"
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*"+ext))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one %s file in agent upgrade bundle, found %d", ext, len(matches))
	}
	return matches[0], nil
}

// verifyChecksum checks path's sha256 against expected, formatted
// "sha256:<hex>".
func verifyChecksum(path, expected string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from findPackageArtifact's filepath.Glob() with validated single match
	if err != nil {
		return err
	}
	actual := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// agentPackageInstallCommand returns the fixed install command for family —
// never operator-supplied content, and never includes a force-downgrade
// flag (see docs/proposals/agent-self-upgrade-adr.md §2.4: a downgrade
// failing here is the intended backstop, not a bug to route around).
func agentPackageInstallCommand(family, artifactPath string) (string, error) {
	switch family {
	case infrastructurev1beta1.HostOSFamilyDebian:
		return fmt.Sprintf("dpkg -i %s", artifactPath), nil
	case infrastructurev1beta1.HostOSFamilyRHEL:
		return fmt.Sprintf("rpm -Uvh %s", artifactPath), nil
	default:
		return "", fmt.Errorf("unrecognized package family %q", family)
	}
}

func (r *HostReconciler) reconcileDelete(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *HostReconciler) getBootstrapScript(ctx context.Context, dataSecretName, namespace string) (string, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: dataSecretName, Namespace: namespace}, secret)
	if err != nil {
		return "", err
	}

	bootstrapSecret := string(secret.Data["value"])
	return bootstrapSecret, nil
}

func (r *HostReconciler) parseScript(ctx context.Context, script string) (string, error) {
	data, err := cloudinit.TemplateParser{
		Template: map[string]string{
			"BundleDownloadPath": r.DownloadPath,
		},
	}.ParseTemplate(script)
	if err != nil {
		return "", fmt.Errorf("unable to apply install parsed template to the data object")
	}
	return data, nil
}

// SetupWithManager sets up the controller with the manager
func (r *HostReconciler) SetupWithManager(ctx context.Context, mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		WithEventFilter(predicates.ResourceNotPaused(mgr.GetScheme(), ctrl.LoggerFrom(ctx))).
		Complete(r)
}

// cleanup /run/kubeadm, /etc/cni/net.d dirs to remove any stale config on the host
func (r *HostReconciler) cleank8sdirectories(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)

	dirs := []string{
		"/run/kubeadm/*",
		"/etc/cni/net.d/*",
	}

	errList := make([]error, 0)
	for _, dir := range dirs {
		logger.Info(fmt.Sprintf("cleaning up directory %s", dir))
		if err := common.RemoveGlob(dir); err != nil {
			logger.Error(err, fmt.Sprintf("failed to clean up directory %s", dir))
			errList = append(errList, err)
		}
	}

	if len(errList) > 0 {
		err := errList[0]               //nolint: gosec
		for _, e := range errList[1:] { //nolint: gosec
			err = fmt.Errorf("%w; %v error", err, e)
		}
		return errors.WithMessage(err, "not all k8s directories are cleaned up")
	}
	return nil
}

func (r *HostReconciler) hostCleanUp(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("cleaning up host")

	// Guard kubeadm reset behind the installation condition — only run if k8s was installed.
	// MarkFalse immediately after reset so retries skip reset and only retry the uninstall script.
	k8sComponentsInstallationSucceeded := conditions.Get(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
	if k8sComponentsInstallationSucceeded != nil && k8sComponentsInstallationSucceeded.Status == corev1.ConditionTrue {
		err := r.resetNode(ctx, byoHost)
		if err != nil {
			return err
		}
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")
	} else {
		logger.Info("Skipping k8s node reset")
	}

	// Guard uninstall script behind UninstallationSecret being set — independent of the reset
	// condition so that retries after a failed uninstall still execute the script.
	if !r.SkipK8sInstallation && byoHost.Spec.UninstallationSecret != nil {
		logger.Info("Executing Uninstall script")
		secret := &corev1.Secret{}
		err := r.Client.Get(ctx, types.NamespacedName{
			Name:      byoHost.Spec.UninstallationSecret.Name,
			Namespace: byoHost.Spec.UninstallationSecret.Namespace,
		}, secret)
		if err != nil {
			logger.Error(err, "error getting uninstallation script secret")
			r.Recorder.Eventf(byoHost, corev1.EventTypeWarning, "ReadUninstallationSecretFailed", "uninstallation secret %s not found", byoHost.Spec.UninstallationSecret.Name)
			return err
		}
		uninstallScriptBytes, ok := secret.Data["uninstall"]
		if !ok {
			return fmt.Errorf("uninstall script not found in secret %s", secret.Name)
		}
		uninstallScript := string(uninstallScriptBytes)
		uninstallScript, err = r.parseScript(ctx, uninstallScript)
		if err != nil {
			logger.Error(err, "error parsing Uninstallation script")
			return err
		}
		err = r.CmdRunner.RunCmd(ctx, uninstallScript)
		if err != nil {
			logger.Error(err, "error executing Uninstallation script")
			r.Recorder.Event(byoHost, corev1.EventTypeWarning, "UninstallScriptExecutionFailed", "uninstall script execution failed")
			return err
		}
		logger.Info("host removed from the cluster and the uninstall is executed successfully")
	} else if r.SkipK8sInstallation {
		logger.Info("Skipping uninstallation of k8s components")
	}
	conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")

	err := r.removeSentinelFile(ctx, byoHost)
	if err != nil {
		return err
	}

	err = r.deleteEndpointIP(ctx, byoHost)
	if err != nil {
		return err
	}

	byoHost.Spec.InstallationSecret = nil
	r.removeAnnotations(ctx, byoHost)
	conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")
	return nil
}

func (r *HostReconciler) resetNode(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Running kubeadm reset")

	err := r.CmdRunner.RunCmd(ctx, KubeadmResetCommand)
	if err != nil {
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "ResetK8sNodeFailed", "k8s Node Reset failed")
		return errors.Wrapf(err, "failed to exec kubeadm reset")
	}
	logger.Info("Kubernetes Node reset completed")
	r.Recorder.Event(byoHost, corev1.EventTypeNormal, "ResetK8sNodeSucceeded", "k8s Node Reset completed")
	return nil
}

func (r *HostReconciler) bootstrapK8sNode(ctx context.Context, bootstrapScript string, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Bootstraping k8s Node")
	stopPulse := r.startHeartbeatPulse(ctx, byoHost)
	defer stopPulse()
	return cloudinit.ScriptExecutor{
		WriteFilesExecutor:    r.FileWriter,
		RunCmdExecutor:        r.CmdRunner,
		ParseTemplateExecutor: r.TemplateParser}.Execute(bootstrapScript)
}

func (r *HostReconciler) removeSentinelFile(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing the bootstrap sentinel file")
	if _, err := os.Stat(bootstrapSentinelFile); !os.IsNotExist(err) {
		err := os.Remove(bootstrapSentinelFile)
		if err != nil {
			return errors.Wrapf(err, "failed to delete sentinel file %s", bootstrapSentinelFile)
		}
	}
	return nil
}

func (r *HostReconciler) deleteEndpointIP(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing network endpoints")
	if IP, ok := byoHost.Annotations[infrastructurev1beta1.EndPointIPAnnotation]; ok {
		return deleteIP(IP, registration.LocalHostRegistrar.ByoHostInfo.DefaultNetworkInterfaceName)
	}
	return nil
}

func (r *HostReconciler) removeAnnotations(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing annotations")
	// Remove host reservation
	byoHost.Status.MachineRef = nil

	// Remove BootstrapSecret
	byoHost.Spec.BootstrapSecret = nil

	// Remove UninstallationSecret reference (secret deletion is handled manager-side)
	//
	// FIXME: Currently we cleanup uninstallation secret from the management plane controller.
	// This means we have split-ownership of cleanup between the agent and the management plane controller.
	// We should ensure agent's boundary for cleanup remains within the host itself and it should not be modifying the ByoHost CR at all. The management of the ByoHost CR is the management plane's responsibility, until the host decides to "deboard". And even then it may only ask the management plane to initiate the cleanup of the CR, but not do so itself.
	// byoHost.Spec.UninstallationSecret = nil

	// Remove cluster-name label
	delete(byoHost.Labels, clusterv1.ClusterNameLabel)

	// Remove Byomachine-name label
	delete(byoHost.Labels, infrastructurev1beta1.AttachedByoMachineLabel)

	// Remove the EndPointIP annotation
	delete(byoHost.Annotations, infrastructurev1beta1.EndPointIPAnnotation)

	// Remove the cleanup annotation
	delete(byoHost.Annotations, infrastructurev1beta1.HostCleanupAnnotation)

	// Remove the cluster version annotation
	delete(byoHost.Annotations, infrastructurev1beta1.K8sVersionAnnotation)

	// Remove the bundle registry annotation
	delete(byoHost.Annotations, infrastructurev1beta1.BundleLookupBaseRegistryAnnotation)
}
