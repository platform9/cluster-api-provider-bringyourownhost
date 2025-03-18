// Package service contains BYOH agent setup functions
package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

const (
	// DefaultDirPerms is the default directory permission
	DefaultDirPerms = 0755
	// DefaultFilePerms is the default file permission
	DefaultFilePerms = 0644

	// ByohAgentDebPackageURL is the URL to download the agent package
	ByohAgentDebPackageURL = "quay.io/platform9/byoh-agent-deb:0.1.2"
	// ByohAgentDebPackageFilename is the filename of the agent package
	ByohAgentDebPackageFilename = "pf9-byohost-agent.deb"

	// ByohAgentServiceName is the name of the agent service
	ByohAgentServiceName = "pf9-byohost-agent"
	// ByohAgentConfDir is the directory where agent configuration is stored
	ByohAgentConfDir = "/etc/pf9/pf9-byoh-agent"
	// ByohAgentConfFile is the file where agent configuration is stored
	ByohAgentConfFile = "/etc/systemd/system/pf9-byohost-agent.service.d/10-env.conf"

	// ByohAgentBinaryPath is the path to the BYOH agent binary
	ByohAgentBinaryPath = "/binary/pf9-byoh-hostagent-linux-amd64"

	// ByohAgentKubeconfigPath is the path to the BYOH agent kubeconfig
	ByohAgentKubeconfigPath = "/etc/pf9/pf9-byoh-agent/kubeconfig"

	// ByohAgentLogPath is the path to the BYOH agent log file
	ByohAgentLogPath = "/var/log/pf9/byoh/byoh-agent.log"

	// ByohConfigDir is the directory for BYOH configuration
	ByohConfigDir = ".byoh"
)

// SetupAgent installs the BYOH agent in the host
func SetupAgent(byohDirPath string) (string, error) {
	utils.LogInfo("Setting up BYOH agent")

	// Install all required packages first - this is critical for agent functionality
	utils.LogInfo("Checking and installing required packages...")
	if err := ensureRequiredPackages(); err != nil {
		// Since all packages are important, return an error here
		return "", fmt.Errorf("failed to install required packages: %v", err)
	}

	// Proceed with downloading the agent package
	utils.LogInfo("Downloading agent package...")
	packagePath, err := downloadDebianPackage(byohDirPath)
	if err != nil {
		return "", fmt.Errorf("failed to download Debian package: %v", err)
	}

	// Install the agent package
	utils.LogInfo("Installing BYOH agent package...")
	if err = installDebianPackage(packagePath); err != nil {
		return "", fmt.Errorf("failed to install Debian package: %v", err)
	}

	utils.LogSuccess("Agent setup completed successfully")
	return ByohAgentBinaryPath, nil
}

// ensureLogDirectory creates the log directory if it doesn't exist
func ensureLogDirectory() error {
	logDir := filepath.Dir(ByohAgentLogPath)
	return os.MkdirAll(logDir, DefaultDirPerms)
}

// PrepareAgentDirectory prepares the BYOH agent directory
func PrepareAgentDirectory(byohDir string) error {
	// Create byohDir if it doesn't exist
	if err := os.MkdirAll(byohDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create BYOH directory %s: %v", byohDir, err)
	}
	return nil
}

// ConfigureAgent configures the BYOH agent with the provided namespace
func ConfigureAgent(namespace string, kubeconfig string) error {
	// Create directories and write files in a single operation when possible

	// Create the configuration directory if it doesn't exist
	if err := os.MkdirAll(ByohAgentConfDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create agent configuration directory: %v", err)
	}

	// Create the systemd directory if it doesn't exist
	systemdDir := filepath.Dir(ByohAgentConfFile)
	if err := os.MkdirAll(systemdDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create systemd directory: %v", err)
	}

	// Write the kubeconfig
	kubeconfigPath := filepath.Join(filepath.Dir(ByohAgentConfDir), "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %v", err)
	}
	utils.LogSuccess("Wrote kubeconfig to %s", kubeconfigPath)

	// Create environment file for agent service
	envContent := fmt.Sprintf("NAMESPACE=%s\nBOOTSTRAP_KUBECONFIG=%s\n", namespace, kubeconfigPath)
	if err := os.WriteFile(ByohAgentConfFile, []byte(envContent), DefaultFilePerms); err != nil {
		return fmt.Errorf("failed to write agent environment file: %v", err)
	}
	utils.LogSuccess("Created environment file for the agent service")

	return nil
}

