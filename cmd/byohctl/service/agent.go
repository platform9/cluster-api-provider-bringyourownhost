// Package service contains BYOH agent setup functions
package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
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

	// ImgPkgVersion is the version of imgpkg to install
	ImgPkgVersion = "v0.45.0"
	// ImgPkgURL is the URL to download imgpkg
	ImgPkgURL = "https://github.com/carvel-dev/imgpkg/releases/download/" + ImgPkgVersion + "/imgpkg-linux-amd64"
	// ImgPkgPath is the path where imgpkg will be installed
	ImgPkgPath = "/usr/local/bin/imgpkg"
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
	output, err := exec.Command("apt-get", "--fix-broken", "install", "-y").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to fix broken packages: %v\nOutput: %s", err, string(output))
	}

	// Install imgpkg if needed
	if _, err := exec.LookPath("imgpkg"); err != nil {
		utils.LogInfo("Installing imgpkg...")

		resp, err := http.Get(ImgPkgURL)
		if err != nil {
			return fmt.Errorf("failed to download imgpkg: %v", err)
		}
		defer resp.Body.Close()

		// Create the file
		out, err := os.Create(ImgPkgPath)
		if err != nil {
			return fmt.Errorf("failed to create file: %v", err)
		}
		defer out.Close()

		// Copy data to file
		_, err = io.Copy(out, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to write file: %v", err)
		}

		// Make executable
		if err := os.Chmod(ImgPkgPath, 0755); err != nil {
			return fmt.Errorf("failed to make file executable: %v", err)
		}

		utils.LogSuccess("Installed imgpkg " + ImgPkgVersion)
	}

	// Install all required packages in one command
	utils.LogInfo("Installing required packages...")
	cmd := exec.Command("bash", "-c",
		"apt-get update && apt-get install -y --no-install-recommends dpkg ebtables conntrack socat libseccomp2")

	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install packages: %v\nOutput: %s", err, string(output))
	}

	utils.LogSuccess("All required packages installed successfully")
	return nil
}

var downloadDebianPackage = func(tempDir string) (string, error) {
	utils.LogInfo("Downloading BYOH agent Debian package from %s", ByohAgentDebPackageURL)

	imgpkgPath, _ := exec.LookPath("imgpkg")

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
	dpkgPath, _ := exec.LookPath("dpkg")

	// Install the package
	utils.LogInfo("Installing package %s", debFilePath)

	// First, try a clean installation
	cmd := exec.Command(dpkgPath, "-i", debFilePath)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		return fmt.Errorf("failed to install package: %v\nOutput: %s", err, outputStr)
	}

	utils.LogSuccess("Successfully installed Debian package %s", debFilePath)
	return nil
}
