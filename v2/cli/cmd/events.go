package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	eventActor     string
	eventNote      string
	eventDuration  string
	eventUntil     string
)

func init() {
	rootCmd.AddCommand(eventsCmd)
	eventsCmd.Flags().StringVar(&eventsApp, "app", "", "Filter events by app")
	eventsCmd.Flags().StringVar(&eventsType, "type", "", "Filter events by type")
	eventsCmd.Flags().StringVar(&eventsSeverity, "severity", "", "Filter events by severity")
	eventsCmd.Flags().IntVar(&eventsLimit, "limit", 25, "Maximum events to show")
	eventsCmd.AddCommand(eventsShowCmd)
	eventsCmd.AddCommand(eventsAckCmd)
	eventsCmd.AddCommand(eventsSnoozeCmd)
	eventsCmd.AddCommand(eventsOpenCmd)
	eventsAckCmd.Flags().StringVar(&eventActor, "by", "", "Operator name")
	eventsAckCmd.Flags().StringVar(&eventNote, "note", "", "Acknowledgement note")
	eventsSnoozeCmd.Flags().StringVar(&eventActor, "by", "", "Operator name")
	eventsSnoozeCmd.Flags().StringVar(&eventNote, "note", "", "Snooze note")
	eventsSnoozeCmd.Flags().StringVar(&eventDuration, "for", "1h", "Snooze duration, such as 30m or 2h")
	eventsSnoozeCmd.Flags().StringVar(&eventUntil, "until", "", "Snooze until RFC3339 timestamp")
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

var eventsShowCmd = &cobra.Command{
	Use:   "show <event-id>",
	Short: "Show a Beacon event with metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event, err := client.GetEvent(args[0])
		if err != nil {
			return err
		}
		printBeaconEventDetail(*event)
		return nil
	},
}

var eventsAckCmd = &cobra.Command{
	Use:   "ack <event-id>",
	Short: "Acknowledge a Beacon event",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event, err := client.AcknowledgeEvent(args[0], eventActor, eventNote)
		if err != nil {
			return err
		}
		fmt.Printf("acknowledged %s (%s)\n", shortID(event.ID), event.State)
		return nil
	},
}

var eventsSnoozeCmd = &cobra.Command{
	Use:   "snooze <event-id>",
	Short: "Snooze a Beacon event",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event, err := client.SnoozeEvent(args[0], eventActor, eventNote, eventDuration, eventUntil)
		if err != nil {
			return err
		}
		fmt.Printf("snoozed %s until %s\n", shortID(event.ID), emptyDash(event.SnoozedUntil))
		return nil
	},
}

var eventsOpenCmd = &cobra.Command{
	Use:   "open <event-id>",
	Short: "Reopen a Beacon event",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		event, err := client.OpenEvent(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("opened %s (%s)\n", shortID(event.ID), event.State)
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
	fmt.Fprintln(w, style.TableHeader.Render("ID")+"\t"+
		style.TableHeader.Render("TIME")+"\t"+
		style.TableHeader.Render("SEVERITY")+"\t"+
		style.TableHeader.Render("STATE")+"\t"+
		style.TableHeader.Render("TYPE")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("TITLE"))
	for _, event := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(event.ID),
			localTime(event.OccurredAt),
			renderSeverity(event.Severity),
			renderEventState(event.State),
			event.Type,
			emptyDash(event.App),
			emptyDash(event.Title),
		)
	}
	w.Flush()
}

func printBeaconEventDetail(event api.BeaconEvent) {
	fmt.Println(style.Title.Render("event " + shortID(event.ID)))
	fmt.Println()
	fmt.Printf("%s %s\n", style.Key.Render("id"), event.ID)
	fmt.Printf("%s %s\n", style.Key.Render("time"), localTime(event.OccurredAt))
	fmt.Printf("%s %s\n", style.Key.Render("severity"), renderSeverity(event.Severity))
	fmt.Printf("%s %s\n", style.Key.Render("state"), renderEventState(event.State))
	fmt.Printf("%s %s\n", style.Key.Render("type"), event.Type)
	fmt.Printf("%s %s\n", style.Key.Render("app"), emptyDash(event.App))
	fmt.Printf("%s %s\n", style.Key.Render("title"), event.Title)
	if event.Body != "" {
		fmt.Printf("%s %s\n", style.Key.Render("body"), event.Body)
	}
	if event.DedupeKey != "" {
		fmt.Printf("%s %s\n", style.Key.Render("dedupe"), event.DedupeKey)
	}
	if event.AcknowledgedAt != "" {
		fmt.Printf("%s %s by %s\n", style.Key.Render("ack"), localTime(event.AcknowledgedAt), emptyDash(event.AcknowledgedBy))
	}
	if event.SnoozedUntil != "" {
		fmt.Printf("%s %s\n", style.Key.Render("snoozed until"), localTime(event.SnoozedUntil))
	}
	if event.AcknowledgementNote != "" {
		fmt.Printf("%s %s\n", style.Key.Render("note"), event.AcknowledgementNote)
	}
	if len(event.Metadata) > 0 {
		fmt.Println()
		fmt.Println(style.TableHeader.Render("METADATA"))
		keys := make([]string, 0, len(event.Metadata))
		for key := range event.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %s: %v\n", key, event.Metadata[key])
		}
	}
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

func renderEventState(state string) string {
	switch state {
	case "acknowledged":
		return style.Healthy.Render(state)
	case "snoozed":
		return style.Warning.Render(state)
	case "":
		return style.DimText.Render("open")
	default:
		return style.DimText.Render(state)
	}
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
