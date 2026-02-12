package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var invokeBody string

var invokeCmd = &cobra.Command{
	Use:   "invoke <app>",
	Short: "Invoke a function app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		app := args[0]

		body := invokeBody
		if body == "" {
			body = "{}"
		}

		// If body starts with @, read from file
		if strings.HasPrefix(body, "@") {
			data, err := os.ReadFile(body[1:])
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			body = string(data)
		}

		resp, err := client.HTTPClient.Post(
			client.BaseURL+"/api/apps/"+app+"/invoke",
			"application/json",
			strings.NewReader(body),
		)
		if err != nil {
			return fmt.Errorf("invoke failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
		}

		var result struct {
			Status     string `json:"status"`
			ExitCode   int    `json:"exitCode"`
			Output     string `json:"output"`
			DurationMs int64  `json:"durationMs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}

		statusStyle := lipgloss.NewStyle().Bold(true)
		if result.Status == "succeeded" {
			statusStyle = statusStyle.Foreground(style.Green)
		} else {
			statusStyle = statusStyle.Foreground(style.Red)
		}

		fmt.Printf("\n  %s %s\n", style.Key.Render("Status"), statusStyle.Render(result.Status))
		fmt.Printf("  %s %s\n", style.Key.Render("Exit"), style.Val.Render(fmt.Sprintf("%d", result.ExitCode)))
		fmt.Printf("  %s %s\n", style.Key.Render("Duration"), style.Val.Render(fmt.Sprintf("%dms", result.DurationMs)))
		if result.Output != "" {
			fmt.Printf("\n  %s\n", style.DimText.Render("Output:"))
			fmt.Printf("  %s\n", result.Output)
		}
		fmt.Println()

		return nil
	},
}

func init() {
	invokeCmd.Flags().StringVar(&invokeBody, "body", "", "Request body (or @filename)")
	rootCmd.AddCommand(invokeCmd)
}
