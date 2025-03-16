package service

import (
	"bytes"
	_ "embed" // Required for //go:embed directives
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

const (
	// ByohConfigDir is the relative path to the BYOH config directory
	ByohConfigDir = ".byoh"
	// ByohConfigFile is the name of the BYOH config file
	ByohConfigFile = "config"
	// ByohAgentURL is the URL to download the BYOH agent binary
	ByohAgentURL = "https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/releases/download/v0.5.0/byoh-hostagent-linux-amd64"
	// File permissions
	DefaultDirPerms  = 0755
	DefaultFilePerms = 0644
)

// Embed template files directly into the binary
//
//go:embed templates/agent-wrapper.sh
var agentWrapperTemplate string

//go:embed templates/agent-systemd.sh
var agentSystemdTemplate string

//go:embed templates/agent-service.template
var agentServiceTemplate string

//go:embed templates/agent-start.sh
var agentStartTemplate string

//go:embed templates/agent-dmesg.sh
var agentDmesgTemplate string

// MinimumVersions defines the minimum dependency versions required for BYOH to function correctly
var MinimumVersions = map[string]string{
	"socat":     "1.7.3.3", // Minimum required version
	"conntrack": "1.4.5",   // Minimum required version
	"ebtables":  "1.8.4",   // Minimum required version
}

// AgentConfig holds configuration for the BYOH agent
type AgentConfig struct {
	BinaryPath   string
	HomeDir      string
	Namespace    string
	KubeConfig   string
	LogDirectory string
}

// InstallPrerequisites installs the required packages for BYOH agent
func InstallPrerequisites() error {
	utils.LogInfo("Detecting OS distribution")
	osReleaseBytes, err := os.ReadFile("/etc/os-release")
	if err != nil {
		utils.LogWarn("Could not detect OS distribution: %v", err)
		return nil
	}

	osRelease := string(osReleaseBytes)
	isUbuntu := strings.Contains(strings.ToLower(osRelease), "ubuntu")

	if isUbuntu {
		utils.LogInfo("Detected Ubuntu OS")

		// Check and install prerequisites in a single function
		return installUbuntuPrerequisites()
	}

	utils.LogWarn("Non-Ubuntu OS detected. Prerequisites installation may be required manually.")
	return nil
}

// installUbuntuPrerequisites checks for and installs required packages on Ubuntu
func installUbuntuPrerequisites() error {
	// Check if we're on Ubuntu
	cmd := exec.Command("lsb_release", "-i")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Ubuntu") {
		return fmt.Errorf("not an Ubuntu system or lsb_release failed: %v", err)
	}

	// Install missing packages
	requiredPkgs := []string{"conntrack", "ebtables", "socat"}
	missingPkgs := []string{}

	for _, pkg := range requiredPkgs {
		if !checkPackage(pkg) {
			missingPkgs = append(missingPkgs, pkg)
		}
	}

	if len(missingPkgs) == 0 {
		utils.LogSuccess("All prerequisites are already installed")
		// Verify versions even if packages are installed
		if err := verifyDependencyVersions(); err != nil {
			return err
		}
		return nil
	}

	utils.LogInfo("Installing missing prerequisites: %s", strings.Join(missingPkgs, ", "))
	installCmd := fmt.Sprintf("sudo apt-get update && sudo apt-get install -y %s", strings.Join(missingPkgs, " "))
	cmd = exec.Command("bash", "-c", installCmd)
	output, err = cmd.CombinedOutput()

	if err != nil {
		utils.LogWarn("Failed to install prerequisites: %v\nOutput: %s", err, string(output))
		return err
	}

	utils.LogSuccess("Successfully installed prerequisites")

	// Verify versions after installation
	return verifyDependencyVersions()
}

