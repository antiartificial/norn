package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	addIngressOperationFlags(forgeCmd)
	addIngressOperationFlags(teardownCmd)
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

		key, err := requestIdempotencyKey(cmd, ingressIdempotencyKey, "norn-forge")
		if err != nil {
			return err
		}
		op, err := client.Forge(appID, key)
		if err != nil {
			return fmt.Errorf("forge failed: %w", err)
		}
		return handleIngressOperation(cmd, op, "cloudflared forge")
	},
}

var teardownCmd = &cobra.Command{
	Use:   "teardown <app>",
	Short: "Remove cloudflared routing for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		fmt.Println(style.Title.Render("tearing down " + appID))

		key, err := requestIdempotencyKey(cmd, ingressIdempotencyKey, "norn-teardown")
		if err != nil {
			return err
		}
		op, err := client.Teardown(appID, key)
		if err != nil {
			return fmt.Errorf("teardown failed: %w", err)
		}
		return handleIngressOperation(cmd, op, "cloudflared teardown")
	},
}
