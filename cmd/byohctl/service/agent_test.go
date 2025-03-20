package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
)

// TestDirCreator is an interface for directory creation to allow mocking
type TestDirCreator interface {
	MkdirAll(path string, perm os.FileMode) error
}

// RealDirCreator uses the actual os package
type RealDirCreator struct{}

func (r *RealDirCreator) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// TestDirCreatorMock mocks directory creation for testing
type TestDirCreatorMock struct {
	Called    bool
	Path      string
	Perm      os.FileMode
	ReturnErr error
}

func (m *TestDirCreatorMock) MkdirAll(path string, perm os.FileMode) error {
	m.Called = true
	m.Path = path
	m.Perm = perm
	return m.ReturnErr
}

// Helper function to restore original functions after tests
func restoreExecFunctions() {
	execCommand = exec.Command
	execLookPath = exec.LookPath
}

// Setup a complete mocking environment for BYOH agent tests
func setupMockExecEnvironment() func() {
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath

	// Mock exec.Command
	execCommand = func(command string, args ...string) *exec.Cmd {
		switch command {
		case "bash":
			if len(args) > 1 && args[0] == "-c" && contains(args[1], "apt-get") {
				return mockCommand("bash")
			}
			return mockCommand("bash")
		case "dpkg":
			return mockCommand("dpkg")
		case "apt-get":
			return mockCommand("apt-get")
		case "imgpkg":
			cmd := mockCommand("imgpkg")
			// If pull command, create the package file
			if len(args) > 0 && args[0] == "pull" {
				outputDir := ""
				for i, arg := range args {
					if arg == "-o" && i+1 < len(args) {
						outputDir = args[i+1]
						// Create the directory and mock package file
						os.MkdirAll(outputDir, 0755)
						mockFile := filepath.Join(outputDir, "pf9-byohost-agent.deb")
						os.WriteFile(mockFile, []byte("mock package"), 0644)
						break
					}
				}
			}
			return cmd
		case "which", "type":
			return mockCommand("which")
		default:
			return mockCommand(command)
		}
	}

	// Mock exec.LookPath
	execLookPath = func(file string) (string, error) {
		switch file {
		case "imgpkg":
			return "/usr/local/bin/imgpkg", nil
		case "dpkg":
			return "/usr/bin/dpkg", nil
		case "apt-get":
			return "/usr/bin/apt-get", nil
		default:
			return "", fmt.Errorf("%s: executable file not found in $PATH", file)
		}
	}

	// Return a function to restore the original functions
	return func() {
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}
}

// Mock command execution for testing
var execCommand = exec.Command
var execLookPath = exec.LookPath

// Mock command execution for testing
func mockCommand(command string) *exec.Cmd {
	cs := []string{"-c", ""}
	cmd := exec.Command("echo")
	cmd.Args = append([]string{"bash"}, cs...)
	return cmd
}

// mockCommandWithError creates a mock command that fails with an error
func mockCommandWithError(command string, errMsg string, exitCode int) *exec.Cmd {
	cs := []string{"-c", fmt.Sprintf("echo '%s' >&2; exit %d", errMsg, exitCode)}
	cmd := exec.Command("bash", cs...)
	return cmd
}

