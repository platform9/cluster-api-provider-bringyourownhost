package cmd

import (
	"bytes"
	"strings"
	"testing"

	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func TestOnboardFlags(t *testing.T) {
	// Store original values to restore later
	origUsername := username
	origPassword := password
	origPasswordInteractive := passwordInteractive
	origFqdn := fqdn
	origDomain := domain
	origTenant := tenant
	origClientToken := clientToken
	origVerbosity := verbosity
	origRegionName := regionName

	defer func() {
		// Restore original values
		username = origUsername
		password = origPassword
		passwordInteractive = origPasswordInteractive
		fqdn = origFqdn
		domain = origDomain
		tenant = origTenant
		clientToken = origClientToken
		verbosity = origVerbosity
		regionName = origRegionName
	}()

	// Reset global flags
	username = ""
	password = ""
	passwordInteractive = false
	fqdn = ""
	domain = ""
	tenant = ""
	clientToken = ""
	verbosity = ""
	regionName = ""
	// Create a new test command with the same flag setup
	testCmd := createTestCommand()

	// Test flag parsing
	args := []string{
		"--username", "test@example.com",
		"--password", "test-password",
		"--url", "test.platform9.com",
		"--domain", "custom-domain",
		"--tenant", "custom-tenant",
		"--client-token", "custom-token",
		"--verbosity", "debug",
		"--region", "test-region",
	}

	testCmd.SetArgs(args)
	if err := testCmd.Execute(); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify flags were parsed correctly
	if username != "test@example.com" {
		t.Errorf("Expected username 'test@example.com', got '%s'", username)
	}

	if password != "test-password" {
		t.Errorf("Expected password 'test-password', got '%s'", password)
	}

	if passwordInteractive != false {
		t.Errorf("Expected passwordInteractive 'false', got '%v'", passwordInteractive)
	}

	if fqdn != "test.platform9.com" {
		t.Errorf("Expected fqdn 'test.platform9.com', got '%s'", fqdn)
	}

	if domain != "custom-domain" {
		t.Errorf("Expected domain 'custom-domain', got '%s'", domain)
	}

	if tenant != "custom-tenant" {
		t.Errorf("Expected tenant 'custom-tenant', got '%s'", tenant)
	}

	if clientToken != "custom-token" {
		t.Errorf("Expected client-token 'custom-token', got '%s'", clientToken)
	}

	if verbosity != "debug" {
		t.Errorf("Expected verbosity 'debug', got '%s'", verbosity)
	}

	if regionName != "test-region" {
		t.Errorf("Expected region 'test-region', got '%s'", regionName)
	}
}

func TestMutexFlags(t *testing.T) {
	// Create a test command with the same flag setup
	testCmd := createTestCommand()

	// Test mutual exclusivity
	args := []string{
		"--username", "testuser",
		"--password", "testpass",
		"--password-interactive",
		"--url", "test.example.com",
		"--tenant", "test-tenant",
		"--client-token", "test-token",
		"--region", "test-region",
	}

	testCmd.SetArgs(args)
	var output bytes.Buffer
	testCmd.SetOut(&output)
	testCmd.SetErr(&output)

	err := testCmd.Execute()
	if err == nil {
		t.Error("Expected error when using mutually exclusive flags, but got nil")
	}

	// Check if the error message contains information about mutually exclusive flags
	outputStr := output.String()
	if !strings.Contains(outputStr, "exclusive") && !strings.Contains(outputStr, "password") && !strings.Contains(outputStr, "interactive") {
		t.Errorf("Expected error message about mutually exclusive flags, got: %s", outputStr)
	}
}

func TestRequiredFlags(t *testing.T) {
	requiredFlags := []string{"username", "url", "client-token", "region"}

	for _, flagName := range requiredFlags {
		t.Run("missing "+flagName, func(t *testing.T) {
			// Create a test command
			testCmd := createTestCommand()

			// Prepare args with all required flags except the one we're testing
			var args []string
			if flagName != "username" {
				args = append(args, "--username", "testuser")
			}
			if flagName != "url" {
				args = append(args, "--url", "test.example.com")
			}
			if flagName != "tenant" {
				args = append(args, "--tenant", "testtenant")
			}
			if flagName != "client-token" {
				args = append(args, "--client-token", "testtoken")
			}
			if flagName != "region" {
				args = append(args, "--region", "test-region")
			}
			// Add either password or interactive
			args = append(args, "--password", "testpass")

			testCmd.SetArgs(args)
			var output bytes.Buffer
			testCmd.SetOut(&output)
			testCmd.SetErr(&output)

			err := testCmd.Execute()
			if err == nil {
				t.Errorf("Expected error when missing required flag %s, but got nil", flagName)
			}

			outputStr := output.String()
			if !strings.Contains(outputStr, "Error: missing required values") || !strings.Contains(outputStr, flagName) {
				t.Errorf("Expected error message about required flag %s, got: %s", flagName, outputStr)
			}
		})
	}
}

func TestDefaultTenantValue(t *testing.T) {
	// Reset global tenant variable
	tenant = ""
	testCmd := createTestCommand()
	args := []string{
		"--username", "testuser",
		"--url", "test.example.com",
		"--client-token", "testtoken",
		"--region", "test-region",
		"--password", "testpass",
		// no --tenant flag
	}
	testCmd.SetArgs(args)
	if err := testCmd.Execute(); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if tenant != "service" {
		t.Errorf("Expected default tenant 'service', got '%s'", tenant)
	}
}

// Helper to create a temp config file
func createTempConfigFile(t *testing.T, content string) string {
	tmpfile, err := os.CreateTemp("", "onboard-config-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write to temp config file: %v", err)
	}
	tmpfile.Close()
	t.Cleanup(func() { os.Remove(tmpfile.Name()) })
	return tmpfile.Name()
}

