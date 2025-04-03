package version

var (
	// Version is the version of byohctl
	Version string

	// GitCommit is the git commit hash
	GitCommit string
)

// GetVersion returns the full version string
func GetVersion() string {
	if Version == "" {
		Version = "dev"
	}
	if GitCommit != "" {
		return Version + " (" + GitCommit + ")"
	}
	return Version
}
