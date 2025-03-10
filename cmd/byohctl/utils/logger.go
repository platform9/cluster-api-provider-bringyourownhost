package utils

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fatih/color"
)

var (
	InfoColor    = color.New(color.FgCyan)
	ErrorColor   = color.New(color.FgRed)
	SuccessColor = color.New(color.FgGreen)
	WarnColor    = color.New(color.FgYellow)

	// File logger
	fileLogger *log.Logger
)

// InitLogger initializes the file logger
func InitLogger(logFilePath string) error {
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	fileLogger = log.New(logFile, "", log.LstdFlags)
	return nil
}

// Log to file only
func LogToFile(format string, args ...interface{}) {
	if fileLogger != nil {
		fileLogger.Printf(format, args...)
	}
}

// User-facing logs (stdout + file)
func LogInfo(format string, args ...interface{}) {
	InfoColor.Printf("ℹ️  INFO: "+format+"\n", args...)
	LogToFile("INFO: "+format, args...)
}

func LogError(format string, args ...interface{}) {
	ErrorColor.Printf("❌ ERROR: "+format+"\n", args...)
	LogToFile("ERROR: "+format, args...)
}

func LogSuccess(format string, args ...interface{}) {
	SuccessColor.Printf("✅ SUCCESS: "+format+"\n", args...)
	LogToFile("SUCCESS: "+format, args...)
}

func LogWarn(format string, args ...interface{}) {
	WarnColor.Printf("⚠️  WARNING: "+format+"\n", args...)
	LogToFile("WARNING: "+format, args...)
}

// Debug logs only go to file, not stdout
func LogDebug(format string, args ...interface{}) {
	LogToFile("DEBUG: "+format, args...)
}

// Track time in both stdout and logfile
func TrackTime(start time.Time, name string) {
	elapsed := time.Since(start)
	InfoColor.Printf("⏱️  %s took %s\n", name, elapsed)
	LogToFile("%s took %s", name, elapsed)
}

// New function: Log error and return formatted error
func LogErrorf(format string, args ...interface{}) error {
	LogError(format, args...)
	return fmt.Errorf(format, args...)
}
