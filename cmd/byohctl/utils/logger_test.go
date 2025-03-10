// utils/logger_test.go
package utils

import (
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLogToFile(t *testing.T) {
	// Create temp file for logs
	tmpFile, err := ioutil.TempFile("", "test-log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Initialize logger with temp file
	err = InitLogger(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}

	// Log some messages - Use %s format specifier with the message
	testMessage := "Test log message"
	LogToFile("%s", testMessage)

	// Close file (fileLogger will keep it open otherwise)
	fileLogger = nil

	// Read log file
	content, err := ioutil.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Verify content
	if !strings.Contains(string(content), testMessage) {
		t.Errorf("Log file doesn't contain the test message: %s", string(content))
	}
}

func TestLogErrorf(t *testing.T) {
	// Create temp file for logs
	tmpFile, err := ioutil.TempFile("", "test-log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Initialize logger with temp file
	err = InitLogger(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}

	// Log error
	testError := "Test error %s"
	testParam := "param"
	returnedErr := LogErrorf(testError, testParam)

	// Verify returned error
	expectedErrMsg := "Test error param"
	if returnedErr.Error() != expectedErrMsg {
		t.Errorf("LogErrorf returned unexpected error: expected %q, got %q", expectedErrMsg, returnedErr.Error())
	}

	// Close file (fileLogger will keep it open otherwise)
	fileLogger = nil

	// Read log file
	content, err := ioutil.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Verify content
	if !strings.Contains(string(content), expectedErrMsg) {
		t.Errorf("Log file doesn't contain the error message: %s", string(content))
	}
}

func TestTrackTime(t *testing.T) {
	// Create temp file for logs
	tmpFile, err := ioutil.TempFile("", "test-log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Initialize logger with temp file
	err = InitLogger(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitLogger failed: %v", err)
	}

	// Track time
	start := time.Now().Add(-1 * time.Second) // Simulate 1 second elapsed
	TrackTime(start, "Test operation")

	// Close file
	fileLogger = nil

	// Read log file
	content, err := ioutil.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Verify content contains time tracking message
	if !strings.Contains(string(content), "Test operation took") {
		t.Errorf("Log file doesn't contain time tracking message: %s", string(content))
	}
}
