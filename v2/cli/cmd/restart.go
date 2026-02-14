package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart <app>",
	Short: "Restart an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		fmt.Println(style.Title.Render("restarting " + appID))

		if err := client.Restart(appID); err != nil {
			return fmt.Errorf("restart failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render("restart initiated"))
		return nil
	},
}