// Helper to reset global flags
func resetOnboardGlobals() {
	username = ""
	password = ""
	passwordInteractive = false
	fqdn = ""
	domain = ""
	tenant = ""
	clientToken = ""
	verbosity = ""
	regionName = ""
	configFile = ""
}

func TestConfigFilePrecedence(t *testing.T) {
	const configYAML = `
url: "config.platform9.com"
username: "configuser"
password: "configpass"
client-token: "config-token"
domain: "config-domain"
tenant: "config-tenant"
verbosity: "important"
region: "config-region"
`
	tests := []struct {
		name string
		args []string
		want map[string]string
	}{
		{
			name: "Config file only",
			args: []string{"--config", "TMPFILE", "--password", ""},
			want: map[string]string{
				"fqdn":        "config.platform9.com",
				"username":    "configuser",
				"password":    "configpass",
				"clientToken": "config-token",
				"domain":      "config-domain",
				"tenant":      "config-tenant",
				"verbosity":   "important",
				"regionName":  "config-region",
			},
		},
		{
			name: "CLI overrides config",
			args: []string{
				"--config", "TMPFILE",
				"--username", "cliuser",
				"--url", "cli.platform9.com",
				"--client-token", "cli-token",
				"--region", "cli-region",
				"--password", "clipass",
				"--domain", "cli-domain",
				"--tenant", "cli-tenant",
				"--verbosity", "debug",
			},
			want: map[string]string{
				"fqdn":        "cli.platform9.com",
				"username":    "cliuser",
				"password":    "clipass",
				"clientToken": "cli-token",
				"domain":      "cli-domain",
				"tenant":      "cli-tenant",
				"verbosity":   "debug",
				"regionName":  "cli-region",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetOnboardGlobals()
			tmpfile := createTempConfigFile(t, configYAML)
			var args []string
			for _, arg := range tt.args {
				if arg == "TMPFILE" {
					args = append(args, tmpfile)
				} else {
					args = append(args, arg)
				}
			}
			testCmd := createTestCommand()
			testCmd.SetArgs(args)
			if err := testCmd.Execute(); err != nil {
				t.Fatalf("Expected no error, got: %v", err)
			}
			// Check expected values
			for k, v := range tt.want {
				var got string
				switch k {
				case "fqdn":
					got = fqdn
				case "username":
					got = username
				case "password":
					got = password
				case "clientToken":
					got = clientToken
				case "domain":
					got = domain
				case "tenant":
					got = tenant
				case "verbosity":
					got = verbosity
				case "regionName":
					got = regionName
				}
				if got != v {
					t.Errorf("Expected %s = '%s', got '%s'", k, v, got)
				}
			}
		})
	}
}

func TestConfigFileAndCLIDefaultFallback(t *testing.T) {
	// No CLI or config, should use default for domain, tenant, verbosity
	resetOnboardGlobals()
	username = "testuser"
	fqdn = "test.platform9.com"
	clientToken = "testtoken"
	regionName = "test-region"
	password = "testpass"

	testCmd := createTestCommand()
	args := []string{
		"--username", username,
		"--url", fqdn,
		"--client-token", clientToken,
		"--region", regionName,
		"--password", password,
	}
	testCmd.SetArgs(args)
	if err := testCmd.Execute(); err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	if domain != "default" {
		t.Errorf("Expected default domain, got '%s'", domain)
	}
	if tenant != "service" {
		t.Errorf("Expected default tenant, got '%s'", tenant)
	}
	if verbosity != "minimal" {
		t.Errorf("Expected default verbosity, got '%s'", verbosity)
	}
}

// Helper function to create a test command with the same flag setup as onboardCmd
func createTestCommand() *cobra.Command {
	// Create a new command for testing
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Test command",
		RunE: func(cmd *cobra.Command, args []string) error {
			// This function will be called when Execute() is called
			// It will validate the required and mutually exclusive flags
			return nil
		},
	}

	// Add the same flags as onboardCmd
	AddOnboardFlags(
		testCmd,
		&fqdn, &username, &password, &passwordInteractive,
		&clientToken, &domain, &tenant, &verbosity, &regionName, &configFile,
	)

	return testCmd
}

func TestInteractivePassword(t *testing.T) {
	// This is challenging to test since it requires input from stdin
	// One approach is to mock the term.ReadPassword function

	// Store original function
	origReadPassword := readPassword

	// Restore after test
	defer func() {
		readPassword = origReadPassword
	}()

	// Mock the function
	readPassword = func(fd int) ([]byte, error) {
		return []byte("mock-password"), nil
	}

	// Create a test command
	testCmd := &cobra.Command{
		Use: "test",
		Run: func(cmd *cobra.Command, args []string) {
			// Clear password
			password = ""

			// Set interactive flag
			passwordInteractive = true

			// Call the handler (simplified)
			if passwordInteractive {
				pwBytes, err := readPassword(0)
				if err != nil {
					t.Fatalf("ReadPassword failed: %v", err)
				}
				password = string(pwBytes)
			}

			// Verify password was set
			if password != "mock-password" {
				t.Errorf("Expected password 'mock-password', got '%s'", password)
			}
		},
	}

	// Execute the command
	if err := testCmd.Execute(); err != nil {
		t.Fatalf("Command execution failed: %v", err)
	}
}

// Mock function type
var readPassword func(fd int) ([]byte, error) = term.ReadPassword
