// Package service handles the BYOH agent service management
package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

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
	// Check and install prerequisites
	utils.LogInfo("Checking for prerequisites: conntrack, ebtables, socat")
	
	// Function to check if a package is installed
	checkPackage := func(pkgName string) bool {
		cmd := fmt.Sprintf("dpkg -s %s >/dev/null 2>&1", pkgName)
		err := exec.Command("bash", "-c", cmd).Run()
		return err == nil
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
		return nil
	}
	
	utils.LogInfo("Installing missing prerequisites: %s", strings.Join(missingPkgs, ", "))
	installCmd := fmt.Sprintf("sudo apt-get update && sudo apt-get install -y %s", strings.Join(missingPkgs, " "))
	cmd := exec.Command("bash", "-c", installCmd)
	output, err := cmd.CombinedOutput()
	
	if err != nil {
		utils.LogWarn("Failed to install prerequisites: %v\nOutput: %s", err, string(output))
		return err
	} 
	
	utils.LogSuccess("Successfully installed prerequisites")
	return nil
}

// SetupAgent downloads and prepares the BYOH agent binary
func SetupAgent(byohDir string) (string, error) {
	// Download BYOH agent binary
	agentBinary := filepath.Join(byohDir, "byoh-hostagent-linux-amd64")
	utils.LogInfo("Downloading BYOH agent binary to %s", agentBinary)
	
	// Use wget to download the binary
	downloadCmd := fmt.Sprintf("wget -O %s %s", agentBinary, ByohAgentURL)
	cmd := exec.Command("bash", "-c", downloadCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to download agent binary: %v\nOutput: %s", err, string(output))
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

// CopyAgentConfig copies the kubeconfig to all necessary locations
func CopyAgentConfig(config *AgentConfig) error {
	// Source kubeconfig path
	sourceConfig := config.KubeConfig
	
	// Target paths
	mainConfigPath := filepath.Join(config.HomeDir, "kubeconfig.yaml")
	nestedConfigPath := filepath.Join(config.HomeDir, ".byoh", "config")
	
	// Read the content once
	utils.LogDebug("Reading kubeconfig: %s", sourceConfig)
	content, err := os.ReadFile(sourceConfig)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig: %v", err)
	}
	
	// Write to both locations
	for _, path := range []string{mainConfigPath, nestedConfigPath} {
		utils.LogDebug("Writing config to: %s", path)
		if err := os.WriteFile(path, content, DefaultFilePerms); err != nil {
			utils.LogWarn("Failed to write config to %s: %v", path, err)
		}
	}
	
	return nil
}

// VerifyAgentBinary verifies the agent binary is valid and executable
func VerifyAgentBinary(binaryPath string) (bool, string) {
	utils.LogDebug("Verifying agent binary: %s", binaryPath)
	
	// Check if file exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return false, fmt.Sprintf("binary does not exist: %v", err)
	}
	
	// Test with --version flag
	versionCmd := exec.Command(binaryPath, "--version")
	versionOutput, err := versionCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("binary verification failed: %v\nOutput: %s", err, string(versionOutput))
	}
	
	// Check file type
	fileTypeCmd := exec.Command("file", "--brief", "--mime-type", binaryPath)
	typeOutput, err := fileTypeCmd.CombinedOutput()
	if err != nil {
		utils.LogDebug("File type check failed: %v", err)
	} else {
		mimeType := strings.TrimSpace(string(typeOutput))
		if mimeType != "application/x-executable" {
			return false, fmt.Sprintf("binary has incorrect file type: %s", mimeType)
		}
	}
	
	return true, string(versionOutput)
}

