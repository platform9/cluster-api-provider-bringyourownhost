package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
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
	bootstrapKubeconfigPath = ""
	hostNamespace = ""
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
	// No CLI or config, should use default for domain, tenant, verbosity.
	// Args are passed as literals rather than globals because AddOnboardFlags
	// resets the global vars to their zero-value defaults when called inside
	// createTestCommand.
	resetOnboardGlobals()

	testCmd := createTestCommand()
	args := []string{
		"--username", "testuser",
		"--url", "test.platform9.com",
		"--client-token", "testtoken",
		"--region", "test-region",
		"--password", "testpass",
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
	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Test command",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Mirror runOnboard: load config first, then validate required fields.
			// Returns an error instead of os.Exit so tests can inspect the result.
			if configFile != "" {
				cfg, err := LoadOnboardConfig(configFile)
				if err == nil {
					mergeConfigWithFlags(cfg)
				}
			}
			var missing []string
			if fqdn == "" {
				missing = append(missing, "--url")
			}
			if username == "" {
				missing = append(missing, "--username")
			}
			if clientToken == "" {
				missing = append(missing, "--client-token")
			}
			if regionName == "" {
				missing = append(missing, "--region")
			}
			if len(missing) > 0 {
				return fmt.Errorf("missing required values: %s", strings.Join(missing, ", "))
			}
			return nil
		},
	}

	AddOnboardFlags(
		testCmd,
		&fqdn, &username, &password, &passwordInteractive,
		&clientToken, &domain, &tenant, &verbosity, &regionName, &configFile,
		&bootstrapKubeconfigPath, &hostNamespace,
	)

	return testCmd
}

func TestWriteBootstrapCredential(t *testing.T) {
	byohDir := t.TempDir()
	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	srcPath := filepath.Join(t.TempDir(), "bootstrap-kubeconfig.yaml")
	const kubeconfigContent = "apiVersion: v1\nkind: Config\n"
	require.NoError(t, os.WriteFile(srcPath, []byte(kubeconfigContent), 0o600))

	require.NoError(t, writeBootstrapCredential(byohDir, srcPath, "test-tenant-ns"))

	written, err := os.ReadFile(filepath.Join(bootstrapAgentConfDir, "bootstrap-kubeconfig.yaml"))
	require.NoError(t, err)
	require.Equal(t, kubeconfigContent, string(written))

	namespace, err := os.ReadFile(filepath.Join(byohDir, "namespace"))
	require.NoError(t, err)
	require.Equal(t, "test-tenant-ns", string(namespace))
}

func TestWriteBootstrapCredentialMissingSource(t *testing.T) {
	byohDir := t.TempDir()
	origConfDir := bootstrapAgentConfDir
	bootstrapAgentConfDir = t.TempDir()
	t.Cleanup(func() { bootstrapAgentConfDir = origConfDir })

	err := writeBootstrapCredential(byohDir, filepath.Join(t.TempDir(), "does-not-exist.yaml"), "test-tenant-ns")
	require.Error(t, err)
}

func TestBootstrapKubeconfigMutuallyExclusiveWithPlatform9Flags(t *testing.T) {
	for _, pcdFlag := range []struct {
		name string
		args []string
	}{
		{"username", []string{"--username", "testuser"}},
		{"client-token", []string{"--client-token", "testtoken"}},
		{"url", []string{"--url", "test.platform9.com"}},
	} {
		t.Run(pcdFlag.name, func(t *testing.T) {
			resetOnboardGlobals()
			testCmd := createTestCommand()
			args := append([]string{"--bootstrap-kubeconfig", "/tmp/bootstrap.yaml", "--namespace", "ns", "--region", "test-region"}, pcdFlag.args...)
			testCmd.SetArgs(args)
			var output bytes.Buffer
			testCmd.SetOut(&output)
			testCmd.SetErr(&output)

			err := testCmd.Execute()
			if err == nil {
				t.Errorf("Expected error when combining --bootstrap-kubeconfig with --%s, got nil", pcdFlag.name)
			}
			if !strings.Contains(output.String(), "none of the others can be") || !strings.Contains(output.String(), "bootstrap-kubeconfig") {
				t.Errorf("Expected error message about mutually exclusive flags, got: %s", output.String())
			}
		})
	}
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

	// Isolate from os.Args so test coverage flags (--test.coverprofile etc.)
	// are not parsed by cobra as unknown flags.
	testCmd.SetArgs([]string{})

	if err := testCmd.Execute(); err != nil {
		t.Fatalf("Command execution failed: %v", err)
	}
}

