package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(rollbackCmd)
	rollbackCmd.Flags().StringVar(&rollbackIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
}

var rollbackIdempotencyKey string

var rollbackCmd = &cobra.Command{
	Use:   "rollback <app>",
	Short: "Roll back to previous successful deployment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]

		fmt.Println(style.Title.Render("rolling back " + appID))

		key, err := requestIdempotencyKey(cmd, rollbackIdempotencyKey, "norn-rollback")
		if err != nil {
			return err
		}
		accepted, err := client.Rollback(appID, key)
		if err != nil {
			return fmt.Errorf("rollback failed: %w", err)
		}

		fmt.Printf("  saga: %s\n\n", style.DimText.Render(accepted.SagaID))

		return finishEnqueue(accepted)
	},
}
