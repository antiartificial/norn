package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(canaryCmd)
	rootCmd.AddCommand(promoteCmd)
}

var canaryCmd = &cobra.Command{
	Use:   "canary <app>",
	Short: "Show canary deployment status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		status, err := client.CanaryStatus(appID)
		if err != nil {
			return fmt.Errorf("failed to get canary status: %w", err)
		}

		fmt.Println(style.Title.Render("canary status for " + appID))
		fmt.Println()

		if status.Status == "none" {
			fmt.Println(style.DimText.Render("  no active deployment"))
			return nil
		}

		fmt.Printf("  %s %s\n", style.Key.Render("id"), status.ID)
		fmt.Printf("  %s %s\n", style.Key.Render("job"), status.JobID)
		fmt.Printf("  %s %s\n", style.Key.Render("status"), status.Status)
		if status.StatusDescription != "" {
			fmt.Printf("  %s %s\n", style.Key.Render("description"), status.StatusDescription)
		}

		canaryLabel := style.DimText.Render("no")
		if status.IsCanary {
			canaryLabel = style.Warning.Render("yes")
		}
		fmt.Printf("  %s %s\n", style.Key.Render("canary"), canaryLabel)

		return nil
	},
}

var promoteCmd = &cobra.Command{
	Use:   "promote <app>",
	Short: "Promote canary deployment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		if err := client.PromoteCanary(appID); err != nil {
			return fmt.Errorf("promote failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("canary promoted for " + appID))
		return nil
	},
}
