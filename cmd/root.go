package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gorsync",
	Short: "Gorsync is a CLI tool for backing up and synchronizing data efficiently",
	Long: `Gorsync is a powerful and flexible CLI tool that allows you to back up your data
from a source directory to a destination, supporting both real-time and scheduled sync.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	// Add any global flags or settings here if needed
}