// DiagnoseAgentFailure performs detailed diagnostics when the agent fails to start or terminates unexpectedly
func DiagnoseAgentFailure(byohDir, agentBinary string) error {
	utils.LogInfo("Diagnosing agent startup failure...")
	
	// Check binary existence and permissions
	isValid, verifyMsg := VerifyAgentBinary(agentBinary)
	if !isValid {
		return utils.LogErrorf("binary verification failed: %s", verifyMsg)
	}
	
	// Try running the binary with --version flag to check if it works at all
	cmd := exec.Command(agentBinary, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		utils.LogError("Agent binary fails to execute with --version flag: %v", err)
		utils.LogInfo("Agent binary output: %s", string(output))
	} else {
		utils.LogInfo("Agent binary executes successfully with --version flag: %s", string(output))
	}
	
	// Check binary file type and dependencies
	fileTypeCmd := exec.Command("file", agentBinary)
	fileTypeOutput, err := fileTypeCmd.CombinedOutput()
	if err != nil {
		utils.LogWarn("Unable to check file type: %v", err)
	} else {
		utils.LogInfo("Agent binary file type: %s", string(fileTypeOutput))
	}
	
	// On Linux, check binary dependencies
	if runtime.GOOS == "linux" {
		lddCmd := exec.Command("ldd", agentBinary)
		lddOutput, err := lddCmd.CombinedOutput()
		if err != nil {
			utils.LogWarn("Unable to check binary dependencies: %v", err)
		} else {
			// Check for missing dependencies in the output
			lddString := string(lddOutput)
			if strings.Contains(lddString, "not found") {
				utils.LogError("Agent binary has missing dependencies: %s", lddString)
			} else {
				utils.LogInfo("Agent binary dependencies look good")
			}
		}
		
		// Try using strace to trace system calls
		utils.LogInfo("Running strace on agent binary for system call diagnostics...")
		straceCmd := exec.Command("strace", "-f", "-e", "trace=file,process", agentBinary, "--version")
		straceOutput, err := straceCmd.CombinedOutput()
		if err != nil {
			// Some error is expected since we're interrupting the process
			utils.LogInfo("Strace diagnostics (error code %v): %s", err, string(straceOutput))
		} else {
			utils.LogInfo("Strace diagnostics: %s", string(straceOutput))
		}
		
		// Check system logs for any relevant errors
		dmesgCmd := exec.Command("sh", "-c", "dmesg | grep -i error | tail -n 20")
		dmesgOutput, _ := dmesgCmd.CombinedOutput()
		if len(dmesgOutput) > 0 {
			utils.LogInfo("Recent system errors: %s", string(dmesgOutput))
		}
	}
	
	// Check environment variables
	utils.LogInfo("Environment variables that might affect the agent:")
	for _, env := range os.Environ() {
		if strings.Contains(strings.ToLower(env), "proxy") || 
		   strings.Contains(strings.ToLower(env), "path") || 
		   strings.Contains(strings.ToLower(env), "home") {
			utils.LogInfo("  %s", env)
		}
	}
	
	// Verify directory permissions
	logDir := filepath.Join(byohDir, "logs")
	confDir := filepath.Join(byohDir, "conf")
	
	for _, dir := range []string{byohDir, logDir, confDir} {
		info, err := os.Stat(dir)
		if err != nil {
			utils.LogError("Cannot access directory %s: %v", dir, err)
		} else {
			mode := info.Mode()
			utils.LogInfo("Directory %s exists with permissions: %v", dir, mode.String())
		}
	}
	
	// Look for log files that might have been created
	utils.LogInfo("Checking for existing log files...")
	logFiles, err := filepath.Glob(filepath.Join(logDir, "*.log"))
	if err != nil {
		utils.LogWarn("Unable to check log files: %v", err)
	} else {
		for _, logFile := range logFiles {
			utils.LogInfo("Found log file: %s", logFile)
			
			// Read and display the last few lines of each log file
			file, err := os.Open(logFile)
			if err != nil {
				utils.LogWarn("Unable to open log file %s: %v", logFile, err)
				continue
			}
			defer file.Close()
			
			// Get file size
			info, err := file.Stat()
			if err != nil {
				utils.LogWarn("Unable to get file info for %s: %v", logFile, err)
				continue
			}
			
			// Read the last 2KB of the file
			size := info.Size()
			maxReadSize := int64(2048)
			var seekStart int64 = 0
			if size > maxReadSize {
				seekStart = size - maxReadSize
			}
			
			_, err = file.Seek(seekStart, io.SeekStart)
			if err != nil {
				utils.LogWarn("Unable to seek in log file %s: %v", logFile, err)
				continue
			}
			
			content := make([]byte, maxReadSize)
			n, err := file.Read(content)
			if err != nil && err != io.EOF {
				utils.LogWarn("Unable to read log file %s: %v", logFile, err)
				continue
			}
			
			if n > 0 {
				logContent := string(content[:n])
				if seekStart > 0 {
					logContent = fmt.Sprintf("... (first %d bytes omitted) ...\n%s", seekStart, logContent)
				}
				utils.LogInfo("Log file content (last 2KB):\n%s", logContent)
			}
		}
	}
	
	utils.LogInfo("Agent diagnostics complete. Please review the above information for clues about the agent failure.")
	return nil
}

