package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var restartIdempotencyKey string

func init() {
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart <app>",
	Short: "Replace an app's active allocations",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		key, err := requestIdempotencyKey(cmd, restartIdempotencyKey, "restart-"+appID)
		if err != nil {
			return err
		}

		fmt.Println(style.Title.Render("restarting " + appID))

		op, err := client.Restart(appID, key)
		if err != nil {
			return fmt.Errorf("restart failed: %w", err)
		}
		fmt.Println(style.SuccessBox.Render("restart accepted: " + op.ID))
		return nil
	},
}

func init() {
	restartCmd.Flags().StringVar(&restartIdempotencyKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
}
