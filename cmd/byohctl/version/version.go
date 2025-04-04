package version

// Version is the version of byohctl
var Version string

// GetVersion returns the version string
func GetVersion() string {
    if Version == "" {
        Version = "0.0.0"
    }
    return Version
}
