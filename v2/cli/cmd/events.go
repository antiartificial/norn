package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	eventsApp      string
	eventsType     string
	eventsSeverity string
	eventsLimit    int
)

func init() {
	rootCmd.AddCommand(eventsCmd)
	eventsCmd.Flags().StringVar(&eventsApp, "app", "", "Filter events by app")
	eventsCmd.Flags().StringVar(&eventsType, "type", "", "Filter events by type")
	eventsCmd.Flags().StringVar(&eventsSeverity, "severity", "", "Filter events by severity")
	eventsCmd.Flags().IntVar(&eventsLimit, "limit", 25, "Maximum events to show")
}

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Show recent Norn Beacon events",
	RunE: func(cmd *cobra.Command, args []string) error {
		events, total, err := client.ListEvents(eventsApp, eventsType, eventsSeverity, eventsLimit)
		if err != nil {
			return err
		}
		printBeaconEvents(events, total)
		return nil
	},
}

func printBeaconEvents(events []api.BeaconEvent, total int) {
	fmt.Println(style.Title.Render("norn events"))
	if len(events) == 0 {
		fmt.Println(style.DimText.Render("  no events"))
		return
	}
	fmt.Printf("%s %d\n\n", style.Key.Render("total"), total)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("TIME")+"\t"+
		style.TableHeader.Render("SEVERITY")+"\t"+
		style.TableHeader.Render("TYPE")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("TITLE"))
	for _, event := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			localTime(event.OccurredAt),
			renderSeverity(event.Severity),
			event.Type,
			emptyDash(event.App),
			emptyDash(event.Title),
		)
	}
	w.Flush()
}

func renderSeverity(severity string) string {
	switch severity {
	case "critical":
		return style.Unhealthy.Render(severity)
	case "warning":
		return style.Warning.Render(severity)
	default:
		return style.Healthy.Render(severity)
	}
}
