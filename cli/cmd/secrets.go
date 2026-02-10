package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/cli/style"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets <app>",
	Short: "List secret names for an app",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecrets,
}

func init() {
	rootCmd.AddCommand(secretsCmd)
}

func runSecrets(cmd *cobra.Command, args []string) error {
	appID := args[0]

	secrets, err := client.ListSecrets(appID)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	if len(secrets) == 0 {
		fmt.Println(style.DimText.Render("No secrets configured for " + appID))
		return nil
	}

	fmt.Println(style.Banner.Render("🔑 SECRETS") + "  " + style.Bold.Render(appID))
	fmt.Println()

	for _, name := range secrets {
		fmt.Printf("  %s  %s\n", style.DotDim, style.Val.Render(name))
	}

	fmt.Println()
	fmt.Println(style.DimText.Render(fmt.Sprintf("  %d secret(s) — values are encrypted at rest", len(secrets))))
	return nil
}
