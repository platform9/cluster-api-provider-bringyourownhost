// cmd/byohctl/main.go
package main

import (
	"os"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/cmd"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/version"
)

// Version information set by linker flags
var (
	buildVersion string
	buildCommit  string
)

func init() {
	// Set version information
	if buildVersion != "" {
		version.Version = buildVersion
	}
	if buildCommit != "" {
		version.GitCommit = buildCommit
	}
}

func main() {
	if err := cmd.Execute(); err != nil {
		utils.LogError("Command execution failed: %s", err.Error())
		os.Exit(1)
	}
}
