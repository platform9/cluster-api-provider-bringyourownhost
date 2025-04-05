package service

import (
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultDirPerms is the default directory permission
	DefaultDirPerms = 0755
	// DefaultFilePerms is the default file permission
	DefaultFilePerms = 0644

	// ByohAgentDebPackageURL is the URL to download the agent package
	ByohAgentDebPackageURL = "quay.io/platform9/byoh-agent-deb:0.1.78"
	// ByohAgentDebPackageFilename is the filename of the agent package
	ByohAgentDebPackageFilename = "pf9-byohost-agent.deb"
	// ByohAgentServiceName is the name of the agent service
	ByohAgentServiceName = "pf9-byohost-agent"
	// ByohAgentLogPath is the path to the BYOH agent log file
	ByohAgentLogPath = "/var/log/pf9/byoh/byoh-agent.log"
	// ByohConfigDir is the directory for BYOH configuration
	ByohConfigDir = ".byoh"

	// ImgPkgVersion is the version of imgpkg to install
	ImgPkgVersion = "v0.45.0"
	// ImgPkgURL is the URL to download imgpkg
	ImgPkgURL = "https://github.com/carvel-dev/imgpkg/releases/download/" + ImgPkgVersion + "/imgpkg-linux-amd64"
	// ImgPkgPath is the path where imgpkg will be installed
	ImgPkgPath = "/usr/local/bin/imgpkg"

	// Timeout for waiting for machineRef to be unset
	WaitForMachineRefToBeUnsetTimeout = 5 * time.Minute

	// Systemctl constants
	Systemctl = "systemctl"
)

var (
	HomeDir, _ = os.UserHomeDir()
	ByohDir    = filepath.Join(HomeDir, ByohConfigDir)

	KubeconfigFilePath = filepath.Join(ByohDir, "config")

	SystemctlServiceExists = []string{"list-unit-files", ByohAgentServiceName + ".service"}
)

// Config defines the structure of our kubeconfig file.
type Config struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			Namespace string `yaml:"namespace"`
			User      string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}
