package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(runtimeCmd)
}

var runtimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Show container runtime configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := client.RuntimeInfo()
		if err != nil {
			return fmt.Errorf("runtime info: %w", err)
		}

		fmt.Println(style.Title.Render("container runtime"))
		fmt.Println()

		active := info.Active
		dot := style.DotHealthy
		if !active.Available {
			dot = style.DotUnhealthy
		}

		fmt.Printf("  %s backend:    %s\n", dot, style.Bold.Render(string(active.Backend)))
		fmt.Printf("    version:    %s\n", style.DimText.Render(active.Version))
		fmt.Printf("    build cmd:  %s\n", style.DimText.Render(active.BuildCmd))
		fmt.Printf("    task driver: %s\n", style.DimText.Render(active.TaskDriver))

		if len(active.Capabilities) > 0 {
			fmt.Printf("    capabilities: %s\n", style.DimText.Render(strings.Join(active.Capabilities, ", ")))
		}

		fmt.Println()
		fmt.Println(style.Bold.Render("  available backends"))
		for _, b := range info.Backends {
			bdot := style.DotDim
			label := b.Name
			if b.Available {
				bdot = style.DotHealthy
			}
			if b.Current {
				label += " (active)"
			}
			fmt.Printf("    %s %s\n", bdot, label)
		}
		fmt.Println()

		return nil
	},
}
