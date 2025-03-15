package utils

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"os/exec"
)

// Log level constants
const (
	LevelDebug   = "DEBUG"
	LevelInfo    = "INFO"
	LevelSuccess = "SUCCESS"
	LevelWarning = "WARNING"
	LevelError   = "ERROR"
)

// Console output levels
const (
	ConsoleOutputAll      = "all"      // Show all log messages
	ConsoleOutputImportant = "important" // Show only important messages (INFO, SUCCESS, WARNING, ERROR)
	ConsoleOutputMinimal  = "minimal"  // Show only SUCCESS, WARNING, ERROR
	ConsoleOutputCritical = "critical" // Show only WARNING and ERROR
	ConsoleOutputNone     = "none"     // Don't show any messages on console
)

var (
	// Logger instance
	debugLogger  *log.Logger

	// File handle for logger
	debugLogFile *os.File

	// Console output configuration
	consoleOutputEnabled = true
	consoleOutputLevel   = ConsoleOutputMinimal // Default to minimal messages only
)

// InitLoggers initializes the consolidated debug logger
func InitLoggers(logDir string, debugEnabled bool) error {
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %v", err)
	}

	// Define log file path - only use a single debug file
	debugLogPath := filepath.Join(logDir, "byoh-agent-debug.log")
	
	// Always create a new log file when the command is run
	// Open debug log file with truncate flag to overwrite any existing content
	var err error
	debugLogFile, err = os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open debug log file: %v", err)
	}

	// Initialize logger
	debugLogger = log.New(debugLogFile, "", 0)

	// Write header to log file
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(debugLogFile, "===== BYOHCTL SESSION STARTED AT %s =====\n\n", timestamp)

	LogInfo("Logger initialized with logs at %s", debugLogPath)
	return nil
}

// CloseLoggers closes the logger file handles
func CloseLoggers() {
	if debugLogFile != nil {
		// Add timestamp for session end
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(debugLogFile, "\n===== BYOHCTL SESSION ENDED AT %s =====\n\n", timestamp)
		
		debugLogFile.Close()
	}
}

// DisableConsoleOutput disables logging to the console
func DisableConsoleOutput() {
	consoleOutputEnabled = false
}

// EnableConsoleOutput enables logging to the console
func EnableConsoleOutput() {
	consoleOutputEnabled = true
}

// SetConsoleOutputLevel sets the level of detail for console output
func SetConsoleOutputLevel(level string) {
	switch level {
	case ConsoleOutputAll, ConsoleOutputImportant, ConsoleOutputMinimal, ConsoleOutputCritical, ConsoleOutputNone:
		consoleOutputLevel = level
	default:
		LogWarn("Invalid console output level: %s. Using default (important)", level)
		consoleOutputLevel = ConsoleOutputImportant
	}
}

// shouldShowOnConsole determines if a log message should be displayed on the console
func shouldShowOnConsole(level string) bool {
	if !consoleOutputEnabled {
		return false
	}

	switch consoleOutputLevel {
	case ConsoleOutputAll:
		return true
	case ConsoleOutputImportant:
		return level != LevelDebug
	case ConsoleOutputMinimal:
		return level == LevelSuccess || level == LevelWarning || level == LevelError
	case ConsoleOutputCritical:
		return level == LevelWarning || level == LevelError
	case ConsoleOutputNone:
		return false
	default:
		return true
	}
}

// LogDebug logs a debug message to the debug log file
func LogDebug(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s", timestamp, LevelDebug, message)

	// Log to console if enabled and level matches
	if shouldShowOnConsole(LevelDebug) {
		fmt.Println(logMessage)
	}

	// Log to debug file
	if debugLogger != nil {
		debugLogger.Println(logMessage)
	}
}

// LogInfo logs an info message to the debug log file
func LogInfo(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s", timestamp, LevelInfo, message)

	// Log to console if enabled and level matches
	if shouldShowOnConsole(LevelInfo) {
		fmt.Println(logMessage)
	}

	// Log to debug file
	if debugLogger != nil {
		debugLogger.Println(logMessage)
	}
}

// LogSuccess logs a success message to the debug log file
func LogSuccess(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s", timestamp, LevelSuccess, message)

	// Log to console if enabled and level matches
	if shouldShowOnConsole(LevelSuccess) {
		fmt.Printf("\033[0;32m%s\033[0m\n", logMessage) // Green color
	}

	// Log to debug file
	if debugLogger != nil {
		debugLogger.Println(logMessage)
	}
}

// LogWarn logs a warning message to the debug log file
func LogWarn(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s", timestamp, LevelWarning, message)

	// Log to console if enabled and level matches
	if shouldShowOnConsole(LevelWarning) {
		fmt.Printf("\033[0;33m%s\033[0m\n", logMessage) // Yellow color
	}

	// Log to debug file
	if debugLogger != nil {
		debugLogger.Println(logMessage)
	}
}

// LogError logs an error message to the debug log file
func LogError(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s", timestamp, LevelError, message)

	// Log to console if enabled and level matches
	if shouldShowOnConsole(LevelError) {
		fmt.Printf("\033[0;31m%s\033[0m\n", logMessage) // Red color
	}

	// Log to debug file
	if debugLogger != nil {
		debugLogger.Println(logMessage)
	}
}

// LogErrorf logs an error message and returns an error with the same message
func LogErrorf(format string, args ...interface{}) error {
	LogError(format, args...)
	return fmt.Errorf(format, args...)
}

// TrackTime logs the time taken for an operation
func TrackTime(start time.Time, name string) {
	elapsed := time.Since(start)
	LogDebug("%s took %s", name, elapsed)
}

// ProcessExists checks if a process with the given PID exists
func ProcessExists(pid string) (bool, error) {
	// Use the standard ps command to check if process exists
	cmd := exec.Command("ps", "-p", pid)
	err := cmd.Run()
	
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			// ps returns exit code 1 when the process doesn't exist
			if exitError.ExitCode() == 1 {
				return false, nil
			}
			return false, fmt.Errorf("ps command failed with exit code %d", exitError.ExitCode())
		}
		return false, fmt.Errorf("error checking process status: %v", err)
	}
	
	return true, nil
}
