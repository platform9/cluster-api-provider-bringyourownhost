// Package service contains BYOH agent setup functions
package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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
	// ByohAgentLogPath is the path to the BYOH agent log file
	ByohAgentLogPath = "/var/log/pf9/byoh/byoh-agent.log"
	// ByohConfigDir is the directory for BYOH configuration
	ByohConfigDir = ".byoh"
)

// SetupAgent installs the BYOH agent in the host
func SetupAgent(byohDirPath string) error {
	utils.LogInfo("Setting up BYOH agent")

	// Install all pre-requisite packages first
	utils.LogInfo("Checking and installing required packages...")
	if err := ensureRequiredPackages(); err != nil {
		// Since all packages are important, return an error here
		return fmt.Errorf("failed to install required packages: %v", err)
	}

	// Proceed with downloading the agent package
	utils.LogInfo("Downloading agent package...")
	packagePath, err := downloadDebianPackage(byohDirPath)
	if err != nil {
		return fmt.Errorf("failed to download Debian package: %v", err)
	}

	// Install the agent package
	utils.LogInfo("Installing BYOH agent package...")
	if err = installDebianPackage(packagePath); err != nil {
		return fmt.Errorf("failed to install Debian package: %v", err)
	}

	utils.LogSuccess("Agent setup completed successfully")
	return nil
}

// PrepareAgentDirectory prepares the BYOH agent directory
func PrepareAgentDirectory(byohDir string) error {
	// Create byohDir if it doesn't exist
	if err := os.MkdirAll(byohDir, DefaultDirPerms); err != nil {
		return fmt.Errorf("failed to create BYOH directory %s: %v", byohDir, err)
	}
	return nil
}

var ensureRequiredPackages = func() error {
	utils.LogInfo("Checking for required packages...")

	// Fix any broken package state first
	exec.Command("dpkg", "--configure", "-a").Run()
	exec.Command("apt-get", "--fix-broken", "install", "-y").Run()

	// Install imgpkg if needed
	if _, err := exec.LookPath("imgpkg"); err != nil {
		utils.LogInfo("Installing imgpkg...")
		cmd := exec.Command("bash", "-c", "curl -s -L https://carvel.dev/install.sh | bash")
		if _, err := cmd.CombinedOutput(); err != nil {
			utils.LogWarn("Failed to install imgpkg: %v", err)
		} else {
			utils.LogSuccess("Installed imgpkg successfully")
		}
	}

	// Install all required packages in one command
	utils.LogInfo("Installing required packages...")
	cmd := exec.Command("bash", "-c",
		"apt-get update && apt-get install -y --no-install-recommends dpkg ebtables conntrack socat libseccomp2")

	_, err := cmd.CombinedOutput()
	if err != nil {
		utils.LogWarn("Initial package installation failed: %v", err)
		utils.LogInfo("Trying to fix and reinstall...")

		// Try to fix broken dependencies
		exec.Command("apt-get", "--fix-broken", "install", "-y").Run()

		// Try again with reinstall
		retryCmd := exec.Command("bash", "-c",
			"apt-get install -y --reinstall --no-install-recommends dpkg ebtables conntrack socat libseccomp2")
		retryOutput, retryErr := retryCmd.CombinedOutput()

		if retryErr != nil {
			return fmt.Errorf("failed to install packages: %v\nOutput: %s", retryErr, string(retryOutput))
		}
	}

	utils.LogSuccess("All required packages installed successfully")
	return nil
}

var downloadDebianPackage = func(tempDir string) (string, error) {
	utils.LogInfo("Downloading BYOH agent Debian package from %s", ByohAgentDebPackageURL)

	// Check if imgpkg is available (this is now handled by ensureRequiredPackages)
	imgpkgPath, err := exec.LookPath("imgpkg")
	if err != nil {
		return "", fmt.Errorf("imgpkg not found in PATH: %v", err)
	}

	// Use a buffer to capture the command output
	var outputBuffer bytes.Buffer
	pullCmd := exec.Command(imgpkgPath, "pull", "-i", ByohAgentDebPackageURL, "-o", tempDir)
	pullCmd.Stdout = &outputBuffer
	pullCmd.Stderr = &outputBuffer

	if err := pullCmd.Run(); err != nil {
		output := outputBuffer.String()
		return "", fmt.Errorf("failed to pull package: %v\nOutput: %s", err, output)
	}

	// Check if we've downloaded the Debian package file
	debFilePath := filepath.Join(tempDir, ByohAgentDebPackageFilename)
	if _, err := os.Stat(debFilePath); err != nil {
		return "", fmt.Errorf("could not find downloaded Debian package in %s", tempDir)
	}

	utils.LogSuccess("Downloaded package to %s", debFilePath)
	return debFilePath, nil
}

var installDebianPackage = func(debFilePath string) error {

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
		// // If there was a dependency error, try to fix and install again
		// if strings.Contains(outputStr, "dependency problems") {
		// 	utils.LogWarn("Dependency issues detected. Attempting to fix...")

		// 	// Fix dependencies and retry installation
		// 	if err := fixBrokenDependencies(); err != nil {
		// 		return fmt.Errorf("failed to fix dependencies: %v", err)
		// 	}

		// 	// Try installing again
		// 	retryCmd := exec.Command(dpkgPath, "-i", debFilePath)
		// 	retryOutput, retryErr := retryCmd.CombinedOutput()

		// 	if retryErr != nil {
		// 		return fmt.Errorf("failed to install package after dependency fix: %v\nOutput: %s",
		// 			retryErr, string(retryOutput))
		// 	}
		// } else {
		return fmt.Errorf("failed to install package: %v\nOutput: %s", err, outputStr)
	}

	utils.LogSuccess("Successfully installed Debian package %s", debFilePath)
	return nil
}