// InstallPrerequisites installs prerequisites for the byoh agent
func InstallPrerequisites() error {
	utils.LogInfo("Installing prerequisites for byoh agent")

	// Create all required directories in one step
	dirsToCreate := []string{
		filepath.Dir(ByohAgentConfFile),
		ByohAgentConfDir,
		filepath.Dir(ByohAgentLogPath),
	}

	for _, dir := range dirsToCreate {
		if err := os.MkdirAll(dir, DefaultDirPerms); err != nil {
			utils.LogWarn("Failed to create directory %s: %v", dir, err)
		}
	}

	// Check and install required packages
	if err := ensureRequiredPackages(); err != nil {
		return fmt.Errorf("failed to install required packages: %v", err)
	}

	return nil
}

// fixBrokenDependencies attempts to fix any broken package dependencies
func fixBrokenDependencies() error {
	cleanCmd := exec.Command("dpkg", "--configure", "-a")
	cleanOutput, cleanErr := cleanCmd.CombinedOutput()
	if cleanErr != nil {
		utils.LogDebug("dpkg --configure -a output: %s", string(cleanOutput))
	}

	// Try the fix-broken install
	fixCmd := exec.Command("apt-get", "--fix-broken", "install", "-y")
	_, fixErr := fixCmd.CombinedOutput()
	if fixErr != nil {
		return fmt.Errorf("failed to fix broken dependencies: %v", fixErr)
	}

	return nil
}

// ensureRequiredPackages checks if required packages are installed and installs them if not
func ensureRequiredPackages() error {
	// Fix broken dependencies once at the beginning if any packages need installation
	needsFixing := false
	for _, pkg := range requiredPackages {
		if !checkPackageInstalled(pkg.packageName) {
			needsFixing = true
			break
		}
	}

	if needsFixing {
		utils.LogInfo("Preparing package system...")
		if err := fixBrokenDependencies(); err != nil {
			utils.LogWarn("Could not fix broken dependencies: %v", err)
			// Continue anyway, we'll try to install packages
		}
	}

	var installationErrors []error
	var installedPackages []string

	for _, pkg := range requiredPackages {
		// Check if package needs to be installed
		needsInstall := false

		// First try PATH lookup (for executable commands)
		_, err := exec.LookPath(pkg.name)
		needsInstall = err != nil

		// If package has a Debian package name, verify it's installed via dpkg
		if pkg.packageName != "" && !checkPackageInstalled(pkg.packageName) {
			needsInstall = true
		}

		// If it's installed but has a version check, verify the version
		if !needsInstall && pkg.versionCheck != nil {
			versionOK, _ := pkg.versionCheck()
			if !versionOK {
				needsInstall = true
			}
		}

		if needsInstall {
			utils.LogInfo("%s", pkg.installMsg)

			// Execute the installation command
			cmd := exec.Command("bash", "-c", pkg.installCmd)

			// Capture output
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			// Run the installation
			err := cmd.Run()
			if err != nil {
				// Get the error message
				errMsg := stderr.String()
				if errMsg == "" {
					errMsg = err.Error()
				}

				// Add the error to our collection
				installationErrors = append(installationErrors,
					fmt.Errorf("failed to install %s: %s", pkg.name, errMsg))

				// Log the error
				utils.LogWarn("Failed to install %s: %v", pkg.name, err)
			} else {
				// Log success
				utils.LogSuccess("%s installed successfully", pkg.name)
				installedPackages = append(installedPackages, pkg.name)
			}

			// Verify installation
			if pkg.packageName != "" && !checkPackageInstalled(pkg.packageName) {
				utils.LogWarn("%s installation verification failed", pkg.name)
				installationErrors = append(installationErrors,
					fmt.Errorf("%s installation verification failed", pkg.name))
			}
		} else {
			// For packages that are already installed, log at debug level to reduce noise
			utils.LogDebug("%s is already installed", pkg.name)
		}
	}

	// If we encountered errors, return a combined error
	if len(installationErrors) > 0 {
		errorMessages := []string{}
		for _, err := range installationErrors {
			errorMessages = append(errorMessages, err.Error())
		}
		return fmt.Errorf("package installation errors: %s", strings.Join(errorMessages, "; "))
	}

	// Only log a message if we actually installed something
	if len(installedPackages) > 0 {
		utils.LogSuccess("All required packages installed successfully")
	}

	return nil
}

