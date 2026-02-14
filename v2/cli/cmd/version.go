package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var Version = "dev"

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show norn CLI and API version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("%s %s\n", style.Bold.Render("norn cli"), Version)

		health, err := client.Health()
		if err != nil {
			fmt.Printf("%s %s\n", style.DimText.Render("api"), style.Unhealthy.Render("unreachable"))
			return nil
		}
		fmt.Printf("%s %s\n", style.DimText.Render("api"), health.Status)
		for svc, status := range health.Services {
			dot := style.NomadStatusDot(status)
			fmt.Printf("  %s %s %s\n", dot, svc, status)
		}
		return nil
	},
}