// verifyDependencyVersions checks that installed dependencies meet minimum version requirements
func verifyDependencyVersions() error {
	var versionErrors []string

	// Define dependency commands and parser functions
	dependencies := map[string]struct {
		command   []string
		parseFunc func(string) string
	}{
		"socat":     {[]string{"socat", "-V"}, parseSocatVersion},
		"conntrack": {[]string{"conntrack", "--version"}, parseConntrackVersion},
		"ebtables":  {[]string{"ebtables", "--version"}, parseEbtablesVersion},
	}

	// Check all dependencies in a loop
	for dep, info := range dependencies {
		cmd := exec.Command(info.command[0], info.command[1:]...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			versionErrors = append(versionErrors, fmt.Sprintf("Failed to check %s version: %v", dep, err))
			continue
		}

		// Parse version using the appropriate function
		version := info.parseFunc(string(output))
		minVersion := MinimumVersions[dep]
		if !isVersionSufficient(dep, version) {
			versionErrors = append(versionErrors,
				fmt.Sprintf("Installed %s version %s does not meet the minimum required version %s",
					dep, version, minVersion))
		} else {
			utils.LogDebug("%s version %s meets minimum requirement",
				dep, version)
		}
	}

	if len(versionErrors) > 0 {
		return fmt.Errorf("dependency version requirements not met:\n%s", strings.Join(versionErrors, "\n"))
	}

	utils.LogSuccess("All dependencies meet minimum version requirements")
	return nil
}

// parseSocatVersion extracts the version from socat's output
func parseSocatVersion(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "socat version") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				return parts[2]
			}
		}
	}
	return "unknown"
}

// parseConntrackVersion extracts the version from conntrack's output
func parseConntrackVersion(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "conntrack v") {
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasPrefix(part, "v") {
					return strings.TrimPrefix(part, "v")
				}
			}
		}
	}
	return "unknown"
}

// parseEbtablesVersion extracts the version from ebtables' output
func parseEbtablesVersion(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "ebtables") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return "unknown"
}

// isVersionSufficient compares installed version against minimum required version
func isVersionSufficient(pkg, installedVersion string) bool {
	if installedVersion == "unknown" {
		return false
	}

	minRequired, exists := MinimumVersions[pkg]
	if !exists {
		return true // No minimum specified
	}

	// Compare version strings using semantic versioning
	installed := parseVersionParts(installedVersion)
	required := parseVersionParts(minRequired)

	// Compare major, minor, patch
	for i := 0; i < 3; i++ {
		if i >= len(installed) {
			// If installed version has fewer parts (e.g., 1.7 vs 1.7.0),
			// treat missing parts as 0
			installed = append(installed, 0)
		}
		if i >= len(required) {
			// If required version has fewer parts, treat missing parts as 0
			required = append(required, 0)
		}

		if installed[i] > required[i] {
			return true
		}
		if installed[i] < required[i] {
			return false
		}
	}

	// If we get here, versions are equal up to the precision specified
	return true
}

// parseVersionParts splits a version string into its numeric components
func parseVersionParts(version string) []int {
	parts := []int{}
	for _, part := range strings.Split(version, ".") {
		num, err := strconv.Atoi(part)
		if err != nil {
			// If we encounter a non-numeric part (like "-dev"), stop parsing
			break
		}
		parts = append(parts, num)
	}
	return parts
}

// checkPackage checks if a package is installed using a more efficient dpkg query
func checkPackage(pkgName string) bool {
	cmd := fmt.Sprintf("dpkg-query -W -f='${Status}' %s 2>/dev/null | grep -q 'install ok installed'", pkgName)
	err := exec.Command("bash", "-c", cmd).Run()
	return err == nil
}

// SetupAgent downloads and prepares the BYOH agent binary
func SetupAgent(byohDir string) (string, error) {
	// Download BYOH agent binary
	agentBinary := filepath.Join(byohDir, "byoh-hostagent-linux-amd64")
	utils.LogInfo("Downloading BYOH agent binary to %s", agentBinary)

	// Use HTTP client to download the binary instead of wget
	utils.LogDebug("Downloading from URL: %s", ByohAgentURL)
	resp, err := http.Get(ByohAgentURL)
	if err != nil {
		return "", fmt.Errorf("failed to download agent binary: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download agent binary: HTTP status %d", resp.StatusCode)
	}

	// Create the output file
	out, err := os.Create(agentBinary)
	if err != nil {
		return "", fmt.Errorf("failed to create agent binary file: %v", err)
	}
	defer out.Close()

	// Copy the response body to the output file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to write agent binary: %v", err)
	}

	// Make the binary executable
	if err := os.Chmod(agentBinary, DefaultDirPerms); err != nil {
		return "", fmt.Errorf("failed to make agent binary executable: %v", err)
	}

	utils.LogSuccess("Successfully downloaded and prepared agent binary")
	return agentBinary, nil
}

