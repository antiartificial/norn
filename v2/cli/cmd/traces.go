package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	tracesSince       time.Duration
	tracesLimit       int
	tracesMinDuration time.Duration
)

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func init() {
	tracesCmd.Flags().DurationVar(&tracesSince, "since", time.Hour, "Look back this far, such as 30m or 2h")
	tracesCmd.Flags().IntVar(&tracesLimit, "limit", 20, "Maximum traces to list (1-100)")
	tracesCmd.Flags().DurationVar(&tracesMinDuration, "min-duration", 0, "Only traces slower than this, such as 250ms")
	rootCmd.AddCommand(tracesCmd)
}

var tracesCmd = &cobra.Command{
	Use:   "traces <app> [traceId]",
	Short: "List recent traces of an app, or show one trace as a span tree",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 2 {
			if !traceIDPattern.MatchString(args[1]) {
				return fmt.Errorf("trace id must be 32 lowercase hex characters")
			}
			trace, err := client.GetTrace(args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Println(style.Title.Render("trace " + trace.TraceID))
			fmt.Print(renderTraceTree(trace))
			return nil
		}
		if tracesSince <= 0 {
			return fmt.Errorf("--since must be positive")
		}
		traces, err := client.SearchTraces(args[0], time.Now().Add(-tracesSince), tracesLimit, int(tracesMinDuration.Milliseconds()))
		if err != nil {
			return err
		}
		if len(traces) == 0 {
			fmt.Println(style.DimText.Render("  no traces found"))
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, style.TableHeader.Render("TRACE ID")+"\t"+
			style.TableHeader.Render("ROOT")+"\t"+
			style.TableHeader.Render("SERVICE")+"\t"+
			style.TableHeader.Render("START")+"\t"+
			style.TableHeader.Render("DURATION")+"\t"+
			style.TableHeader.Render("SPANS"))
		for _, t := range traces {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", t.TraceID, t.RootName, t.RootService, t.Start, formatSpanDuration(t.DurationMs), t.SpanCount)
		}
		return w.Flush()
	},
}

func formatSpanDuration(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.0fus", ms*1000)
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.2fs", ms/1000)
	}
	return fmt.Sprintf("%.1fms", ms)
}

// renderTraceTree prints spans indented under their parents. A span whose
// parent is absent (filtered out, or not yet received) is shown as a root.
func renderTraceTree(trace *api.Trace) string {
	byID := make(map[string]bool, len(trace.Spans))
	for _, span := range trace.Spans {
		byID[span.SpanID] = true
	}
	children := map[string][]api.TraceSpan{}
	var roots []api.TraceSpan
	for _, span := range trace.Spans {
		if span.ParentSpanID == "" || !byID[span.ParentSpanID] {
			roots = append(roots, span)
		} else {
			children[span.ParentSpanID] = append(children[span.ParentSpanID], span)
		}
	}
	var b strings.Builder
	seen := map[string]bool{}
	var walk func(span api.TraceSpan, depth int)
	walk = func(span api.TraceSpan, depth int) {
		if seen[span.SpanID] {
			return
		}
		seen[span.SpanID] = true
		line := fmt.Sprintf("%s%s  %s  %s", strings.Repeat("  ", depth), span.Name, style.DimText.Render(span.Service), formatSpanDuration(span.DurationMs))
		if span.Status == "error" {
			line = style.Unhealthy.Render(fmt.Sprintf("%s%s  %s  %s  ERROR", strings.Repeat("  ", depth), span.Name, span.Service, formatSpanDuration(span.DurationMs)))
			if span.StatusMsg != "" {
				line += " " + style.Unhealthy.Render(span.StatusMsg)
			}
		}
		b.WriteString(line + "\n")
		for _, child := range children[span.SpanID] {
			walk(child, depth+1)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	if trace.OmittedSpans > 0 {
		fmt.Fprintf(&b, "%s\n", style.DimText.Render(fmt.Sprintf("  %d span(s) from other apps omitted", trace.OmittedSpans)))
	}
	return b.String()
}