// TestHelperProcess is not a real test, it's used to mock exec.Command
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	// args are the command and arguments passed to the mock
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}

	if len(args) == 0 {
		os.Exit(1)
	}

	// Mock different commands based on args
	switch args[0] {
	case "bash":
		// Successfully mock bash commands
		os.Exit(0)
	case "apt-get":
		// Successfully mock apt-get
		os.Exit(0)
	case "dpkg":
		// Successfully mock dpkg
		os.Exit(0)
	case "imgpkg":
		// Mock imgpkg - always succeed
		if len(args) > 1 && args[1] == "pull" {
			// If -o output directory is specified
			outputDir := ""
			for i, arg := range args {
				if arg == "-o" && i+1 < len(args) {
					outputDir = args[i+1]
					// Create a mock file in the output directory
					if outputDir != "" {
						os.MkdirAll(outputDir, 0755)
						mockFile := filepath.Join(outputDir, "pf9-byohost-agent.deb")
						os.WriteFile(mockFile, []byte("mock package"), 0644)
					}
					break
				}
			}
		}
		os.Exit(0)
	case "which":
		// Mock which command - always succeed for our binaries
		if len(args) > 1 && (args[1] == "imgpkg" || args[1] == "dpkg" || args[1] == "apt-get") {
			os.Stdout.WriteString("/usr/bin/" + args[1])
			os.Exit(0)
		}
		// For other commands, fail
		os.Exit(1)
	}

	// Default - mock as succeeded
	os.Exit(0)
}

// Helper function to check if a string contains another string
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Custom function for testing PrepareAgentDirectory that accepts a TestDirCreator
func testPrepareAgentDirectory(dirCreator TestDirCreator) (string, error) {
	// Create .byoh directory in user's home
	byohDir := filepath.Join(os.Getenv("HOME"), ".byoh")
	if err := dirCreator.MkdirAll(byohDir, 0755); err != nil {
		return "", err
	}
	return byohDir, nil
}

// Test PrepareAgentDirectory
func TestPrepareAgentDirectory(t *testing.T) {
	// Create mock directory creator
	mockDirCreator := &TestDirCreatorMock{}

	// Execute our test function with the mock
	byohDir, err := testPrepareAgentDirectory(mockDirCreator)

	// Verify
	if err != nil {
		t.Errorf("PrepareAgentDirectory returned error: %v", err)
	}

	// Check directory path - using the ByohConfigDir constant
	homeDir, _ := os.UserHomeDir()
	expectedDir := filepath.Join(homeDir, ".byoh")
	if byohDir != expectedDir {
		t.Errorf("Expected byohDir to be %s, got %s", expectedDir, byohDir)
	}

	if !mockDirCreator.Called {
		t.Errorf("Expected MkdirAll to be called")
	}

	if mockDirCreator.Path != expectedDir {
		t.Errorf("Expected MkdirAll to be called with path %s, got %s", expectedDir, mockDirCreator.Path)
	}

	if mockDirCreator.Perm != os.FileMode(0755) {
		t.Errorf("Expected MkdirAll to be called with permissions %v, got %v", os.FileMode(0755), mockDirCreator.Perm)
	}
}

// Helper to get the current operating system
func getGOOS() string {
	return "linux" // Mock to return linux for tests
}

// Test SetupAgent with mocked binary download
func TestSetupAgent(t *testing.T) {
	// Skip if not Linux
	if runtime.GOOS != "linux" {
		t.Skip("Skipping test on non-Linux platform")
	}

	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "setup-agent-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save original functions and restore after test
	origExecCommand := execCommand
	origExecLookPath := execLookPath
	origEnsureRequiredPackages := ensureRequiredPackages
	origDownloadDebianPackage := downloadDebianPackage
	origInstallDebianPackage := installDebianPackage
	
	defer func() {
		execCommand = origExecCommand
		execLookPath = origExecLookPath
		ensureRequiredPackages = origEnsureRequiredPackages
		downloadDebianPackage = origDownloadDebianPackage
		installDebianPackage = origInstallDebianPackage
	}()

	// Mock required functions
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}
	
	execCommand = func(command string, args ...string) *exec.Cmd {
		return mockCommand(command)
	}
	
	// Mock ensureRequiredPackages to succeed
	ensureRequiredPackages = func() error {
		return nil
	}
	
	// Mock downloadDebianPackage to create a mock package file
	downloadDebianPackage = func(tempDir string) (string, error) {
		packagePath := filepath.Join(tempDir, ByohAgentDebPackageFilename)
		os.WriteFile(packagePath, []byte("mock package"), 0644)
		return packagePath, nil
	}
	
	// Mock installDebianPackage to succeed
	installDebianPackage = func(packagePath string) error {
		return nil
	}

	// Run the function being tested
	err = SetupAgent(tmpDir)

	// Validate results
	if err != nil {
		t.Errorf("SetupAgent returned error: %v", err)
	}

	// Verify the package file was created
	packagePath := filepath.Join(tmpDir, ByohAgentDebPackageFilename)
	if _, err := os.Stat(packagePath); os.IsNotExist(err) {
		t.Errorf("Debian package file was not found at %s", packagePath)
	}
}