// CreateAgentDirectories creates all necessary directories for the agent
func CreateAgentDirectories(config *AgentConfig) error {
	// Create directories with a single function
	dirs := []string{
		config.HomeDir,
		config.LogDirectory,
		filepath.Join(config.HomeDir, ".byoh"),
	}

	for _, dir := range dirs {
		utils.LogDebug("Creating directory: %s", dir)
		if err := os.MkdirAll(dir, DefaultDirPerms); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
	}

	return nil
}

// setupDiagnosticEnvironment creates a diagnostics directory and initializes logging
func setupDiagnosticEnvironment(byohDir string) (string, *os.File) {
	// Create diagnostics directory
	timestamp := time.Now().Format("20060102-150405")
	diagDir := filepath.Join(byohDir, "diagnostics", timestamp)
	if err := os.MkdirAll(diagDir, DefaultDirPerms); err != nil {
		utils.LogError("Failed to create diagnostics directory: %v", err)
		return diagDir, nil
	}

	// Setup diagnostic log file
	logFile, err := os.Create(filepath.Join(diagDir, "diagnostics.log"))
	if err != nil {
		utils.LogError("Failed to create diagnostics log file: %v", err)
		return diagDir, nil
	}

	utils.LogInfo("Collecting agent diagnostics in %s", diagDir)
	return diagDir, logFile
}

// diagnoseAgentBinary checks the binary for issues
func diagnoseAgentBinary(agentBinary string, diagDir string, diagLog *os.File) {
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	logDiag("🔍 Checking agent binary...")
	// Get basic file info
	fileInfo, err := os.Stat(agentBinary)
	if err != nil {
		logDiag("⚠️ Failed to get agent binary info: %v", err)
	} else {
		logDiag("📄 Agent binary path: %s", agentBinary)
		logDiag("📄 Size: %d bytes", fileInfo.Size())
		logDiag("📄 Permissions: %s", fileInfo.Mode().String())
		logDiag("📄 Last modified: %s", fileInfo.ModTime().Format(time.RFC3339))
	}

	// Check file ownership and extended attributes
	statCmd := exec.Command("stat", agentBinary)
	statOutput, err := statCmd.CombinedOutput()
	if err != nil {
		logDiag("⚠️ Failed to get detailed file info: %v", err)
	} else {
		logDiag("📄 Binary file details:\n%s", string(statOutput))
	}

	// Test binary execution
	testCmd := exec.Command(agentBinary, "--version")
	testOutput, err := testCmd.CombinedOutput()
	if err != nil {
		logDiag("⚠️ Agent binary failed to execute: %v", err)
		logDiag("📄 Output: %s", string(testOutput))

		// Get file type for debugging
		fileTypeCmd := exec.Command("file", agentBinary)
		fileTypeOutput, fileTypeErr := fileTypeCmd.CombinedOutput()
		if fileTypeErr != nil {
			logDiag("⚠️ Failed to determine file type: %v", fileTypeErr)
		} else {
			logDiag("📄 File type: %s", string(fileTypeOutput))
		}
	} else {
		logDiag("✅ Agent binary executable: %s", strings.TrimSpace(string(testOutput)))
	}

	// Check for shared library dependencies using ldd
	if strings.Contains(runtime.GOOS, "linux") {
		lddCmd := exec.Command("ldd", agentBinary)
		lddOutput, err := lddCmd.CombinedOutput()
		if err != nil {
			logDiag("⚠️ Failed to check binary dependencies: %v", err)
		} else {
			// Check for any missing libraries (indicated by "not found")
			lddOutputStr := string(lddOutput)
			if strings.Contains(lddOutputStr, "not found") {
				missingLibs := []string{}
				for _, line := range strings.Split(lddOutputStr, "\n") {
					if strings.Contains(line, "not found") {
						missingLibs = append(missingLibs, strings.TrimSpace(line))
					}
				}
				if len(missingLibs) > 0 {
					logDiag("⚠️ Missing shared libraries detected:")
					for _, lib := range missingLibs {
						logDiag("   %s", lib)
					}
				}
			} else {
				logDiag("✅ All shared library dependencies are available")
			}

			// Save full dependency list to diagnostic directory
			if diagLog != nil {
				depFilePath := filepath.Join(diagDir, "dependencies.log")
				if err := os.WriteFile(depFilePath, lddOutput, DefaultFilePerms); err != nil {
					logDiag("⚠️ Failed to save dependency information: %v", err)
				}
			}
		}
	}
}

