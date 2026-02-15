package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(healthCmd)
}

var healthCmd = &cobra.Command{
	Use:     "health",
	Aliases: []string{"doctor"},
	Short:   "Check health of all services",
	RunE: func(cmd *cobra.Command, args []string) error {
		h, err := client.Health()
		if err != nil {
			return fmt.Errorf("health check failed: %w", err)
		}

		fmt.Println(style.Title.Render("norn health"))
		fmt.Println()

		for svc, status := range h.Services {
			dot := style.DotHealthy
			if status != "up" {
				dot = style.DotUnhealthy
			}
			fmt.Printf("  %s %s\n", dot, svc)
		}

		fmt.Println()
		if h.Status == "ok" {
			fmt.Println(style.Healthy.Render("  all systems operational"))
		} else {
			fmt.Println(style.Unhealthy.Render("  degraded"))
		}

		return nil
	},
}