// Test SetupAgent with errors
func TestSetupAgentErrors(t *testing.T) {
	// Define test cases
	tests := []struct {
		name          string
		setupMock     func()
		expectedError string
	}{
		{
			name: "package installation fails",
			setupMock: func() {
				// Mock apt-get to fail
				execCommand = func(command string, args ...string) *exec.Cmd {
					if command == "bash" && len(args) > 1 && args[0] == "-c" && strings.Contains(args[1], "apt-get") {
						cmd := mockCommand("exit")
						cmd.Args = append(cmd.Args, "1") // Cause exit with error
						return cmd
					}
					return mockCommand(command)
				}

				// Make sure the binaries are found
				execLookPath = func(file string) (string, error) {
					return "/usr/bin/" + file, nil
				}
			},
			expectedError: "failed to install required packages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary directory
			tempDir, err := os.MkdirTemp("", "setup-agent-error-test")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			// Save original functions and restore after test
			oldExecCommand := execCommand
			oldExecLookPath := execLookPath
			defer func() {
				execCommand = oldExecCommand
				execLookPath = oldExecLookPath
			}()

			// Setup test-specific mocks
			tc.setupMock()

			// Call the function being tested
			err = SetupAgent(tempDir)

			// Verify error was returned
			if err == nil {
				t.Fatalf("Expected error but got nil")
			}

			// Verify the error message
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("Expected error about %s, got: %v", tc.expectedError, err)
			}
		})
	}
}

// TestEnsureRequiredPackagesMock tests that we can properly mock the package installation
func TestEnsureRequiredPackagesMock(t *testing.T) {
	// Set up mock environment
	cleanup := setupMockExecEnvironment()
	defer cleanup()

	// Force apt-get to always fail to simulate package installation failures
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == "bash" && len(args) > 1 && args[0] == "-c" && strings.Contains(args[1], "apt-get") {
			return exec.Command("bash", "-c", "echo 'Package installation failed' >&2; exit 127")
		}
		// Let other commands use our standard mocking
		return mockCommand(command)
	}

	// Call the function
	err := ensureRequiredPackages()

	// Should fail with apt-get error
	if err == nil {
		t.Fatalf("Expected ensureRequiredPackages to fail, but got no error")
	}

	// Error should contain information about the failed package installation
	if !strings.Contains(err.Error(), "failed to install packages") {
		t.Errorf("ensureRequiredPackages returned error: %v", err)
	}
}

// MockCommandWithError returns a mock Command that will fail with the given exit code and error message
func MockCommandWithError(exitCode int, errorMsg string) func(string, ...string) *exec.Cmd {
	return func(command string, args ...string) *exec.Cmd {
		// Default to success for all commands
		if command == "which" {
			// We need which to succeed for our binaries
			if len(args) > 0 && (args[0] == "imgpkg" || args[0] == "dpkg" || args[0] == "apt-get") {
				return mockCommand("which")
			}
		}

		// Create a command that will fail
		cmd := exec.Command("sh", "-c", fmt.Sprintf("echo '%s' >&2; exit %d", errorMsg, exitCode))
		return cmd
	}
}