// diagnoseSystemDependencies checks the system dependencies
func diagnoseSystemDependencies(diagDir string, diagLog *os.File) {
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	// Check and report on dependency versions
	logDiag("🔍 Checking system dependency versions...")

	// Define dependency commands and parser functions
	dependencies := map[string]struct {
		command   []string
		parseFunc func(string) string
	}{
		"socat":     {[]string{"socat", "-V"}, parseSocatVersion},
		"conntrack": {[]string{"conntrack", "--version"}, parseConntrackVersion},
		"ebtables":  {[]string{"ebtables", "--version"}, parseEbtablesVersion},
	}

	// Check each dependency in a loop
	for dep, info := range dependencies {
		checkDepCmd := exec.Command(info.command[0], info.command[1:]...)
		checkOutput, err := checkDepCmd.CombinedOutput()
		if err != nil {
			logDiag("⚠️ Failed to check %s version: %v", dep, err)
			continue
		}

		version := info.parseFunc(string(checkOutput))
		testedVersion := MinimumVersions[dep]

		if version == "unknown" {
			logDiag("⚠️ Could not determine %s version", dep)
		} else if !isVersionSufficient(dep, version) {
			logDiag("⚠️ Installed %s version %s does not meet the minimum required version %s",
				dep, version, testedVersion)
		} else {
			logDiag("✅ %s version %s meets minimum requirement (%s)",
				dep, version, testedVersion)
		}
	}
}

// diagnoseSystemEnvironment checks the system environment
func diagnoseSystemEnvironment(diagDir string, diagLog *os.File) {
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	// Check OS version
	logDiag("🔍 Checking operating system version...")
	osCheckCmd := exec.Command("lsb_release", "-a")
	osCheckOutput, err := osCheckCmd.CombinedOutput()
	if err != nil {
		logDiag("⚠️ Failed to check OS version: %v", err)
	} else {
		outputStr := string(osCheckOutput)
		if !strings.Contains(outputStr, "Ubuntu") {
			logDiag("⚠️ This system is not running Ubuntu")
			logDiag("📄 OS details:\n%s", outputStr)
		} else {
			logDiag("✅ System is running Ubuntu")
			logDiag("📄 OS details:\n%s", outputStr)
		}
	}

	// Check environment variables
	logDiag("🔍 Environment variables that might affect the agent:")
	envVars := []string{}
	for _, env := range os.Environ() {
		if strings.Contains(strings.ToLower(env), "proxy") ||
			strings.Contains(strings.ToLower(env), "path") ||
			strings.Contains(strings.ToLower(env), "home") ||
			strings.Contains(strings.ToLower(env), "kube") {
			logDiag("  %s", env)
			envVars = append(envVars, env)
		}
	}

	// Save environment variables to file
	if diagLog != nil && len(envVars) > 0 {
		envFilePath := filepath.Join(diagDir, "environment.log")
		if err := os.WriteFile(envFilePath, []byte(strings.Join(envVars, "\n")), DefaultFilePerms); err != nil {
			logDiag("⚠️ Failed to save environment variables: %v", err)
		}
	}

	// Check system logs for errors
	logDiag("🔍 Checking system logs for recent errors...")
	// Create and run a dmesg script to capture system errors
	dmesgScript := filepath.Join(diagDir, "check-dmesg.sh")
	if err := os.WriteFile(dmesgScript, []byte(agentDmesgTemplate), 0755); err != nil {
		logDiag("⚠️ Failed to create dmesg check script: %v", err)
	} else {
		dmesgCmd := exec.Command("/bin/bash", dmesgScript)
		dmesgOutput, err := dmesgCmd.CombinedOutput()
		if err != nil {
			logDiag("⚠️ Failed to check system logs: %v", err)
		} else {
			if len(dmesgOutput) > 0 {
				logDiag("⚠️ Recent system errors found in dmesg:")
				logDiag("📄 %s", string(dmesgOutput))

				// Save to file
				dmesgFilePath := filepath.Join(diagDir, "dmesg_errors.log")
				if err := os.WriteFile(dmesgFilePath, dmesgOutput, DefaultFilePerms); err != nil {
					logDiag("⚠️ Failed to save dmesg errors: %v", err)
				}
			} else {
				logDiag("✅ No relevant system errors found in dmesg")
			}
		}
	}
}

