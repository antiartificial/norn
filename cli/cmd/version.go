package cmd

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"norn/cli/style"
)

var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		logo := lipgloss.NewStyle().
			Bold(true).
			Foreground(style.Primary).
			Render(`
  ┌─┐┌─┐┬─┐┌┐┌
  │││││ │├┬┘│││
  ┘└┘└─┘┴└─┘└┘`)

		fmt.Println(logo)
		fmt.Println()
		fmt.Printf("  %s %s\n", style.Key.Render("Version"), style.Val.Render(Version))
		fmt.Printf("  %s %s\n", style.Key.Render("API"), style.Val.Render(apiURL))
		fmt.Println()
		fmt.Println(style.DimText.Render("  The three fates weave your infrastructure."))
		fmt.Println()
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