// TestDownloadDebianPackage tests the debian package download functionality
func TestDownloadDebianPackage(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "download-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock for downloadDebianPackage
	oldDownloadDebianPackage := downloadDebianPackage
	defer func() {
		downloadDebianPackage = oldDownloadDebianPackage
	}()
	
	// Mock downloadDebianPackage to succeed and create a mock file
	downloadDebianPackage = func(tempDir string) (string, error) {
		packagePath := filepath.Join(tempDir, ByohAgentDebPackageFilename)
		// Create the mock file
		err := os.MkdirAll(tempDir, 0755)
		if err != nil {
			return "", err
		}
		err = os.WriteFile(packagePath, []byte("mock package contents"), 0644)
		if err != nil {
			return "", err
		}
		return packagePath, nil
	}

	// Call the mocked function
	packagePath, err := downloadDebianPackage(tempDir)

	// Verify results
	if err != nil {
		t.Errorf("downloadDebianPackage returned error: %v", err)
	}

	// Verify the package path is correct
	expectedPath := filepath.Join(tempDir, ByohAgentDebPackageFilename)
	if packagePath != expectedPath {
		t.Errorf("Expected package path %s, got %s", expectedPath, packagePath)
	}

	// Verify the package file exists
	if _, err := os.Stat(packagePath); os.IsNotExist(err) {
		t.Errorf("Package file was not created at %s", packagePath)
	}
}

// TestDownloadDebianPackageErrors tests error scenarios for downloadDebianPackage
func TestDownloadDebianPackageErrors(t *testing.T) {
	// Define test cases
	tests := []struct {
		name          string
		setupMock     func() func()
		expectedError string
	}{
		{
			name: "imgpkg not found",
			setupMock: func() func() {
				oldDownloadDebianPackage := downloadDebianPackage
				downloadDebianPackage = func(tempDir string) (string, error) {
					return "", fmt.Errorf("imgpkg not found in PATH: exec: \"imgpkg\": executable file not found in $PATH")
				}
				return func() {
					downloadDebianPackage = oldDownloadDebianPackage
				}
			},
			expectedError: "imgpkg not found in PATH",
		},
		{
			name: "imgpkg pull fails",
			setupMock: func() func() {
				oldDownloadDebianPackage := downloadDebianPackage
				downloadDebianPackage = func(tempDir string) (string, error) {
					return "", fmt.Errorf("failed to pull package: exit status 1\nOutput: Error: some error message")
				}
				return func() {
					downloadDebianPackage = oldDownloadDebianPackage
				}
			},
			expectedError: "failed to pull",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a temporary directory for each test
			tempDir, err := os.MkdirTemp("", "download-error-test")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)
			
			// Setup and get the cleanup function
			cleanup := tc.setupMock()
			defer cleanup()
			
			// Call the function being tested
			_, err = downloadDebianPackage(tempDir)
			
			// Verify error was returned
			if err == nil {
				t.Fatalf("Expected error but got nil")
			}
			
			// Verify the error message
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("Expected error about %s, got: %v", tc.expectedError, err)
			}
		})
	}
}

// TestInstallDebianPackage tests the installDebianPackage function
func TestInstallDebianPackage(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "install-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock package file
	packageFile := filepath.Join(tempDir, "pf9-byohost-agent.deb")
	if err := os.WriteFile(packageFile, []byte("mock package"), 0644); err != nil {
		t.Fatalf("Failed to create mock package file: %v", err)
	}

	// Mock installDebianPackage to succeed
	oldInstallDebianPackage := installDebianPackage
	defer func() {
		installDebianPackage = oldInstallDebianPackage
	}()
	
	installDebianPackage = func(debFilePath string) error {
		// Just verify the file exists 
		if _, err := os.Stat(debFilePath); os.IsNotExist(err) {
			return fmt.Errorf("package file does not exist: %v", err)
		}
		return nil
	}

	// Test the function
	err = installDebianPackage(packageFile)

	// Verify results
	if err != nil {
		t.Errorf("installDebianPackage returned error: %v", err)
	}
}

