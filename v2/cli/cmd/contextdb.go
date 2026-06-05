package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	contextDBReviewNamespace string
	contextDBReviewMode      string
	contextDBReviewLimit     int
	contextDBReviewWebURL    string

	contextDBWorkerRunsMode          string
	contextDBWorkerRunsAfter         string
	contextDBWorkerRunsLimit         int
	contextDBWorkerRunsJSON          bool
	contextDBWorkerRunsShowDecisions bool
	contextDBWorkerRunsWebURL        string
)

func init() {
	rootCmd.AddCommand(contextDBCmd)
	contextDBCmd.AddCommand(contextDBReviewCmd)
	contextDBCmd.AddCommand(contextDBWorkerRunsCmd)
	contextDBReviewCmd.Flags().StringVar(&contextDBReviewNamespace, "namespace", "hermes-agent", "ContextDB namespace")
	contextDBReviewCmd.Flags().StringVar(&contextDBReviewMode, "mode", "agent_memory", "ContextDB mode")
	contextDBReviewCmd.Flags().IntVar(&contextDBReviewLimit, "limit", 50, "Maximum review queue items to inspect")
	contextDBReviewCmd.Flags().StringVar(&contextDBReviewWebURL, "web-url", "", "Override ContextDB web URL")
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsMode, "mode", "agent_memory", "ContextDB mode")
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsAfter, "after", "", "Only show runs after this RFC3339 timestamp")
	contextDBWorkerRunsCmd.Flags().IntVar(&contextDBWorkerRunsLimit, "limit", 10, "Maximum runs to show")
	contextDBWorkerRunsCmd.Flags().BoolVar(&contextDBWorkerRunsJSON, "json", false, "Print raw JSON")
	contextDBWorkerRunsCmd.Flags().BoolVar(&contextDBWorkerRunsShowDecisions, "decisions", false, "Print decision details below each run")
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsWebURL, "web-url", "", "Override ContextDB web URL")
}

var contextDBCmd = &cobra.Command{
	Use:   "contextdb",
	Short: "Inspect ContextDB integration state",
}

var contextDBReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Summarize ContextDB review queue and worker activity",
	RunE: func(cmd *cobra.Command, args []string) error {
		queue, err := fetchContextDBReviewQueue(contextDBReviewNamespace, contextDBReviewMode, contextDBReviewLimit, contextDBReviewWebURL)
		if err != nil {
			return err
		}
		runs, err := fetchContextDBWorkerRuns(contextDBReviewNamespace, contextDBReviewMode, "", contextDBReviewWebURL)
		if err != nil {
			return err
		}
		if len(runs.Runs) > 5 {
			runs.Runs = runs.Runs[:5]
		}
		printContextDBReviewOverview(contextDBReviewNamespace, contextDBReviewMode, queue.Items, runs.Runs)
		return nil
	},
}

var contextDBWorkerRunsCmd = &cobra.Command{
	Use:   "worker-runs <namespace>",
	Short: "List ContextDB review worker run summaries",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		namespace := args[0]
		runs, err := fetchContextDBWorkerRuns(namespace, contextDBWorkerRunsMode, contextDBWorkerRunsAfter, contextDBWorkerRunsWebURL)
		if err != nil {
			return err
		}
		if contextDBWorkerRunsLimit > 0 && len(runs.Runs) > contextDBWorkerRunsLimit {
			runs.Runs = runs.Runs[:contextDBWorkerRunsLimit]
		}
		if contextDBWorkerRunsJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(runs)
		}
		printContextDBWorkerRuns(namespace, runs.Runs, contextDBWorkerRunsShowDecisions)
		return nil
	},
}