// Mock function type
var readPassword func(fd int) ([]byte, error) = term.ReadPassword

func ubuntuOSRelease(versionID string) string {
	return fmt.Sprintf("PRETTY_NAME=\"Ubuntu %s LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"%s\"\nID=ubuntu\nID_LIKE=debian\n", versionID, versionID)
}

func TestCheckSupportedPlatform(t *testing.T) {
	origGoos := goos
	origGoarch := goarch
	origReadFile := osReadFile
	t.Cleanup(func() {
		goos = origGoos
		goarch = origGoarch
		osReadFile = origReadFile
	})

	tests := []struct {
		name string
		goos string
		// goarch defaults to amd64 when empty, so only the arch cases have to set it.
		goarch string
		// files maps a path to its contents; a path absent from the map reads as
		// "no such file or directory".
		files      map[string]string
		wantErr    bool
		errPhrases []string
	}{
		{
			name:  "ubuntu 20.04",
			goos:  "linux",
			files: map[string]string{"/etc/os-release": ubuntuOSRelease("20.04")},
		},
		{
			name:  "ubuntu 22.04",
			goos:  "linux",
			files: map[string]string{"/etc/os-release": ubuntuOSRelease("22.04")},
		},
		{
			name:  "ubuntu 24.04",
			goos:  "linux",
			files: map[string]string{"/etc/os-release": ubuntuOSRelease("24.04")},
		},
		{
			name: "point release keeps a supported VERSION_ID",
			goos: "linux",
			files: map[string]string{
				"/etc/os-release": "PRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\nID=ubuntu\nVERSION_ID=\"22.04\"\n",
			},
		},
		{
			name:  "os-release only under /usr/lib",
			goos:  "linux",
			files: map[string]string{"/usr/lib/os-release": ubuntuOSRelease("24.04")},
		},
		{
			name:       "ubuntu too old",
			goos:       "linux",
			files:      map[string]string{"/etc/os-release": ubuntuOSRelease("18.04")},
			wantErr:    true,
			errPhrases: []string{"18.04", "20.04, 22.04, 24.04"},
		},
		{
			name:       "ubuntu too new",
			goos:       "linux",
			files:      map[string]string{"/etc/os-release": ubuntuOSRelease("25.04")},
			wantErr:    true,
			errPhrases: []string{"25.04", "20.04, 22.04, 24.04"},
		},
		{
			name: "non-ubuntu linux distro",
			goos: "linux",
			files: map[string]string{
				"/etc/os-release": "PRETTY_NAME=\"Rocky Linux 9.4 (Blue Onyx)\"\nID=\"rocky\"\nVERSION_ID=\"9.4\"\n",
			},
			wantErr:    true,
			errPhrases: []string{"Rocky Linux", "Ubuntu"},
		},
		{
			name: "ubuntu derivative is not ubuntu",
			goos: "linux",
			files: map[string]string{
				"/etc/os-release": "PRETTY_NAME=\"Pop!_OS 22.04 LTS\"\nID=pop\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22.04\"\n",
			},
			wantErr:    true,
			errPhrases: []string{"Pop!_OS", "Ubuntu"},
		},
		{
			name:       "os-release unreadable",
			goos:       "linux",
			files:      map[string]string{},
			wantErr:    true,
			errPhrases: []string{"/etc/os-release"},
		},
		{
			name:       "os-release missing VERSION_ID",
			goos:       "linux",
			files:      map[string]string{"/etc/os-release": "PRETTY_NAME=\"Ubuntu\"\nID=ubuntu\n"},
			wantErr:    true,
			errPhrases: []string{"VERSION_ID"},
		},
		{
			name:       "non-linux OS",
			goos:       "darwin",
			files:      map[string]string{"/etc/os-release": ubuntuOSRelease("22.04")},
			wantErr:    true,
			errPhrases: []string{"Linux", "darwin"},
		},
		{
			name:       "unsupported architecture",
			goos:       "linux",
			goarch:     "arm64",
			files:      map[string]string{"/etc/os-release": ubuntuOSRelease("22.04")},
			wantErr:    true,
			errPhrases: []string{"arm64", "amd64"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goos = tt.goos
			goarch = tt.goarch
			if goarch == "" {
				goarch = supportedArch
			}
			osReadFile = func(name string) ([]byte, error) {
				data, ok := tt.files[name]
				if !ok {
					return nil, fmt.Errorf("open %s: no such file or directory", name)
				}
				return []byte(data), nil
			}

			err := checkSupportedPlatform()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, phrase := range tt.errPhrases {
				require.Contains(t, err.Error(), phrase)
			}
		})
	}
}

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name string
		data string
		want map[string]string
	}{
		{
			name: "quoted and unquoted values",
			data: "PRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\nID=ubuntu\nVERSION_ID=\"22.04\"\n",
			want: map[string]string{"PRETTY_NAME": "Ubuntu 22.04.5 LTS", "ID": "ubuntu", "VERSION_ID": "22.04"},
		},
		{
			name: "single quotes",
			data: "ID='ubuntu'\n",
			want: map[string]string{"ID": "ubuntu"},
		},
		{
			name: "comments and blank lines ignored",
			data: "# a comment\n\nID=ubuntu\n\n",
			want: map[string]string{"ID": "ubuntu"},
		},
		{
			name: "lines without = ignored",
			data: "garbage\nID=ubuntu\n",
			want: map[string]string{"ID": "ubuntu"},
		},
		{
			name: "value containing =",
			data: "HOME_URL=https://ubuntu.com/?a=b\n",
			want: map[string]string{"HOME_URL": "https://ubuntu.com/?a=b"},
		},
		{
			name: "empty input",
			data: "",
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseOSRelease([]byte(tt.data)))
		})
	}
}