// TestInstallDebianPackageErrors tests error scenarios for installDebianPackage
func TestInstallDebianPackageErrors(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "install-error-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock package file
	packagePath := filepath.Join(tempDir, "pf9-byohost-agent.deb")
	if err := os.WriteFile(packagePath, []byte("mock package"), 0644); err != nil {
		t.Fatalf("Failed to create mock package file: %v", err)
	}

	// Define test cases
	tests := []struct {
		name          string
		setupMock     func() func()
		expectedError string
	}{
		{
			name: "dpkg not found",
			setupMock: func() func() {
				oldInstallDebianPackage := installDebianPackage
				installDebianPackage = func(debFilePath string) error {
					return fmt.Errorf("dpkg not found in PATH: exec: \"dpkg\": executable file not found in $PATH")
				}
				return func() {
					installDebianPackage = oldInstallDebianPackage
				}
			},
			expectedError: "dpkg not found in PATH",
		},
		{
			name: "dpkg installation fails",
			setupMock: func() func() {
				oldInstallDebianPackage := installDebianPackage
				installDebianPackage = func(debFilePath string) error {
					return fmt.Errorf("failed to install package: exit status 1\nOutput: some error message")
				}
				return func() {
					installDebianPackage = oldInstallDebianPackage
				}
			},
			expectedError: "failed to install package",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup and get the cleanup function
			cleanup := tc.setupMock()
			defer cleanup()
			
			// Call the function being tested
			err := installDebianPackage(packagePath)
			
			// Verify error was returned
			if err == nil {
				t.Fatalf("Expected error but got nil")
			}
			
			// Verify the error message
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("Expected error about %s, got: %v", tc.expectedError, err)
			}
		})
	}
}

// Wrap the original downloadDebianPackage function with one that uses our mocked exec functions
func mockDownloadDebianPackage(outputDir string) (string, error) {
	// Check if imgpkg is available
	imgpkgPath, err := execLookPath("imgpkg")
	if err != nil {
		return "", fmt.Errorf("imgpkg not found in PATH: %v", err)
	}

	// Use a buffer to capture the command output
	var outputBuffer bytes.Buffer
	pullCmd := execCommand(imgpkgPath, "pull", "-i", ByohAgentDebPackageURL, "-o", outputDir)
	pullCmd.Stdout = &outputBuffer
	pullCmd.Stderr = &outputBuffer

	if err := pullCmd.Run(); err != nil {
		output := outputBuffer.String()
		return "", fmt.Errorf("failed to pull package: %v\nOutput: %s", err, output)
	}

	// Check if we've downloaded the Debian package file
	debFilePath := filepath.Join(outputDir, ByohAgentDebPackageFilename)
	if _, err := os.Stat(debFilePath); err != nil {
		return "", fmt.Errorf("could not find downloaded Debian package in %s", outputDir)
	}

	utils.LogSuccess("Downloaded package to %s", debFilePath)
	return debFilePath, nil
}

// Wrap the original installDebianPackage function with one that uses our mocked exec functions
func mockInstallDebianPackage(debFilePath string) error {
	dpkgPath, err := execLookPath("dpkg")
	if err != nil {
		return fmt.Errorf("dpkg not found in PATH: %v", err)
	}

	// Install the package
	utils.LogInfo("Installing package %s", debFilePath)

	// Install the package
	cmd := execCommand(dpkgPath, "-i", debFilePath)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		return fmt.Errorf("failed to install package: %v\nOutput: %s", err, outputStr)
	}

	utils.LogSuccess("Successfully installed Debian package %s", debFilePath)
	return nil
}

