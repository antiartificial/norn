package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(validateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate [app]",
	Short: "Validate infraspec files",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			result, err := client.ValidateApp(args[0])
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}
			printValidation(result)
			return nil
		}

		results, err := client.ValidateAll()
		if err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
		for i, r := range results {
			printValidation(&r)
			if i < len(results)-1 {
				fmt.Println()
			}
		}
		return nil
	},
}

func printValidation(r *api.ValidationResult) {
	status := style.Healthy.Render("✓ valid")
	if !r.Valid {
		status = style.Unhealthy.Render("✗ invalid")
	}
	fmt.Printf("%s  %s\n", style.Bold.Render(r.App), status)

	for _, f := range r.Findings {
		var icon string
		var s = style.DimText
		switch f.Severity {
		case "error":
			icon = "✗"
			s = style.StepFailed
		case "warning":
			icon = "!"
			s = style.Warning
		default:
			icon = "·"
		}
		fmt.Printf("  %s %s  %s\n", s.Render(icon), style.DimText.Render(f.Field), f.Message)
	}
}
