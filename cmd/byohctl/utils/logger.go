// cmd/byohctl/utils/logger.go
package utils

import (
    "time"
    "github.com/fatih/color"
)

var (
    InfoColor  = color.New(color.FgCyan)
    ErrorColor = color.New(color.FgRed)
    SuccessColor = color.New(color.FgGreen)
    WarnColor = color.New(color.FgYellow)
)

func LogInfo(format string, args ...interface{}) {
    InfoColor.Printf("ℹ️  INFO: "+format+"\n", args...)
}

func LogError(format string, args ...interface{}) {
    ErrorColor.Printf("❌ ERROR: "+format+"\n", args...)
}

func LogSuccess(format string, args ...interface{}) {
    SuccessColor.Printf("✅ SUCCESS: "+format+"\n", args...)
}

func LogWarn(format string, args ...interface{}) {
    WarnColor.Printf("⚠️  WARNING: "+format+"\n", args...)
}

func TrackTime(start time.Time, name string) {
    elapsed := time.Since(start)
    InfoColor.Printf("⏱️  %s took %s\n", name, elapsed)
}