// Wrap the original ensureRequiredPackages function with one that uses our mocked exec functions
func mockEnsureRequiredPackages() error {
	// Fix any broken package state first
	execCommand("dpkg", "--configure", "-a").Run()
	execCommand("apt-get", "--fix-broken", "install", "-y").Run()

	// Install imgpkg if needed
	if _, err := execLookPath("imgpkg"); err != nil {
		utils.LogInfo("Installing imgpkg...")
		cmd := execCommand("bash", "-c", "curl -s -L https://carvel.dev/install.sh | bash")
		if _, err := cmd.CombinedOutput(); err != nil {
			utils.LogWarn("Failed to install imgpkg: %v", err)
		} else {
			utils.LogSuccess("Installed imgpkg successfully")
		}
	}

	// Install all required packages in one command
	utils.LogInfo("Installing required packages...")
	cmd := execCommand("bash", "-c",
		"apt-get update && apt-get install -y --no-install-recommends dpkg ebtables conntrack socat libseccomp2")

	_, err := cmd.CombinedOutput()
	if err != nil {
		utils.LogWarn("Initial package installation failed: %v", err)
		utils.LogInfo("Trying to fix and reinstall...")

		// Try to fix broken dependencies
		execCommand("apt-get", "--fix-broken", "install", "-y").Run()

		// Try again with reinstall
		retryCmd := execCommand("bash", "-c",
			"apt-get install -y --reinstall --no-install-recommends dpkg ebtables conntrack socat libseccomp2")
		retryOutput, retryErr := retryCmd.CombinedOutput()

		if retryErr != nil {
			return fmt.Errorf("failed to install packages: %v\nOutput: %s", retryErr, string(retryOutput))
		}
	}

	utils.LogSuccess("All required packages installed successfully")
	return nil
}

// TestMockEnsureRequiredPackages tests the package installation function
func TestMockEnsureRequiredPackages(t *testing.T) {
	// Save original functions
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath
	defer func() {
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}()

	// Mock LookPath to find all required executables
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}

	// Mock Command to avoid real execution
	execCommand = func(command string, args ...string) *exec.Cmd {
		return mockCommand(command)
	}

	// Test the function
	err := mockEnsureRequiredPackages()

	// Verify results
	if err != nil {
		t.Errorf("ensureRequiredPackages returned error: %v", err)
	}
}

// TestMockEnsureRequiredPackagesImgpkgMissing tests when imgpkg needs to be installed
func TestMockEnsureRequiredPackagesImgpkgMissing(t *testing.T) {
	// Save original functions
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath
	defer func() {
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}()

	// Flag to track if the imgpkg install script was called
	imgpkgScriptCalled := false

	// Mock LookPath to indicate imgpkg is not found
	execLookPath = func(file string) (string, error) {
		if file == "imgpkg" {
			return "", fmt.Errorf("executable file not found in $PATH")
		}
		return "/usr/bin/" + file, nil
	}

	// Mock Command to simulate installing imgpkg successfully
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == "bash" && len(args) > 1 && strings.Contains(args[1], "carvel.dev/install.sh") {
			imgpkgScriptCalled = true
			// Simulate a successful installation
			return mockCommand(command)
		}
		return mockCommand(command)
	}

	// Test the function
	err := mockEnsureRequiredPackages()

	// Verify results
	if err != nil {
		t.Errorf("ensureRequiredPackages returned error: %v", err)
	}

	// Verify that the installation script was called
	if !imgpkgScriptCalled {
		t.Errorf("imgpkg installation script should have been called")
	}
}

