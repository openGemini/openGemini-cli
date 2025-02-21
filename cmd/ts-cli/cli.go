package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	rootCmd = &cobra.Command{
		Use:   "ts-cli",
		Short: "openGemini client interactive CLI.",
		Long:  `CNCF openGemini client interactive command-line interface.`,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd:   true,
			DisableNoDescFlag:   true,
			DisableDescriptions: true,
		},
		Run: func(cmd *cobra.Command, args []string) {

		},
	}
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("execute command failed: %s\n", err)
	}
}