// diagnoseConfigurationIssues checks for configuration problems
func diagnoseConfigurationIssues(byohDir string, diagDir string, diagLog *os.File) {
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	// Verify directory permissions
	logDiag("🔍 Checking directory permissions...")
	dirCmd := exec.Command("ls", "-la", byohDir)
	dirOutput, err := dirCmd.CombinedOutput()
	if err != nil {
		logDiag("⚠️ Failed to check directory permissions: %v", err)
	} else {
		logDiag("📄 BYOH directory permissions:\n%s", string(dirOutput))
	}

	// Check if kubeconfig exists and is valid
	kubeconfigPath := filepath.Join(byohDir, "config")
	logDiag("🔍 Checking kubeconfig at %s", kubeconfigPath)

	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		logDiag("⚠️ Kubeconfig does not exist")
	} else {
		// Test kubeconfig access
		kubeCmd := exec.Command("kubectl", "--kubeconfig", kubeconfigPath, "version", "--client")
		kubeOutput, err := kubeCmd.CombinedOutput()
		if err != nil {
			logDiag("⚠️ Kubeconfig validation failed: %v", err)
			logDiag("📄 Output: %s", string(kubeOutput))
		} else {
			logDiag("✅ Kubeconfig validation successful")
		}
	}
}

// logDiagnosticSummary provides a summary of potential issues
func logDiagnosticSummary(diagDir string, diagLog *os.File) {
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	logDiag("\n🔍 DIAGNOSTIC SUMMARY:")
	logDiag("   1. Check detailed logs in %s for more information", diagDir)
	logDiag("   2. Potential issues could be:")
	logDiag("      - Missing or incompatible dependencies (check minimum versions)")
	logDiag("      - Insufficient permissions for the agent binary or service files")
	logDiag("      - Binary compatibility issues (compiled for a different architecture)")
	logDiag("      - Invalid or inaccessible kubeconfig")
	logDiag("      - System security restrictions (SELinux, AppArmor)")
	logDiag("   3. Review the diagnostic data collected above for specific errors")

	utils.LogInfo("Diagnostics complete. Check logs in %s for more information", diagDir)
}

// DiagnoseAgentFailure performs detailed diagnostics when the agent fails to start or terminates unexpectedly
func DiagnoseAgentFailure(byohDir, agentBinary string) error {
	diagDir, diagLog := setupDiagnosticEnvironment(byohDir)
	defer func() {
		if diagLog != nil {
			diagLog.Close()
		}
	}()

	// Helper function for logging to both console and diagnostic file
	logDiag := func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		fmt.Println(message)
		if diagLog != nil {
			fmt.Fprintln(diagLog, message)
		}
	}

	// Run all diagnostic modules
	logDiag("🔎 Starting agent diagnostics...")
	logDiag("📁 Diagnostic data will be saved to: %s", diagDir)

	diagnoseAgentBinary(agentBinary, diagDir, diagLog)
	diagnoseSystemDependencies(diagDir, diagLog)
	diagnoseSystemEnvironment(diagDir, diagLog)
	diagnoseConfigurationIssues(byohDir, diagDir, diagLog)
	logDiagnosticSummary(diagDir, diagLog)

	return fmt.Errorf("agent failed to start properly, diagnostics have been captured to %s", diagDir)
}

