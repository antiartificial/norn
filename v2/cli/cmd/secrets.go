package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	secretsCmd.AddCommand(secretsListCmd)
	secretsCmd.AddCommand(secretsSetCmd)
	secretsCmd.AddCommand(secretsDeleteCmd)
	rootCmd.AddCommand(secretsCmd)
}

var secretsCmd = &cobra.Command{
	Use:   "secrets <app>",
	Short: "Manage secrets for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Default to list for backwards compat
		return listSecrets(args[0])
	},
}

var secretsListCmd = &cobra.Command{
	Use:   "list <app>",
	Short: "List secret keys for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return listSecrets(args[0])
	},
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <app> KEY=VALUE ...",
	Short: "Set secrets for an app",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		secrets := make(map[string]string)
		for _, kv := range args[1:] {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid format %q, expected KEY=VALUE", kv)
			}
			secrets[parts[0]] = parts[1]
		}

		if err := client.UpdateSecrets(appID, secrets); err != nil {
			return fmt.Errorf("failed to set secrets: %w", err)
		}

		fmt.Println(style.SuccessBox.Render(fmt.Sprintf("set %d secret(s) for %s", len(secrets), appID)))
		return nil
	},
}

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <app> KEY ...",
	Short: "Delete secrets from an app",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		for _, key := range args[1:] {
			if err := client.DeleteSecret(appID, key); err != nil {
				return fmt.Errorf("failed to delete %s: %w", key, err)
			}
			fmt.Printf("  %s deleted %s\n", style.StepDone.Render("✓"), key)
		}
		return nil
	},
}

func listSecrets(appID string) error {
	secrets, err := client.ListSecrets(appID)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	fmt.Println(style.Title.Render("secrets for " + appID))

	if len(secrets) == 0 {
		fmt.Println(style.DimText.Render("  no secrets configured"))
		return nil
	}

	for _, s := range secrets {
		fmt.Printf("  %s %s\n", style.DimText.Render("•"), s)
	}
	return nil
}
