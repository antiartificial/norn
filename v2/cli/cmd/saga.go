package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	sagaApp   string
	sagaLimit int
)

func init() {
	sagaCmd.Flags().StringVar(&sagaApp, "app", "", "filter by app")
	sagaCmd.Flags().IntVar(&sagaLimit, "limit", 30, "number of events")
	rootCmd.AddCommand(sagaCmd)
}

var sagaCmd = &cobra.Command{
	Use:   "saga [saga-id]",
	Short: "View saga event log",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			// Show events for a specific saga
			sagaID := args[0]
			events, err := client.GetSagaEvents(sagaID)
			if err != nil {
				return fmt.Errorf("fetch saga events: %w", err)
			}

			fmt.Println(style.Title.Render("saga " + sagaID[:8]))
			fmt.Println()

			for _, evt := range events {
				ts, _ := time.Parse(time.RFC3339Nano, evt.Timestamp)
				icon := actionIcon(evt.Action)
				fmt.Printf("  %s %s %s\n",
					style.DimText.Render(ts.Format("15:04:05")),
					icon,
					evt.Message,
				)
			}
			return nil
		}

		// List recent events
		events, err := client.ListRecentSaga(sagaApp, sagaLimit)
		if err != nil {
			return fmt.Errorf("fetch saga events: %w", err)
		}

		if len(events) == 0 {
			fmt.Println(style.DimText.Render("no saga events"))
			return nil
		}

		title := "recent saga events"
		if sagaApp != "" {
			title = "saga events for " + sagaApp
		}
		fmt.Println(style.Title.Render(title))
		fmt.Println()

		for _, evt := range events {
			ts, _ := time.Parse(time.RFC3339Nano, evt.Timestamp)
			icon := actionIcon(evt.Action)
			app := style.DimText.Render(fmt.Sprintf("[%s]", evt.App))
			fmt.Printf("  %s %s %s %s\n",
				style.DimText.Render(ts.Format("15:04:05")),
				app,
				icon,
				evt.Message,
			)
		}
		return nil
	},
}

func actionIcon(action string) string {
	switch action {
	case "step.start":
		return style.StepRunning.Render("▶")
	case "step.complete":
		return style.StepDone.Render("✓")
	case "step.failed":
		return style.StepFailed.Render("✗")
	case "deploy.complete":
		return style.Healthy.Render("✓")
	case "deploy.failed":
		return style.Unhealthy.Render("✗")
	default:
		return style.DimText.Render("·")
	}
}
