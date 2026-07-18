package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/version"
)

const (
	// DefaultDirPerms is the default directory permission
	DefaultDirPerms = 0755
	// DefaultFilePerms is the default file permission
	DefaultFilePerms = 0644

	// byohAgentDebRepo is the OCI repo that holds the agent deb bundle.
	// The tag is resolved at runtime by byohAgentBundleURL, not hardcoded.
	byohAgentDebRepo = "quay.io/platform9/cluster-api-provider-bringyourownhost/agent"
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

	// pcd-kaapi region key
	PcdKaapiRegionKey = "pcd-kaapi.pf9.io/region"
)

// byohAgentBundleURL returns the OCI bundle reference for the agent deb
// package matching byohctl's own build version. byohctl and the agent
// bundle are published from the same commit under the same
// git-describe tag (see .github/workflows/build-push-agent-bundle.yml),
// so there is no separate agent version to track by hand.
func byohAgentBundleURL() string {
	return fmt.Sprintf("%s:%s", byohAgentDebRepo, version.GetVersion())
}

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