var requiredPackages = []struct {
	name         string               // Package name
	installCmd   string               // Installation command
	installMsg   string               // Installation message
	packageName  string               // Debian package name (if different from name)
	versionCheck func() (bool, error) // Optional function to verify version
}{
	{
		name:       "imgpkg",
		installCmd: "curl -s -L https://carvel.dev/install.sh | bash",
		installMsg: "Installing imgpkg using Carvel install script",
	},
	{
		name:        "dpkg",
		installCmd:  "apt-get update && apt-get install -y dpkg",
		installMsg:  "Installing dpkg package manager",
		packageName: "dpkg",
	},
	{
		name:        "ebtables",
		installCmd:  "apt-get update && apt-get install -y ebtables || apt-get install -y --fix-broken && apt-get install -y ebtables || apt-get install -y --reinstall ebtables",
		installMsg:  "Installing ebtables Ethernet bridge filtering",
		packageName: "ebtables",
	},
	{
		name:        "conntrack",
		installCmd:  "apt-get update && apt-get install -y conntrack || apt-get install -y --fix-broken && apt-get install -y conntrack",
		installMsg:  "Installing conntrack network connection tracking",
		packageName: "conntrack",
	},
	{
		name:        "socat",
		installCmd:  "apt-get update && apt-get install -y socat || apt-get install -y --fix-broken && apt-get install -y socat",
		installMsg:  "Installing socat multipurpose relay",
		packageName: "socat",
	},
	{
		name:         "libseccomp2",
		installCmd:   "apt-get install -y --no-install-recommends libseccomp2",
		installMsg:   "Installing libseccomp2 library",
		packageName:  "libseccomp2",
		versionCheck: checkLibseccompVersion,
	},
}

// checkPackageInstalled checks if a Debian package is installed
func checkPackageInstalled(packageName string) bool {
	cmd := exec.Command("dpkg", "-s", packageName)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// checkLibseccompVersion verifies if libseccomp2 meets the version requirement (>= 2.5.1-1ubuntu1~20.04.2)
func checkLibseccompVersion() (bool, error) {
	// Check if libseccomp2 is installed at all
	if !checkPackageInstalled("libseccomp2") {
		return false, nil
	}

	// Get the version of libseccomp2
	cmd := exec.Command("dpkg-query", "-W", "-f=${Version}", "libseccomp2")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to get libseccomp2 version: %v", err)
	}

	version := strings.TrimSpace(string(output))

	// Check if the version is at least 2.5.1-1ubuntu1~20.04.2
	// This is a simplified version check - we need to make sure the major version is at least 2.5
	if len(version) < 3 {
		return false, fmt.Errorf("invalid version format: %s", version)
	}

	// Simple check: 2.5.1 starts with "2.5"
	if !strings.HasPrefix(version, "2.5") {
		return false, nil
	}

	// For a more thorough check, we could parse the version components
	// and compare them numerically, but this simple check should work for now

	return true, nil
}

// runCommandWithOutput runs a command and returns its combined output
func runCommandWithOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// DiagnoseAgent attempts to diagnose issues with the BYOH agent
func DiagnoseAgent() error {
	// Ensure log directory exists
	if err := ensureLogDirectory(); err != nil {
		utils.LogWarn("Failed to create log directory: %v", err)
	} else {
		utils.LogInfo("Log directory exists at %s", filepath.Dir(ByohAgentLogPath))
	}

	// Check if binary exists and is executable
	if _, err := os.Stat(ByohAgentBinaryPath); err != nil {
		utils.LogError("Binary does not exist at %s: %v", ByohAgentBinaryPath, err)

		// Try to find the binary elsewhere (using -type f -executable to filter only executable files)
		findOutput, err := runCommandWithOutput("find", "/", "-name", "pf9-byoh-hostagent-linux-amd64", "-type", "f", "-executable")
		if err == nil && len(findOutput) > 0 {
			utils.LogInfo("Found potential binary locations:\n%s", string(findOutput))
		}
	} else {
		utils.LogSuccess("Binary exists at %s", ByohAgentBinaryPath)

		// Check if it's executable
		if fileInfo, err := os.Stat(ByohAgentBinaryPath); err == nil {
			if fileInfo.Mode()&0111 != 0 {
				utils.LogSuccess("Binary is executable")

				// Try to run with --version to check if it works
				versionOutput, err := runCommandWithOutput(ByohAgentBinaryPath, "--version")
				if err != nil {
					utils.LogError("Binary execution failed: %v", err)
					utils.LogInfo("Output: %s", string(versionOutput))

					// Check dependencies
					lddOutput, _ := runCommandWithOutput("ldd", ByohAgentBinaryPath)
					utils.LogInfo("Binary dependencies:\n%s", string(lddOutput))
				} else {
					utils.LogSuccess("Binary executed successfully with --version")
					utils.LogInfo("Version output: %s", string(versionOutput))
				}
			} else {
				utils.LogError("Binary is not executable, attempting to add execute permission")
				if err := os.Chmod(ByohAgentBinaryPath, 0755); err != nil {
					utils.LogError("Failed to make binary executable: %v", err)
				} else {
					utils.LogSuccess("Made binary executable")
				}
			}
		}
	}

	// Create the config directory if needed
	if err := os.MkdirAll(ByohAgentConfDir, 0755); err != nil {
		utils.LogError("Failed to create config directory: %v", err)
	}

	// Check service unit file
	serviceUnitName := ByohAgentServiceName + ".service"
	unitPathOutput, err := runCommandWithOutput("systemctl", "show", "-p", "FragmentPath", serviceUnitName)
	if err != nil {
		utils.LogError("Failed to find service unit path: %v", err)
	} else {
		utils.LogInfo("Service unit file: %s", string(unitPathOutput))

		// Try to get unit file content
		parts := strings.Split(string(unitPathOutput), "=")
		if len(parts) > 1 {
			unitPath := strings.TrimSpace(parts[1])
			if unitFileContent, err := os.ReadFile(unitPath); err == nil {
				utils.LogInfo("Service unit file content:\n%s", string(unitFileContent))
			}
		}
	}

	// Check systemd service status
	statusOutput, err := runCommandWithOutput("systemctl", "status", serviceUnitName)
	if err != nil {
		utils.LogWarn("Service %s is not running: %v", serviceUnitName, err)
		utils.LogInfo("Service status output: %s", statusOutput)

		// Give suggestion on how to start
		utils.LogInfo("Suggestion: Try starting the service manually with:")
		utils.LogInfo("    sudo systemctl start %s", serviceUnitName)
		utils.LogInfo("    sudo systemctl enable %s", serviceUnitName)
	} else {
		utils.LogSuccess("Service %s is active", serviceUnitName)
	}

	// Check logs regardless of service status
	journalOutput, err := runCommandWithOutput("journalctl", "-u", serviceUnitName, "--no-pager", "-n", "20")
	if err != nil {
		utils.LogWarn("Unable to fetch service logs: %v", err)
	} else {
		utils.LogInfo("Recent logs:")
		utils.LogInfo("%s", string(journalOutput))
	}

	return nil
}

