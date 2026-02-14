package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(rollbackCmd)
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback <app>",
	Short: "Roll back to previous successful deployment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		fmt.Println(style.Title.Render("rolling back " + appID))

		sagaID, err := client.Rollback(appID)
		if err != nil {
			return fmt.Errorf("rollback failed: %w", err)
		}

		fmt.Printf("  saga: %s\n\n", style.DimText.Render(sagaID))

		return streamSagaEvents(sagaID)
	},
}