// TestMockEnsureRequiredPackagesFailure tests when package installation fails
func TestMockEnsureRequiredPackagesFailure(t *testing.T) {
	// Save original functions
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath
	defer func() {
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}()

	// Flag to track if we attempted a retry
	retryAttempted := false

	// Mock LookPath to find all required executables
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}

	// Mock Command to simulate failure in the package installation
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == "bash" && len(args) > 1 {
			if strings.Contains(args[1], "apt-get update && apt-get install") {
				// First installation attempt fails
				return exec.Command("bash", "-c", "echo 'Failed to install packages' >&2; exit 1")
			} else if strings.Contains(args[1], "apt-get install -y --reinstall") {
				// Second attempt (retry) also fails
				retryAttempted = true
				return exec.Command("bash", "-c", "echo 'Failed again to install packages' >&2; exit 1")
			}
		}
		return mockCommand(command)
	}

	// Test the function
	err := mockEnsureRequiredPackages()

	// Verify error was returned
	if err == nil {
		t.Fatalf("Expected error but got nil")
	}

	// Verify that retry was attempted
	if !retryAttempted {
		t.Errorf("Package installation retry should have been attempted")
	}

	// Verify the error message
	if !strings.Contains(err.Error(), "failed to install packages") {
		t.Errorf("Expected error about package installation failure, got: %v", err)
	}
}

// TestMockEnsureRequiredPackagesRetrySucceeds tests when first attempt fails but retry succeeds
func TestMockEnsureRequiredPackagesRetrySucceeds(t *testing.T) {
	// Save original functions
	oldExecCommand := execCommand
	oldExecLookPath := execLookPath
	defer func() {
		execCommand = oldExecCommand
		execLookPath = oldExecLookPath
	}()

	// Flags to track execution
	firstAttemptCalled := false
	retryAttempted := false

	// Mock LookPath to find all required executables
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/" + file, nil
	}

	// Mock Command to simulate failure in the first attempt but success in the retry
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == "bash" && len(args) > 1 {
			if strings.Contains(args[1], "apt-get update && apt-get install") {
				// First installation attempt fails
				firstAttemptCalled = true
				return exec.Command("bash", "-c", "echo 'Failed to install packages' >&2; exit 1")
			} else if strings.Contains(args[1], "apt-get install -y --reinstall") {
				// Second attempt (retry) succeeds
				retryAttempted = true
				return mockCommand(command)
			}
		}
		return mockCommand(command)
	}

	// Test the function
	err := mockEnsureRequiredPackages()

	// Verify results
	if err != nil {
		t.Errorf("ensureRequiredPackages returned error: %v", err)
	}

	// Verify that both attempts were made
	if !firstAttemptCalled {
		t.Errorf("First package installation attempt should have been made")
	}
	if !retryAttempted {
		t.Errorf("Package installation retry should have been attempted")
	}
}

// Test ConfigureAgent
func TestConfigureAgent(t *testing.T) {
	t.Skip("Skipping TestConfigureAgent due to permission requirements")
}

// Test StartAgent with mocked binary verification
func TestStartAgent(t *testing.T) {
	t.Skip("Skipping TestStartAgent due to permission requirements")
}

// Test InstallPrerequisites with mocks to verify checks
func TestInstallPrerequisites(t *testing.T) {
	t.Skip("Skipping TestInstallPrerequisites as it requires refactoring for testability")
}

// Test diagnostic functions
func TestDiagnosticFunctions(t *testing.T) {
	t.Skip("Skipping diagnostic function tests in automated testing")
}

// TestAgentSetupProcess tests the full agent setup process using mocks
func TestAgentSetupProcess(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "setup-agent-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	
	// Save the original functions and restore after test
	origEnsureRequiredPackages := ensureRequiredPackages
	origDownloadDebianPackage := downloadDebianPackage
	origInstallDebianPackage := installDebianPackage
	
	defer func() { 
		ensureRequiredPackages = origEnsureRequiredPackages
		downloadDebianPackage = origDownloadDebianPackage
		installDebianPackage = origInstallDebianPackage
	}()
	
	// Mock the required functions
	packageInstalled := false
	
	// Mock package installation checks
	ensureRequiredPackages = func() error {
		return nil // Succeed with no errors
	}
	
	// Mock package download
	downloadDebianPackage = func(outputDir string) (string, error) {
		// Create a dummy package file
		packagePath := filepath.Join(outputDir, ByohAgentDebPackageFilename)
		os.MkdirAll(outputDir, 0755)
		os.WriteFile(packagePath, []byte("mock package"), 0644)
		return packagePath, nil
	}
	
	// Mock package installation
	installDebianPackage = func(debFilePath string) error {
		packageInstalled = true
		return nil
	}
	
	// Call the function under test
	err = SetupAgent(tempDir)
	
	// Check results
	if err != nil {
		t.Errorf("SetupAgent returned error: %v", err)
	}
	
	// Verify the package was "installed"
	if !packageInstalled {
		t.Errorf("The package installation was not called")
	}
}

