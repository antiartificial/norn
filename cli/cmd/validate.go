package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"norn/cli/api"
	"norn/cli/style"
)

var validateCmd = &cobra.Command{
	Use:     "validate [app]",
	Short:   "Validate infraspec configuration for all apps or a specific app",
	Aliases: []string{"check", "lint"},
	Args:    cobra.MaximumNArgs(1),
	RunE:    runValidate,
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(cmd *cobra.Command, args []string) error {
	if len(args) == 1 {
		return validateOne(args[0])
	}
	return validateAll()
}

func validateAll() error {
	results, err := client.ValidateAll()
	if err != nil {
		return fmt.Errorf("failed to validate: %w", err)
	}

	if len(results) == 0 {
		fmt.Println(style.DimText.Render("No apps discovered."))
		return nil
	}

	fmt.Println(style.Banner.Render("⚡ NORN VALIDATE"))
	fmt.Println()

	hasErrors := false
	for _, r := range results {
		printResultRow(r)
		if r.Errors > 0 {
			hasErrors = true
		}
	}
	fmt.Println()

	// Summary
	totalErrors := 0
	totalWarnings := 0
	for _, r := range results {
		totalErrors += r.Errors
		totalWarnings += r.Warnings
	}

	if hasErrors {
		fmt.Println(style.ErrorBox.Render(fmt.Sprintf("  %d error(s), %d warning(s) across %d app(s)  ", totalErrors, totalWarnings, len(results))))
		os.Exit(1)
	}

	fmt.Println(style.SuccessBox.Render(fmt.Sprintf("  All %d app(s) passed validation  ", len(results))))
	return nil
}

func validateOne(appID string) error {
	result, err := client.ValidateApp(appID)
	if err != nil {
		return fmt.Errorf("failed to validate %s: %w", appID, err)
	}

	fmt.Println(style.Banner.Render("⚡ NORN VALIDATE"))
	fmt.Println()
	printResultRow(*result)
	fmt.Println()

	if result.Errors > 0 {
		os.Exit(1)
	}

	return nil
}

func printResultRow(r api.ValidationResult) {
	name := style.Bold.Render(padRight(r.App, 24))

	if r.Errors == 0 && r.Warnings == 0 {
		fmt.Printf("  %s %s\n", name, style.Healthy.Render("PASS"))
		return
	}

	var parts []string
	if r.Errors > 0 {
		parts = append(parts, style.Unhealthy.Render(fmt.Sprintf("FAIL  %d error(s)", r.Errors)))
	}
	if r.Warnings > 0 {
		parts = append(parts, style.Warning.Render(fmt.Sprintf("%d warning(s)", r.Warnings)))
	}
	fmt.Printf("  %s %s\n", name, strings.Join(parts, "  "))

	for _, f := range r.Findings {
		dot := style.DotDim
		switch f.Severity {
		case "error":
			dot = style.DotUnhealthy
		case "warning":
			dot = style.DotWarning
		}

		tag := ""
		if f.Field != "" {
			tag = " " + style.DimText.Render("["+f.Field+"]")
		}

		fmt.Printf("    %s %s%s\n", dot, f.Message, tag)
	}
}