// StartAgent starts the BYOH agent using direct execution
func StartAgent(byohDir, agentBinary, namespace string) error {
	utils.LogInfo("Starting BYOH agent via direct execution...")

	// Ensure the debug logger is initialized for diagnostics
	logDir := filepath.Join(byohDir, "logs")
	if err := os.MkdirAll(logDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create log directory: %v", err)
	}

	// Reinitialize loggers to ensure debug logs are available
	if err := utils.InitLoggers(logDir, true); err != nil {
		return fmt.Errorf("failed to initialize diagnostic logs: %v", err)
	}
	utils.LogInfo("Diagnostic logs will be written to: %s/byoh-agent-debug.log", logDir)

	// Verify the binary before attempting to run it
	isValid, verifyMsg := VerifyAgentBinary(agentBinary)
	if !isValid {
		return utils.LogErrorf("agent binary verification failed: %s", verifyMsg)
	} else {
		utils.LogDebug("Binary verification: %s", verifyMsg)
	}

	// Define log file path
	logFilePath := filepath.Join(logDir, "agent.log")
	utils.LogInfo("Agent logs will be written to: %s", logFilePath)

	// Ensure the KUBECONFIG environment variable is set correctly
	kubeConfigPath := filepath.Join(byohDir, "config")
	if _, err := os.Stat(kubeConfigPath); err == nil {
		if err := os.Setenv("KUBECONFIG", kubeConfigPath); err != nil {
			utils.LogWarn("Failed to set KUBECONFIG environment variable: %v", err)
		} else {
			utils.LogDebug("Set KUBECONFIG environment variable to: %s", kubeConfigPath)
		}
	} else {
		utils.LogWarn("Kubeconfig not found at %s, agent may not start correctly", kubeConfigPath)
	}

	// Open log file
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, DefaultFilePerms)
	if err != nil {
		return utils.LogErrorf("failed to open log file: %v", err)
	}
	defer logFile.Close()

	// Prepare command with restart flag to ensure the agent keeps running even if it doesn't find a machine reference
	cmd := exec.Command(agentBinary,
		"--namespace", namespace,
		"--metricsbindaddress", "0") // Disable metrics to avoid port conflicts

	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = byohDir

	// Set HOME environment variable properly to avoid the agent looking for .byoh/.byoh/config
	// The agent looks for $HOME/.byoh/config, so HOME should be the parent of .byoh
	homeDir, err := os.UserHomeDir()
	if err != nil {
		utils.LogWarn("Failed to get user home directory: %v, using fallback", err)
		homeDir = filepath.Dir(byohDir) // Fallback to parent of byohDir
	}
	
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("HOME=%s", homeDir),
		fmt.Sprintf("KUBECONFIG=%s", kubeConfigPath))

	// Set process to a new process group to avoid termination when the parent process exits
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Start the process
	utils.LogDebug("Executing command: %s", cmd.String())
	if err := cmd.Start(); err != nil {
		utils.LogError("Failed to start agent process: %v", err)
		return DiagnoseAgentFailure(byohDir, agentBinary)
	}

	// Wait for a short period to ensure the process doesn't immediately terminate
	utils.LogInfo("Agent process started with PID: %d", cmd.Process.Pid)
	utils.LogInfo("Waiting briefly to ensure agent starts properly...")

	// Wait for 2 seconds to check for immediate failures
	time.Sleep(2 * time.Second)

	// Check if the process is still running
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		utils.LogError("Agent process exited immediately after starting")
		return DiagnoseAgentFailure(byohDir, agentBinary)
	}

	// Create a systemd service to ensure the agent runs persistently
	if err := createSystemdService(byohDir, agentBinary, namespace); err != nil {
		utils.LogWarn("Could not create systemd service: %v - agent may not persist after restart", err)
		utils.LogInfo("Agent is currently running with PID %d, but will not automatically restart if the system reboots", cmd.Process.Pid)
	} else {
		utils.LogSuccess("BYOH Agent systemd service created for persistent operation")
	}

	utils.LogSuccess("BYOH Agent started successfully")
	return nil
}

