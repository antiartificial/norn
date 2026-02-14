package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(forgeCmd)
	rootCmd.AddCommand(teardownCmd)
}

var forgeCmd = &cobra.Command{
	Use:   "forge <app>",
	Short: "Provision cloudflared routing for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		fmt.Println(style.Title.Render("forging " + appID))

		if err := client.Forge(appID); err != nil {
			return fmt.Errorf("forge failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("cloudflared routing configured"))
		return nil
	},
}

var teardownCmd = &cobra.Command{
	Use:   "teardown <app>",
	Short: "Remove cloudflared routing for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		fmt.Println(style.Title.Render("tearing down " + appID))

		if err := client.Teardown(appID); err != nil {
			return fmt.Errorf("teardown failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("cloudflared routing removed"))
		return nil
	},
}
