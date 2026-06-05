package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var accessLimit int

func init() {
	accessCmd.Flags().IntVar(&accessLimit, "limit", 25, "Number of recent access events")
	rootCmd.AddCommand(accessCmd)
}

var accessCmd = &cobra.Command{
	Use:   "access",
	Short: "Show recent Norn API access events",
	RunE: func(cmd *cobra.Command, args []string) error {
		events, err := client.AccessEvents(accessLimit)
		if err != nil {
			return fmt.Errorf("failed to fetch access events: %w", err)
		}
		if len(events) == 0 {
			fmt.Println(style.DimText.Render("no access events recorded"))
			return nil
		}
		fmt.Println(style.Title.Render("norn access"))
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("TIME")+"\t"+
			style.TableHeader.Render("STATUS")+"\t"+
			style.TableHeader.Render("METHOD")+"\t"+
			style.TableHeader.Render("PATH")+"\t"+
			style.TableHeader.Render("CLIENT")+"\t"+
			style.TableHeader.Render("CF USER")+"\t"+
			style.TableHeader.Render("MS"))
		for _, event := range events {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				shortTime(event.Timestamp),
				renderHTTPStatus(event.Status),
				event.Method,
				event.Path,
				firstNonEmpty(event.ClientIP, event.Forwarded, event.CFIP, "-"),
				firstNonEmpty(event.CFEmail, "-"),
				event.DurationMs)
		}
		return w.Flush()
	},
}

func renderHTTPStatus(status int) string {
	switch {
	case status >= 500:
		return style.Unhealthy.Render(fmt.Sprintf("%d", status))
	case status >= 400:
		return style.Warning.Render(fmt.Sprintf("%d", status))
	default:
		return style.Healthy.Render(fmt.Sprintf("%d", status))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shortTime(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 19 {
		return value[:19]
	}
	return value
}