// createSystemdService creates a systemd service for the BYOH agent
func createSystemdService(byohDir, agentBinary, namespace string) error {
	utils.LogInfo("Creating systemd service for BYOH agent")

	if os.Geteuid() != 0 {
		utils.LogWarn("Not running as root, systemd service creation may need manual intervention")
	}

	// Define paths for all templates
	wrapperScriptPath := filepath.Join(byohDir, "byoh-agent-wrapper.sh")
	wrapperTemplate := agentWrapperTemplate

	// Get the user's home directory for proper HOME setting
	homeDir, err := os.UserHomeDir()
	if err != nil {
		utils.LogWarn("Failed to get user home directory: %v, using fallback", err)
		homeDir = filepath.Dir(byohDir) // Fallback to parent of byohDir
	}

	// Create the wrapper script with proper parameters
	finalWrapperContent := strings.Replace(wrapperTemplate, "$1", homeDir, -1)
	finalWrapperContent = strings.Replace(finalWrapperContent, "$2", filepath.Join(byohDir, "config"), -1)
	finalWrapperContent = strings.Replace(finalWrapperContent, "$3", agentBinary, -1)
	finalWrapperContent = strings.Replace(finalWrapperContent, "$4", namespace, -1)

	// Write the wrapper script with the replaced values
	if err := os.WriteFile(wrapperScriptPath, []byte(finalWrapperContent), 0755); err != nil {
		return fmt.Errorf("failed to write wrapper script: %v", err)
	}

	utils.LogDebug("Created customized wrapper script at %s", wrapperScriptPath)

	// Use systemd script if available
	systemdServiceTemplate := agentServiceTemplate

	// Replace the placeholders with actual values
	serviceContent := strings.Replace(systemdServiceTemplate, "$1", wrapperScriptPath, -1)
	serviceContent = strings.Replace(serviceContent, "$2", byohDir, -1)

	servicePath := "/etc/systemd/system/byoh-agent.service"

	// Write service content to file - this may require sudo privileges
	err = writeFileWithSudo(servicePath, []byte(serviceContent))
	if err != nil {
		return fmt.Errorf("failed to create systemd service file: %v", err)
	}

	// Use dbus API to enable and start the service
	utils.LogInfo("Using dbus API to enable and start the service...")

	// Connect to the systemd dbus API
	conn, err := dbus.New()
	if err != nil {
		return fmt.Errorf("failed to connect to systemd: %v", err)
	}
	defer conn.Close()

	// Reload systemd to recognize the new service
	if err := conn.Reload(); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	// Enable the service so it starts on boot
	_, _, err = conn.EnableUnitFiles([]string{"byoh-agent.service"}, false, true)
	if err != nil {
		return fmt.Errorf("failed to enable systemd service: %v", err)
	}

	// Start the service
	ch := make(chan string)
	_, err = conn.StartUnit("byoh-agent.service", "replace", ch)
	if err != nil {
		return fmt.Errorf("failed to start systemd service: %v", err)
	}

	// Wait for the result
	result := <-ch
	if result != "done" {
		return fmt.Errorf("failed to start systemd service: unexpected result: %s", result)
	}

	utils.LogSuccess("BYOH Agent systemd service created, enabled, and started")
	return nil
}

// writeFileWithSudo writes content to a file that might require elevated permissions
func writeFileWithSudo(path string, content []byte) error {
	// First try to write directly in case we're already root
	err := os.WriteFile(path, content, 0644)
	if err == nil {
		return nil
	}

	// If direct write fails, try using sudo via a command
	cmd := exec.Command("sudo", "tee", path)
	cmd.Stdin = bytes.NewReader(content)
	cmd.Stdout = nil // Discard output

	return cmd.Run()
}

// StartAgentWithScript starts the BYOH agent using a script (backup method)
func StartAgentWithScript(byohDir, agentBinary, namespace string) error {
	utils.LogInfo("Starting BYOH agent via script method...")

	// Verify binary
	isValid, verifyMsg := VerifyAgentBinary(agentBinary)
	if !isValid {
		utils.LogWarn("Agent binary verification failed: %s", verifyMsg)
	} else {
		utils.LogDebug("Binary verification: %s", verifyMsg)
	}

	// Create logs directory if not exists
	logDir := filepath.Join(byohDir, "logs")
	if err := os.MkdirAll(logDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create logs directory: %v", err)
	}

	// Define paths
	scriptPath := filepath.Join(byohDir, "start-agent.sh")
	agentLogFile := filepath.Join(logDir, "agent.log")

	// Use embedded template
	templateContent := agentStartTemplate

	// Replace parameters in the template
	finalContent := strings.Replace(templateContent, "$1", byohDir, -1)
	finalContent = strings.Replace(finalContent, "$2", agentBinary, -1)
	finalContent = strings.Replace(finalContent, "$3", namespace, -1)
	finalContent = strings.Replace(finalContent, "$4", agentLogFile, -1)

	// Write the customized script to the destination
	if err := os.WriteFile(scriptPath, []byte(finalContent), 0755); err != nil {
		return fmt.Errorf("failed to write agent startup script: %v", err)
	}

	utils.LogDebug("Created customized agent script at %s", scriptPath)

	// Execute the script
	utils.LogDebug("Starting agent with script: %s", scriptPath)
	cmd := exec.Command(scriptPath)
	cmd.Dir = byohDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%s", byohDir))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to execute agent script: %v\nOutput: %s", err, string(output))
	}

	// Check output
	utils.LogDebug("Agent script output: %s", string(output))

	// Wait to see if the PID file was created
	pidFile := filepath.Join(byohDir, "agent.pid")
	var pid string

	// Try a few times to read the PID file
	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)

		pidBytes, err := os.ReadFile(pidFile)
		if err == nil {
			pid = strings.TrimSpace(string(pidBytes))
			break
		}
	}

	if pid == "" {
		utils.LogWarn("Could not find agent PID, it may not have started correctly")
		return DiagnoseAgentFailure(byohDir, agentBinary)
	}

	utils.LogSuccess("BYOH Agent started successfully with PID: %s", pid)
	return nil
}

