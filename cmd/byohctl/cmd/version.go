package cmd

import (
	"fmt"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/version"
	"github.com/spf13/cobra"
)

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Print(version.GetVersion())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