// captureAgentDiagnostics runs diagnostics when agent fails to start properly
func captureAgentDiagnostics(byohDir, agentBinary string) error {
	utils.LogWarn("BYOH Agent failed to start properly. Capturing diagnostics...")
	
	if err := DiagnoseAgentFailure(byohDir, agentBinary); err != nil {
		utils.LogError("Failed to complete diagnostics: %v", err)
	}
	
	utils.LogInfo("Check logs in %s/logs for more information", byohDir)
	utils.LogInfo("For more assistance, please collect the above diagnostics and contact support")
	
	return fmt.Errorf("agent failed to start properly, diagnostics have been captured")
}

// StartAgent starts the BYOH agent using direct execution
func StartAgent(byohDir, agentBinary, namespace string) error {
	utils.LogInfo("Starting BYOH agent via direct execution...")
	
	// Verify the binary before attempting to run it
	isValid, verifyMsg := VerifyAgentBinary(agentBinary)
	if !isValid {
		return utils.LogErrorf("agent binary verification failed: %s", verifyMsg)
	} else {
		utils.LogDebug("Binary verification: %s", verifyMsg)
	}
	
	// Define log file path
	logDir := filepath.Join(byohDir, "logs")
	if err := os.MkdirAll(logDir, DefaultDirPerms); err != nil {
		return utils.LogErrorf("failed to create log directory: %v", err)
	}
	
	logFilePath := filepath.Join(logDir, "agent.log")
	utils.LogInfo("Agent logs will be written to: %s", logFilePath)
	
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
	
	// Set process to a new process group to avoid termination when the parent process exits
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	
	// Start the process
	utils.LogDebug("Executing command: %s", cmd.String())
	if err := cmd.Start(); err != nil {
		return captureAgentDiagnostics(byohDir, agentBinary)
	}
	
	// Wait for a short period to ensure the process doesn't immediately terminate
	utils.LogInfo("Agent process started with PID: %d", cmd.Process.Pid)
	utils.LogInfo("Waiting briefly to ensure agent starts properly...")
	
	// Don't wait for the process to complete since it should run in the background
	// Instead, wait for a few seconds to ensure it doesn't immediately crash
	time.Sleep(2 * time.Second)
	
	// Check if the process is still running
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return captureAgentDiagnostics(byohDir, agentBinary)
	}
	
	// Create a systemd service to ensure the agent runs persistently
	if err := createSystemdService(byohDir, agentBinary, namespace); err != nil {
		utils.LogWarn("Could not create systemd service: %v - agent may not persist after restart", err)
	}
	
	utils.LogSuccess("BYOH Agent started successfully")
	return nil
}

// createSystemdService creates a systemd service file to ensure the agent runs persistently
func createSystemdService(byohDir, agentBinary, namespace string) error {
	// Check if we're running as root or have sudo privileges
	if os.Geteuid() != 0 {
		utils.LogWarn("Not running as root, skipping systemd service creation")
		return fmt.Errorf("not running as root")
	}
	
	// Create a wrapper script to ensure proper environment setup
	wrapperScriptPath := filepath.Join(byohDir, "byoh-agent-wrapper.sh")
	wrapperContent := fmt.Sprintf(`#!/bin/bash
# Wrapper script for BYOH Agent to ensure proper environment setup
export HOME=%s
cd %s
exec %s --namespace %s --metricsbindaddress 0
`, byohDir, byohDir, agentBinary, namespace)

	if err := os.WriteFile(wrapperScriptPath, []byte(wrapperContent), 0755); err != nil {
		return fmt.Errorf("failed to create wrapper script: %v", err)
	}
	
	// Create systemd service file
	serviceContent := fmt.Sprintf(`[Unit]
Description=BYOH Agent Service
After=network.target

[Service]
Type=simple
User=root
ExecStart=%s
Restart=always
RestartSec=10
WorkingDirectory=%s

[Install]
WantedBy=multi-user.target
`, wrapperScriptPath, byohDir)

	servicePath := "/etc/systemd/system/byoh-agent.service"
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to create systemd service file: %v", err)
	}

	// Enable and start the service
	utils.LogInfo("Created systemd service, enabling and starting it...")
	
	// Reload systemd to recognize the new service
	reloadCmd := exec.Command("systemctl", "daemon-reload")
	if err := reloadCmd.Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}
	
	// Enable the service so it starts on boot
	enableCmd := exec.Command("systemctl", "enable", "byoh-agent.service")
	if err := enableCmd.Run(); err != nil {
		return fmt.Errorf("failed to enable systemd service: %v", err)
	}
	
	// Start the service
	startCmd := exec.Command("systemctl", "start", "byoh-agent.service")
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("failed to start systemd service: %v", err)
	}
	
	utils.LogSuccess("BYOH Agent systemd service created, enabled, and started")
	return nil
}

