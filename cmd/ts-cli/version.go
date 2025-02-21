package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openGemini/openGemini-cli/internal/common"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display the version of openGemini CLI",
	Long:  `Display the version of openGemini CLI.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(common.FullVersion("ts-cli"))
	},
}
