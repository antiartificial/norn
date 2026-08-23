package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

func init() {
	validateCmd.Flags().BoolVar(&validateStrictSecrets, "strict-secrets", false, "Treat plaintext secret-like env values as validation errors")
	validateCmd.Flags().StringVarP(&validateFile, "file", "f", "", "Validate an InfraSpec document instead of a discovered app")
	validateCmd.Flags().StringVar(&validateFleetContext, "fleet", "", "Cross-check placement.nodePool against a fleet Cluster document")
	rootCmd.AddCommand(validateCmd)
}

var validateStrictSecrets bool
var validateFile string
var validateFleetContext string

var validateCmd = &cobra.Command{
	Use:   "validate [app]",
	Short: "Validate infraspec files",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		invalid := false
		if validateFile != "" {
			if len(args) != 0 {
				return fmt.Errorf("app argument cannot be combined with --file")
			}
			document, err := os.ReadFile(validateFile)
			if err != nil {
				return fmt.Errorf("read infraspec: %w", err)
			}
			var fleetDocument []byte
			if validateFleetContext != "" {
				fleetDocument, err = os.ReadFile(validateFleetContext)
				if err != nil {
					return fmt.Errorf("read fleet context: %w", err)
				}
			}
			result, err := client.ValidateInfraSpecDocument(string(document), string(fleetDocument), validateStrictSecrets)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}
			printValidation(result)
			if !result.Valid {
				return fmt.Errorf("infraspec document is invalid")
			}
			return nil
		}
		if validateFleetContext != "" {
			return fmt.Errorf("--fleet requires --file")
		}
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
