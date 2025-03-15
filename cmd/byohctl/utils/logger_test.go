// utils/logger_test.go
package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogFiles(t *testing.T) {
	// Create temp directory for logs
	tempDir, err := os.MkdirTemp("", "test-logs")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize loggers with temp directory
	err = InitLoggers(tempDir, true)
	if err != nil {
		t.Fatalf("InitLoggers failed: %v", err)
	}

	// Test various log levels
	infoTestMessage := "Test info log message"
	LogInfo("%s", infoTestMessage)
	
	debugTestMessage := "Test debug log message"
	LogDebug("%s", debugTestMessage)
	
	errorTestMessage := "Test error log message"
	LogError("%s", errorTestMessage)

	// Close loggers
	CloseLoggers()

	// Read debug log file (the only log file now)
	debugLogPath := filepath.Join(tempDir, "byoh-agent-debug.log")
	debugContent, err := os.ReadFile(debugLogPath)
	if err != nil {
		t.Fatalf("Failed to read debug log file: %v", err)
	}

	// Check that all messages are in the debug log
	debugContentStr := string(debugContent)
	if !strings.Contains(debugContentStr, infoTestMessage) {
		t.Errorf("Info message not found in debug log")
	}
	if !strings.Contains(debugContentStr, debugTestMessage) {
		t.Errorf("Debug message not found in debug log")
	}
	if !strings.Contains(debugContentStr, errorTestMessage) {
		t.Errorf("Error message not found in debug log")
	}
	if !strings.Contains(debugContentStr, "BYOHCTL SESSION STARTED") {
		t.Errorf("Session start header not found in debug log")
	}
	if !strings.Contains(debugContentStr, "BYOHCTL SESSION ENDED") {
		t.Errorf("Session end header not found in debug log")
	}
}

func TestConsoleOutputLevels(t *testing.T) {
	// Create temp directory for logs
	tempDir, err := os.MkdirTemp("", "test-logs")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize loggers with temp directory
	err = InitLoggers(tempDir, true)
	if err != nil {
		t.Fatalf("InitLoggers failed: %v", err)
	}
	defer CloseLoggers()

	// Test console output levels
	tests := []struct {
		level          string
		debugShouldLog bool
		infoShouldLog  bool
		warnShouldLog  bool
		errorShouldLog bool
	}{
		{ConsoleOutputAll, true, true, true, true},
		{ConsoleOutputImportant, false, true, true, true},
		{ConsoleOutputMinimal, false, false, true, true},
		{ConsoleOutputCritical, false, false, true, true},
		{ConsoleOutputNone, false, false, false, false},
	}

	for _, test := range tests {
		SetConsoleOutputLevel(test.level)
		
		if shouldShowOnConsole(LevelDebug) != test.debugShouldLog {
			t.Errorf("For level %s, debug should show on console: %v", test.level, test.debugShouldLog)
		}
		
		if shouldShowOnConsole(LevelInfo) != test.infoShouldLog {
			t.Errorf("For level %s, info should show on console: %v", test.level, test.infoShouldLog)
		}
		
		if shouldShowOnConsole(LevelWarning) != test.warnShouldLog {
			t.Errorf("For level %s, warning should show on console: %v", test.level, test.warnShouldLog)
		}
		
		if shouldShowOnConsole(LevelError) != test.errorShouldLog {
			t.Errorf("For level %s, error should show on console: %v", test.level, test.errorShouldLog)
		}
	}
}

func TestLogErrorf(t *testing.T) {
	// Create temp directory for logs
	tempDir, err := os.MkdirTemp("", "test-logs")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize loggers with temp directory
	err = InitLoggers(tempDir, true)
	if err != nil {
		t.Fatalf("InitLoggers failed: %v", err)
	}
	defer CloseLoggers()

	// Log error
	testError := "Test error %s"
	testParam := "param"
	returnedErr := LogErrorf(testError, testParam)

	// Verify returned error
	expectedErrMsg := "Test error param"
	if returnedErr.Error() != expectedErrMsg {
		t.Errorf("LogErrorf returned unexpected error: expected %q, got %q", expectedErrMsg, returnedErr.Error())
	}

	// Read debug log file
	debugLogPath := filepath.Join(tempDir, "byoh-agent-debug.log")
	debugContent, err := os.ReadFile(debugLogPath)
	if err != nil {
		t.Fatalf("Failed to read debug log file: %v", err)
	}

	// Verify content
	if !strings.Contains(string(debugContent), expectedErrMsg) {
		t.Errorf("Debug log file doesn't contain the error message: %s", string(debugContent))
	}
}

func TestTimeTracking(t *testing.T) {
	// Create temp directory for logs
	tempDir, err := os.MkdirTemp("", "test-logs")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize loggers with temp directory
	err = InitLoggers(tempDir, true)
	if err != nil {
		t.Fatalf("InitLoggers failed: %v", err)
	}
	defer CloseLoggers()

	// Test TrackTime function
	start := time.Now()
	time.Sleep(100 * time.Millisecond) // Sleep to ensure we get a measurable time
	TrackTime(start, "test function")

	// Read debug log file
	debugLogPath := filepath.Join(tempDir, "byoh-agent-debug.log")
	debugContent, err := os.ReadFile(debugLogPath)
	if err != nil {
		t.Fatalf("Failed to read debug log file: %v", err)
	}

	// Check that the time tracking message is in the debug log
	debugContentStr := string(debugContent)
	if !strings.Contains(debugContentStr, "test function took") {
		t.Errorf("Time tracking message not found in debug log")
	}
}
