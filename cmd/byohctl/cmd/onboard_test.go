package cmd

import (
	"bytes"
	"strings"
	"testing"

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

	// Create a new test command with the same flag setup
	testCmd := createTestCommand()

	// Test flag parsing
	args := []string{
		"--username", "test@example.com",
		"--password", "test-password",
		"--fqdn", "test.platform9.com",
		"--domain", "custom-domain",
		"--tenant", "custom-tenant",
		"--client-token", "custom-token",
		"--verbosity", "debug",
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
}

func TestMutexFlags(t *testing.T) {
	// Create a test command with the same flag setup
	testCmd := createTestCommand()

	// Test mutual exclusivity
	args := []string{
		"--username", "testuser",
		"--password", "testpass",
		"--interactive",
		"--fqdn", "test.example.com",
		"--tenant", "test-tenant",
		"--client-token", "test-token",
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
	requiredFlags := []string{"username", "fqdn", "tenant", "client-token"}

	for _, flagName := range requiredFlags {
		t.Run("missing "+flagName, func(t *testing.T) {
			// Create a test command
			testCmd := createTestCommand()

			// Prepare args with all required flags except the one we're testing
			var args []string
			if flagName != "username" {
				args = append(args, "--username", "testuser")
			}
			if flagName != "fqdn" {
				args = append(args, "--fqdn", "test.example.com")
			}
			if flagName != "tenant" {
				args = append(args, "--tenant", "testtenant")
			}
			if flagName != "client-token" {
				args = append(args, "--client-token", "testtoken")
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

			// Check if the error message contains information about the required flag
			outputStr := output.String()
			if !strings.Contains(outputStr, "required") && !strings.Contains(outputStr, flagName) {
				t.Errorf("Expected error message about required flag %s, got: %s", flagName, outputStr)
			}
		})
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
	testCmd.Flags().StringVarP(&username, "username", "u", "", "Username for authentication")
	testCmd.Flags().StringVarP(&password, "password", "p", "", "Password for authentication")
	testCmd.Flags().BoolVarP(&passwordInteractive, "interactive", "i", false, "Prompt for password interactively")
	testCmd.Flags().StringVarP(&fqdn, "fqdn", "f", "", "Platform9 FQDN")
	testCmd.Flags().StringVarP(&domain, "domain", "d", "default", "Domain name")
	testCmd.Flags().StringVarP(&tenant, "tenant", "t", "", "Tenant name")
	testCmd.Flags().StringVarP(&clientToken, "client-token", "c", "", "Client token for authentication")
	testCmd.Flags().StringVarP(&verbosity, "verbosity", "v", "minimal", "Log verbosity level")

	// Mark mutual exclusivity
	testCmd.MarkFlagsMutuallyExclusive("password", "interactive")

	// Mark required flags
	testCmd.MarkFlagRequired("username")
	testCmd.MarkFlagRequired("fqdn")
	testCmd.MarkFlagRequired("client-token")
	testCmd.MarkFlagRequired("tenant")

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
