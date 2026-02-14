package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(deployCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy <app> [ref]",
	Short: "Deploy an app",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		ref := "HEAD"
		if len(args) > 1 {
			ref = args[1]
		}

		fmt.Println(style.Title.Render("deploying " + appID))
		fmt.Printf("  ref: %s\n\n", ref)

		sagaID, err := client.Deploy(appID, ref)
		if err != nil {
			return fmt.Errorf("deploy failed: %w", err)
		}

		fmt.Printf("  saga: %s\n\n", style.DimText.Render(sagaID))

		return streamSagaEvents(sagaID)
	},
}
