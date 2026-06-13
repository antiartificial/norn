package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	validateCmd.Flags().BoolVar(&validateStrictSecrets, "strict-secrets", false, "Treat plaintext secret-like env values as validation errors")
	rootCmd.AddCommand(validateCmd)
}

var validateStrictSecrets bool

var validateCmd = &cobra.Command{
	Use:   "validate [app]",
	Short: "Validate infraspec files",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		invalid := false
		if len(args) == 1 {
			result, err := client.ValidateApp(args[0], validateStrictSecrets)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}
			printValidation(result)
			invalid = !result.Valid
			if validateStrictSecrets && invalid {
				return fmt.Errorf("validation failed strict secret gate")
			}
			return nil
		}

		results, err := client.ValidateAll(validateStrictSecrets)
		if err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
		for i, r := range results {
			printValidation(&r)
			if !r.Valid {
				invalid = true
			}
			if i < len(results)-1 {
				fmt.Println()
			}
		}
		if validateStrictSecrets && invalid {
			return fmt.Errorf("validation failed strict secret gate")
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