// StartAgentWithScript starts the BYOH agent using a script (backup method)
func StartAgentWithScript(byohDir, agentBinary, namespace string) error {
	// Create startup script
	utils.LogInfo("Starting BYOH agent via script method...")

	// Create all necessary directories
	config := &AgentConfig{
		BinaryPath:   agentBinary,
		HomeDir:      byohDir,
		Namespace:    namespace,
		KubeConfig:   filepath.Join(byohDir, "kubeconfig.yaml"),
		LogDirectory: filepath.Join(byohDir, "logs"),
	}

	// Verify binary
	isValid, verifyMsg := VerifyAgentBinary(agentBinary)
	if !isValid {
		utils.LogWarn("Agent binary verification failed: %s", verifyMsg)
	} else {
		utils.LogDebug("Binary verification: %s", verifyMsg)
	}

	// Create logs directory if not exists
	if err := os.MkdirAll(config.LogDirectory, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create logs directory: %v", err)
	}

	// Define script and log paths
	scriptPath := filepath.Join(byohDir, "start-agent.sh")
	agentLogFile := filepath.Join(config.LogDirectory, "agent.log")

	// Create script content
	scriptContent := fmt.Sprintf(`#!/bin/bash
# BYOH Agent startup script
export HOME=%s
cd %s
%s --namespace %s --metricsbindaddress 0 > %s 2>&1 &
echo $! > %s/agent.pid
exit 0
`, byohDir, byohDir, agentBinary, namespace, agentLogFile, byohDir)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to write agent startup script: %v", err)
	}

	// Execute the script
	utils.LogDebug("Starting agent with script: %s", scriptPath)
	cmd := exec.Command("sh", "-c", scriptPath)
	cmd.Dir = byohDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("HOME=%s", byohDir))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to execute agent script: %v\nOutput: %s", err, string(output))
	}

	// Check if the agent process is running
	utils.LogDebug("Waiting for agent process to start...")
	time.Sleep(2 * time.Second)

	// Read PID from file
	pidFile := filepath.Join(byohDir, "agent.pid")
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		utils.LogDebug("Could not read agent PID file: %v", err)
		return fmt.Errorf("could not read agent PID file after script execution: %v", err)
	}

	pid := strings.TrimSpace(string(pidBytes))
	utils.LogInfo("Agent PID: %s", pid)

	// Check if process exists
	exists, err := utils.ProcessExists(pid)
	if err != nil {
		utils.LogDebug("Error checking agent process: %v", err)
	} else if !exists {
		return captureAgentDiagnostics(byohDir, agentBinary)
	}

	utils.LogSuccess("BYOH Agent started successfully")
	utils.LogDebug("To check agent logs: cat %s", agentLogFile)

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

	// Create agent config
	config := &AgentConfig{
		HomeDir:    byohDir,
		KubeConfig: kubeConfigPath,
	}

	// Copy the kubeconfig to all necessary locations
	if err := CopyAgentConfig(config); err != nil {
		return err
	}

	utils.LogSuccess("Successfully configured agent")
	return nil
}

// RegisterHostsEntry adds an entry to the hosts file
func RegisterHostsEntry(hostEntry string) error {
	utils.LogInfo("Registering host entry: %s", hostEntry)
	cmd := exec.Command("bash", "-c", fmt.Sprintf("echo '%s' | sudo tee -a /etc/hosts", hostEntry))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add hosts entry: %v, output: %s", err, string(output))
	}
	utils.LogSuccess("Successfully registered host entry")
	return nil
}

// CopyKubeconfig copies the kubeconfig to the agent directory
func CopyKubeconfig(byohDir, kubeConfigPath string) error {
	utils.LogInfo("Copying kubeconfig to agent directory")

	// Use the common function for configuration
	config := &AgentConfig{
		HomeDir:    byohDir,
		KubeConfig: kubeConfigPath,
	}

	if err := CopyAgentConfig(config); err != nil {
		return err
	}

	utils.LogSuccess("Successfully copied kubeconfig")
	return nil
}