func fetchContextDBReviewQueue(namespace, mode string, limit int, webURL string) (*contextDBReviewQueueResponse, error) {
	cfg := contextDBSmokeConfig{WebURL: webURL}
	if err := discoverContextDBURLs(&cfg); err != nil {
		return nil, err
	}

	values := url.Values{}
	if mode != "" {
		values.Set("mode", mode)
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := fmt.Sprintf("%s/v1/namespaces/%s/review/queue", cfg.WebURL, url.PathEscape(namespace))
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var queue contextDBReviewQueueResponse
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if err := contextDBGetJSON(httpClient, path, &queue); err != nil {
		return nil, fmt.Errorf("review queue: %w", err)
	}
	return &queue, nil
}

func printContextDBReviewOverview(namespace, mode string, items []contextDBReviewItem, runs []contextDBWorkerRun) {
	fmt.Println(style.Title.Render("contextdb review"))
	fmt.Printf("namespace=%s mode=%s\n\n", namespace, mode)

	fmt.Println(style.Subtitle.Render("  queue"))
	fmt.Printf("  total %d\n", len(items))
	counts := map[string]int{}
	for _, item := range items {
		itemType := item.Type
		if itemType == "" {
			itemType = "unknown"
		}
		counts[itemType]++
	}
	if len(counts) == 0 {
		fmt.Println(style.DimText.Render("  no review queue items"))
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+style.TableHeader.Render("TYPE")+"\t"+style.TableHeader.Render("COUNT"))
		types := make([]string, 0, len(counts))
		for itemType := range counts {
			types = append(types, itemType)
		}
		sort.Strings(types)
		for _, itemType := range types {
			fmt.Fprintf(w, "  %s\t%d\n", itemType, counts[itemType])
		}
		w.Flush()
	}
	fmt.Println()

	fmt.Println(style.Subtitle.Render("  recent worker runs"))
	if len(runs) == 0 {
		fmt.Println(style.DimText.Render("  no worker runs found"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  "+style.TableHeader.Render("GENERATED")+"\t"+
		style.TableHeader.Render("EVALUATOR")+"\t"+
		style.TableHeader.Render("DRY")+"\t"+
		style.TableHeader.Render("SCANNED")+"\t"+
		style.TableHeader.Render("APPLIED")+"\t"+
		style.TableHeader.Render("ERRORS"))
	for _, run := range runs {
		generated := run.GeneratedAt
		if ts, err := time.Parse(time.RFC3339Nano, run.GeneratedAt); err == nil {
			generated = ts.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "  %s\t%s\t%t\t%d\t%d\t%d\n",
			generated,
			run.Evaluator,
			run.DryRun,
			run.Scanned,
			run.Applied,
			run.Errors,
		)
	}
	w.Flush()
}

func fetchContextDBWorkerRuns(namespace, mode, after, webURL string) (*contextDBWorkerRunsResponse, error) {
	if strings.TrimSpace(after) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(after)); err != nil {
			return nil, fmt.Errorf("invalid --after timestamp: %w", err)
		}
	}

	cfg := contextDBSmokeConfig{WebURL: webURL}
	if err := discoverContextDBURLs(&cfg); err != nil {
		return nil, err
	}

	values := url.Values{}
	if mode != "" {
		values.Set("mode", mode)
	}
	if after != "" {
		values.Set("after", after)
	}
	path := fmt.Sprintf("%s/v1/namespaces/%s/review/worker-runs", cfg.WebURL, url.PathEscape(namespace))
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var runs contextDBWorkerRunsResponse
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if err := contextDBGetJSON(httpClient, path, &runs); err != nil {
		return nil, fmt.Errorf("worker runs: %w", err)
	}
	return &runs, nil
}

func printContextDBWorkerRuns(namespace string, runs []contextDBWorkerRun, showDecisions bool) {
	if len(runs) == 0 {
		fmt.Println(style.DimText.Render("no worker runs found for " + namespace))
		return
	}

	fmt.Println(style.Title.Render("contextdb worker runs for " + namespace))
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("GENERATED")+"\t"+
		style.TableHeader.Render("CYCLE")+"\t"+
		style.TableHeader.Render("MODE")+"\t"+
		style.TableHeader.Render("EVALUATOR")+"\t"+
		style.TableHeader.Render("DRY")+"\t"+
		style.TableHeader.Render("SCANNED")+"\t"+
		style.TableHeader.Render("APPLIED")+"\t"+
		style.TableHeader.Render("SKIPPED")+"\t"+
		style.TableHeader.Render("ERRORS")+"\t"+
		style.TableHeader.Render("DECISIONS"))

	for _, run := range runs {
		generated := run.GeneratedAt
		if ts, err := time.Parse(time.RFC3339Nano, run.GeneratedAt); err == nil {
			generated = ts.Local().Format("2006-01-02 15:04:05")
		}
		cycle := run.CycleID
		if len(cycle) > 8 {
			cycle = cycle[:8]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%d\t%d\t%d\t%d\t%d\n",
			generated,
			cycle,
			run.Mode,
			run.Evaluator,
			run.DryRun,
			run.Scanned,
			run.Applied,
			run.Skipped,
			run.Errors,
			len(run.Decisions),
		)
	}
	w.Flush()

	if showDecisions {
		printContextDBWorkerRunDecisions(runs)
	}
}

func printContextDBWorkerRunDecisions(runs []contextDBWorkerRun) {
	fmt.Println()
	for _, run := range runs {
		cycle := run.CycleID
		if len(cycle) > 8 {
			cycle = cycle[:8]
		}
		if len(run.Decisions) == 0 {
			fmt.Printf("%s %s\n", style.DimText.Render(cycle), style.DimText.Render("no decisions"))
			continue
		}

		fmt.Printf("%s decisions\n", style.Bold.Render(cycle))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+style.TableHeader.Render("TYPE")+"\t"+
			style.TableHeader.Render("ACTION")+"\t"+
			style.TableHeader.Render("APPLIED")+"\t"+
			style.TableHeader.Render("NODE")+"\t"+
			style.TableHeader.Render("REASON"))
		for _, decision := range run.Decisions {
			nodeID := decision.NodeID
			if len(nodeID) > 8 {
				nodeID = nodeID[:8]
			}
			fmt.Fprintf(w, "  %s\t%s\t%t\t%s\t%s\n",
				decision.Type,
				decision.Action,
				decision.Applied,
				nodeID,
				decision.Reason,
			)
		}
		w.Flush()
	}
}
