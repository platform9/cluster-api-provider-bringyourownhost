package cmd

import (
	"fmt"

	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/service"
	"github.com/platform9/cluster-api-provider-bringyourownhost/cmd/byohctl/utils"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "byohctl",
	Short: "BYOH control tool for Platform9",
	Long: `BYOH (Bring Your Own Host) control tool for Platform9.
This tool helps onboard hosts to your Platform9 deployment.`,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Initialize loggers
		if err := utils.InitLoggers(service.ByohDir, true); err != nil {
			return fmt.Errorf("failed to initialize loggers: %v", err)
		}
		return nil
	},
}

func Execute() error {
	return rootCmd.Execute()
}