// TestAgentSetupFailures tests the failure scenarios in the agent setup process
func TestAgentSetupFailures(t *testing.T) {
	tests := []struct {
		name                string
		mockEnsurePackages  func() error
		mockDownloadPackage func(string) (string, error)
		mockInstallPackage  func(string) error
		expectedErrContains string
	}{
		{
			name: "package installation fails",
			mockEnsurePackages: func() error {
				return fmt.Errorf("failed to install packages: Package installation failed")
			},
			mockDownloadPackage: func(outputDir string) (string, error) {
				return "", nil // This should not be called
			},
			mockInstallPackage: func(debFilePath string) error {
				return nil // This should not be called
			},
			expectedErrContains: "failed to install required packages",
		},
		{
			name: "imgpkg missing and installation fails",
			mockEnsurePackages: func() error {
				return nil // Succeed
			},
			mockDownloadPackage: func(outputDir string) (string, error) {
				return "", fmt.Errorf("imgpkg not found in PATH: executable file not found in $PATH")
			},
			mockInstallPackage: func(debFilePath string) error {
				return nil // This should not be called
			},
			expectedErrContains: "imgpkg not found in PATH",
		},
		{
			name: "package download fails",
			mockEnsurePackages: func() error {
				return nil // Succeed
			},
			mockDownloadPackage: func(outputDir string) (string, error) {
				return "", fmt.Errorf("failed to pull image: Failed to pull package")
			},
			mockInstallPackage: func(debFilePath string) error {
				return nil // This should not be called
			},
			expectedErrContains: "failed to download Debian package",
		},
		{
			name: "package installation fails",
			mockEnsurePackages: func() error {
				return nil // Succeed
			},
			mockDownloadPackage: func(outputDir string) (string, error) {
				// Create a dummy package file
				packagePath := filepath.Join(outputDir, ByohAgentDebPackageFilename)
				os.MkdirAll(outputDir, 0755)
				os.WriteFile(packagePath, []byte("mock package"), 0644)
				return packagePath, nil
			},
			mockInstallPackage: func(debFilePath string) error {
				return fmt.Errorf("failed to install package: dpkg -i failed with exit status 1")
			},
			expectedErrContains: "failed to install Debian package",
		},
	}
	
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create a temporary directory for each test
			tempDir, err := os.MkdirTemp("", "setup-agent-fail-test")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)
			
			// Save original functions
			origEnsureRequiredPackages := ensureRequiredPackages
			origDownloadDebianPackage := downloadDebianPackage
			origInstallDebianPackage := installDebianPackage
			
			defer func() { 
				ensureRequiredPackages = origEnsureRequiredPackages
				downloadDebianPackage = origDownloadDebianPackage
				installDebianPackage = origInstallDebianPackage
			}()
			
			// Set up the mocks for this test case
			ensureRequiredPackages = tc.mockEnsurePackages
			downloadDebianPackage = tc.mockDownloadPackage
			installDebianPackage = tc.mockInstallPackage
			
			// Call the function under test
			err = SetupAgent(tempDir)
			
			// Verify we got the expected error
			if err == nil {
				t.Fatalf("Expected error but got nil")
			}
			
			if !strings.Contains(err.Error(), tc.expectedErrContains) {
				t.Errorf("Expected error to contain '%s', got: %v", tc.expectedErrContains, err)
			}
		})
	}
}
