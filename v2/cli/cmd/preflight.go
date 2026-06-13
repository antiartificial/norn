package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(preflightCmd)
}

var preflightCmd = &cobra.Command{
	Use:     "preflight <app> [ref]",
	Aliases: []string{"check"},
	Short:   "Run a deploy rehearsal without mutating runtime state",
	Args:    cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		ref := "HEAD"
		if len(args) > 1 {
			ref = args[1]
		}

		fmt.Println(style.Title.Render("preflighting " + appID))
		fmt.Printf("  ref: %s\n\n", ref)

		sagaID, err := client.Preflight(appID, ref)
		if err != nil {
			return fmt.Errorf("preflight failed: %w", err)
		}

		fmt.Printf("  saga: %s\n\n", style.DimText.Render(sagaID))

		return streamSagaEvents(sagaID)
	},
}