// TestSupportedUbuntuVersionsMatchBundleScript keeps the onboarding gate honest: a host is only
// supportable if a byoh-bundle was published for its OS version, and .ci/build-push-bundle.sh is
// what publishes them. Adding a bundle there without widening supportedUbuntuVersions (or the
// reverse) fails here rather than at cluster-provisioning time on a customer's host.
func TestSupportedUbuntuVersionsMatchBundleScript(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "..", ".ci", "build-push-bundle.sh")
	data, err := os.ReadFile(scriptPath)
	require.NoError(t, err, "cannot read %s -- if the bundle build moved, update this test and supportedUbuntuVersions", scriptPath)

	// Matches a case arm plus the bundle it selects, e.g.:
	//     "22.04")
	//         BUNDLE_NAME="byoh-bundle-ubuntu_22.04_x86-64_k8s"
	armRe := regexp.MustCompile(`"(\d+\.\d+)"\)\s*\n\s*BUNDLE_NAME="([^"]+)"`)
	matches := armRe.FindAllStringSubmatch(string(data), -1)
	require.NotEmpty(t, matches, "found no UBUNTU_VERSION case arms in %s -- the script's shape changed", scriptPath)

	var scriptVersions []string
	for _, m := range matches {
		scriptVersions = append(scriptVersions, m[1])
		// supportedArch is asserted from the same source: every published bundle is x86-64, so
		// there is nothing for an arm64 host to install.
		require.Contains(t, m[2], "_x86-64", "bundle %q is not x86-64; supportedArch may no longer be %s", m[2], supportedArch)
	}

	require.ElementsMatch(t, supportedUbuntuVersions, scriptVersions,
		"supportedUbuntuVersions and the bundles published by %s have drifted apart", scriptPath)
}

func TestIsNTPSynchronized(t *testing.T) {
	origRun := runCommandWithStdout
	t.Cleanup(func() { runCommandWithStdout = origRun })

	tests := []struct {
		name   string
		output string
		err    error
		want   bool
	}{
		{name: "synchronized", output: "yes\n", want: true},
		{name: "not synchronized", output: "no\n", want: false},
		{name: "timedatectl unavailable", output: "", err: fmt.Errorf("exec: \"timedatectl\": executable file not found in $PATH"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCommandWithStdout = func(name string, args ...string) (string, error) {
				require.Equal(t, "timedatectl", name)
				require.Equal(t, []string{"show", "-p", "NTPSynchronized", "--value"}, args)
				return tt.output, tt.err
			}
			require.Equal(t, tt.want, isNTPSynchronized())
		})
	}
}

func TestParseNTPSynchronized(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "synchronized", output: "yes\n", want: true},
		{name: "not synchronized", output: "no\n", want: false},
		{name: "no trailing newline", output: "yes", want: true},
		{name: "empty output", output: "", want: false},
		{name: "unexpected output", output: "unknown\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseNTPSynchronized(tt.output))
		})
	}
}
