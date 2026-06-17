package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var accessLimit int
var accessPatternWindow string
var accessPatternIdleAfter string
var accessObserveProcess string
var accessObserveEndpoint string
var accessObserveSource string
var accessObserveStatus int
var accessObserveCount int64
var accessObserveAt string
var accessCloudflareSyncWindow string

func init() {
	accessCmd.Flags().IntVar(&accessLimit, "limit", 25, "Number of recent access events")
	accessPatternsCmd.Flags().StringVar(&accessPatternWindow, "window", "14d", "Observation lookback window as a duration or day count")
	accessPatternsCmd.Flags().StringVar(&accessPatternIdleAfter, "idle-after", "7d", "Quiet duration before a service is marked as an idle candidate")
	accessObserveCmd.Flags().StringVar(&accessObserveProcess, "process", "web", "Process name for the observation")
	accessObserveCmd.Flags().StringVar(&accessObserveEndpoint, "endpoint", "", "Endpoint or hostname observed")
	accessObserveCmd.Flags().StringVar(&accessObserveSource, "source", "manual", "Observation source, such as cloudflared, gateway, or manual")
	accessObserveCmd.Flags().IntVar(&accessObserveStatus, "status", 200, "HTTP status bucket for the observation")
	accessObserveCmd.Flags().Int64Var(&accessObserveCount, "count", 1, "Number of requests represented by this observation")
	accessObserveCmd.Flags().StringVar(&accessObserveAt, "at", "", "Observation timestamp in RFC3339 format")
	accessCloudflareSyncCmd.Flags().StringVar(&accessCloudflareSyncWindow, "window", "14d", "Cloudflare GraphQL lookback window")
	accessCmd.AddCommand(accessPatternsCmd)
	accessCmd.AddCommand(accessObserveCmd)
	accessCmd.AddCommand(accessCloudflareCmd)
	accessCloudflareCmd.AddCommand(accessCloudflareStatusCmd)
	accessCloudflareCmd.AddCommand(accessCloudflareSyncCmd)
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

var accessPatternsCmd = &cobra.Command{
	Use:   "patterns",
	Short: "Show hosted-service access patterns and idle candidates",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := client.AccessPatterns(accessPatternWindow, accessPatternIdleAfter)
		if err != nil {
			return fmt.Errorf("failed to fetch access patterns: %w", err)
		}
		if len(resp.Patterns) == 0 {
			fmt.Println(style.DimText.Render("no access patterns available"))
			return nil
		}
		fmt.Println(style.Title.Render("access patterns"))
		fmt.Println()
		fmt.Printf("  window=%dh idleAfter=%dh\n\n", resp.WindowHours, resp.IdleAfterHours)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("PROCESS")+"\t"+
			style.TableHeader.Render("REQ")+"\t"+
			style.TableHeader.Render("LAST")+"\t"+
			style.TableHeader.Render("QUIET")+"\t"+
			style.TableHeader.Render("PEAK")+"\t"+
			style.TableHeader.Render("ACTION")+"\t"+
			style.TableHeader.Render("CONF"))
		for _, pattern := range resp.Patterns {
			fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				pattern.App,
				pattern.Process,
				pattern.TotalRequests,
				shortTime(pattern.LastSeen),
				formatQuietHours(pattern.QuietForHours),
				formatPeakHour(pattern.PeakHourUTC),
				renderIdleAction(pattern),
				formatConfidence(pattern.Confidence),
			)
		}
		return w.Flush()
	},
}

var accessObserveCmd = &cobra.Command{
	Use:   "observe <app>",
	Short: "Record a hosted-service access observation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		obs := api.AccessObservation{
			App:      args[0],
			Process:  accessObserveProcess,
			Endpoint: accessObserveEndpoint,
			Source:   accessObserveSource,
			Status:   accessObserveStatus,
			Count:    accessObserveCount,
		}
		if strings.TrimSpace(accessObserveAt) != "" {
			obs.ObservedAt = strings.TrimSpace(accessObserveAt)
		}
		recorded, err := client.RecordAccessObservations([]api.AccessObservation{obs})
		if err != nil {
			return fmt.Errorf("failed to record access observation: %w", err)
		}
		fmt.Printf("recorded %d access observation(s)\n", recorded)
		return nil
	},
}

var accessCloudflareCmd = &cobra.Command{
	Use:   "cloudflare",
	Short: "Inspect and import Cloudflare access observations",
}

var accessCloudflareStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Cloudflare access observation readiness",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := client.CloudflareAccessStatus()
		if err != nil {
			return fmt.Errorf("failed to fetch cloudflare status: %w", err)
		}
		fmt.Println(style.Title.Render("cloudflare access"))
		fmt.Println()
		fmt.Printf("  graphql: token=%t zone=%t configured=%t\n", status.APITokenConfigured, status.ZoneIDConfigured, status.Configured)
		fmt.Printf("  logpush: token=%t\n", status.LogpushConfigured)
		fmt.Printf("  hostnames: %d\n", status.HostnameCount)
		for _, hostname := range status.Hostnames {
			fmt.Printf("    %s\n", hostname)
		}
		return nil
	},
}

var accessCloudflareSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync hosted-service access observations from Cloudflare GraphQL",
	RunE: func(cmd *cobra.Command, args []string) error {
		receipt, err := client.CloudflareAccessSync(accessCloudflareSyncWindow)
		if err != nil {
			return fmt.Errorf("failed to sync cloudflare access observations: %w", err)
		}
		fmt.Println(style.Title.Render("cloudflare sync"))
		fmt.Println()
		fmt.Printf("  window=%dh recorded=%d errors=%d\n\n", receipt.WindowHours, receipt.Recorded, len(receipt.Errors))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+
			style.TableHeader.Render("HOST")+"\t"+
			style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("PROCESS")+"\t"+
			style.TableHeader.Render("BUCKETS")+"\t"+
			style.TableHeader.Render("STATUS"))
		for _, host := range receipt.Hosts {
			status := style.Healthy.Render("ok")
			if host.Error != "" {
				status = style.Unhealthy.Render(host.Error)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%s\n", host.Hostname, host.App, host.Process, host.Recorded, status)
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
	if value == "" {
		return "-"
	}
	if len(value) >= 19 {
		return value[:19]
	}
	return value
}

func formatQuietHours(value *float64) string {
	if value == nil {
		return "-"
	}
	hours := *value
	if hours >= 48 {
		return fmt.Sprintf("%.1fd", hours/24)
	}
	return fmt.Sprintf("%.1fh", hours)
}

func formatPeakHour(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%02d:00Z", *value)
}

func renderIdleAction(pattern api.AccessPattern) string {
	switch {
	case pattern.IdleCandidate && pattern.RecommendedAction == "consider_idle":
		return style.Warning.Render(pattern.RecommendedAction)
	case pattern.IdleCandidate:
		return style.DimText.Render(pattern.RecommendedAction)
	default:
		return style.Healthy.Render(pattern.RecommendedAction)
	}
}