// downloadDebianPackage downloads the debian package from the container registry
func downloadDebianPackage(outputDir string) (string, error) {
	utils.LogInfo("Preparing BYOH agent Debian package from %s", ByohAgentDebPackageURL)

	// Check if imgpkg is available (this is now handled by ensureRequiredPackages)
	imgpkgPath, err := exec.LookPath("imgpkg")
	if err != nil {
		return "", fmt.Errorf("imgpkg not found in PATH: %v", err)
	}

	// Use a buffer to capture the command output
	var outputBuffer bytes.Buffer
	pullCmd := exec.Command(imgpkgPath, "pull", "-i", ByohAgentDebPackageURL, "-o", outputDir)
	pullCmd.Stdout = &outputBuffer
	pullCmd.Stderr = &outputBuffer

	if err := pullCmd.Run(); err != nil {
		output := outputBuffer.String()

		if strings.Contains(output, "UNAUTHORIZED") {
			utils.LogWarn("Authentication error. If this is a private repository, you may need to configure Docker credentials.")
			utils.LogInfo("Try running: docker login quay.io")
			return "", fmt.Errorf("authentication error: %v", err)
		}

		return "", fmt.Errorf("failed to pull package: %v\nOutput: %s", err, output)
	}

	// Check if we've downloaded the Debian package file
	debFilePath := filepath.Join(outputDir, ByohAgentDebPackageFilename)
	if _, err := os.Stat(debFilePath); err != nil {
		// Try to find the downloaded file if it's not in the expected location
		files, err := filepath.Glob(filepath.Join(outputDir, "*.deb"))
		if err != nil || len(files) == 0 {
			return "", fmt.Errorf("could not find downloaded Debian package in %s", outputDir)
		}
		// Use the first .deb file found
		debFilePath = files[0]
	}

	// Log success message only once
	utils.LogSuccess("Downloaded package to %s", debFilePath)
	return debFilePath, nil
}

// installDebianPackage installs the downloaded debian package
func installDebianPackage(debFilePath string) error {
	// Check if dpkg is available (this is now handled by ensureRequiredPackages)
	dpkgPath, err := exec.LookPath("dpkg")
	if err != nil {
		return fmt.Errorf("dpkg not found in PATH: %v", err)
	}

	// Install the package
	utils.LogInfo("Installing package %s", debFilePath)

	// First, try a clean installation
	cmd := exec.Command(dpkgPath, "-i", debFilePath)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// If there was a dependency error, try to fix and install again
		if strings.Contains(outputStr, "dependency problems") {
			utils.LogWarn("Dependency issues detected. Attempting to fix...")

			// Fix dependencies and retry installation
			if err := fixBrokenDependencies(); err != nil {
				return fmt.Errorf("failed to fix dependencies: %v", err)
			}

			// Try installing again
			retryCmd := exec.Command(dpkgPath, "-i", debFilePath)
			retryOutput, retryErr := retryCmd.CombinedOutput()

			if retryErr != nil {
				return fmt.Errorf("failed to install package after dependency fix: %v\nOutput: %s",
					retryErr, string(retryOutput))
			}
		} else {
			return fmt.Errorf("failed to install package: %v\nOutput: %s", err, outputStr)
		}
	}

	utils.LogSuccess("Successfully installed Debian package %s", debFilePath)
	return nil
}