// PrepareAgentDirectory creates and prepares the BYOH agent directory
func PrepareAgentDirectory() (string, error) {
	// Create .byoh directory in user's home
	byohDir := filepath.Join(os.Getenv("HOME"), ByohConfigDir)
	utils.LogInfo("Creating directory: %s", byohDir)
	if err := os.MkdirAll(byohDir, DefaultDirPerms); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %v", byohDir, err)
	}

	return byohDir, nil
}

// ConfigureAgent configures the agent with the provided kubeconfig
func ConfigureAgent(byohDir, kubeConfigPath string) error {
	utils.LogInfo("Configuring agent with kubeconfig")

	// Check if the kubeconfig exists and is valid
	if _, err := os.Stat(kubeConfigPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("kubeconfig does not exist at %s", kubeConfigPath)
		}
		return fmt.Errorf("error checking kubeconfig existence: %v", err)
	}

	// Set KUBECONFIG environment variable
	if err := os.Setenv("KUBECONFIG", kubeConfigPath); err != nil {
		utils.LogWarn("Failed to set KUBECONFIG environment variable: %v", err)
	} else {
		utils.LogDebug("Set KUBECONFIG environment variable to %s", kubeConfigPath)
	}

	utils.LogSuccess("Successfully configured agent with kubeconfig at %s", kubeConfigPath)
	return nil
}

// RegisterHostsEntry adds an entry to the hosts file
func RegisterHostsEntry(hostEntry string) error {
	const hostsFile = "/etc/hosts"
	utils.LogInfo("Registering host entry: %s", hostEntry)

	// First check if the entry already exists
	content, err := os.ReadFile(hostsFile)
	if err != nil {
		return fmt.Errorf("failed to read hosts file: %v", err)
	}

	// Check if the entry already exists
	if strings.Contains(string(content), hostEntry) {
		utils.LogInfo("Host entry already exists, skipping")
		return nil
	}

	// Append the entry to the hosts file - try direct write first
	// since we might be running as root
	newContent := append(content, []byte("\n"+hostEntry+"\n")...)
	err = os.WriteFile(hostsFile, newContent, 0644)
	if err == nil {
		utils.LogSuccess("Successfully registered host entry")
		return nil
	}

	// If direct write fails, try using sudo
	utils.LogDebug("Direct write failed, trying with sudo: %v", err)
	cmd := exec.Command("sudo", "tee", "-a", hostsFile)
	cmd.Stdin = strings.NewReader(hostEntry + "\n")
	cmd.Stdout = nil // Discard output

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add hosts entry: %v", err)
	}

	utils.LogSuccess("Successfully registered host entry")
	return nil
}

// VerifyAgentBinary verifies the agent binary is valid and executable
func VerifyAgentBinary(binaryPath string) (bool, string) {
	// Check if file exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return false, fmt.Sprintf("Agent binary does not exist at path: %s", binaryPath)
	}

	// Check executable permission
	fileInfo, err := os.Stat(binaryPath)
	if err != nil {
		return false, fmt.Sprintf("Failed to get file info: %v", err)
	}

	// On Unix systems, check if file is executable
	if runtime.GOOS != "windows" {
		if fileInfo.Mode()&0111 == 0 {
			return false, fmt.Sprintf("Agent binary is not executable: %s", binaryPath)
		}
	}

	// Try to execute the binary with --version to check if it works
	cmd := exec.Command(binaryPath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check file type using 'file' command to provide more diagnostics
		fileCmd := exec.Command("file", binaryPath)
		fileOutput, fileErr := fileCmd.CombinedOutput()
		if fileErr == nil {
			return false, fmt.Sprintf("Binary failed to execute: %v\nOutput: %s\nFile type: %s",
				err, string(output), string(fileOutput))
		}
		return false, fmt.Sprintf("Binary failed to execute: %v\nOutput: %s",
			err, string(output))
	}

	return true, fmt.Sprintf("Agent binary successfully verified: %s", strings.TrimSpace(string(output)))
}
