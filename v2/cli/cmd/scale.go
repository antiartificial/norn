package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

func init() {
	rootCmd.AddCommand(scaleCmd)
}

var scaleCmd = &cobra.Command{
	Use:   "scale <app> <group> <count>",
	Short: "Scale an app's task group",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		appID := args[0]
		group := args[1]
		count, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("invalid count %q: %w", args[2], err)
		}

		fmt.Println(style.Title.Render("scaling " + appID))
		fmt.Printf("  %s %s → %d\n\n", style.DimText.Render("group:"), group, count)

		if err := client.Scale(appID, group, count); err != nil {
			return fmt.Errorf("scale failed: %w", err)
		}

		fmt.Println(style.SuccessBox.Render(fmt.Sprintf("scaled %s/%s to %d", appID, group, count)))
		return nil
	},
}
