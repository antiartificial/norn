package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/style"
)

var (
	contextDBWorkerRunsMode   string
	contextDBWorkerRunsAfter  string
	contextDBWorkerRunsLimit  int
	contextDBWorkerRunsJSON   bool
	contextDBWorkerRunsWebURL string
)

func init() {
	rootCmd.AddCommand(contextDBCmd)
	contextDBCmd.AddCommand(contextDBWorkerRunsCmd)
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsMode, "mode", "agent_memory", "ContextDB mode")
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsAfter, "after", "", "Only show runs after this RFC3339 timestamp")
	contextDBWorkerRunsCmd.Flags().IntVar(&contextDBWorkerRunsLimit, "limit", 10, "Maximum runs to show")
	contextDBWorkerRunsCmd.Flags().BoolVar(&contextDBWorkerRunsJSON, "json", false, "Print raw JSON")
	contextDBWorkerRunsCmd.Flags().StringVar(&contextDBWorkerRunsWebURL, "web-url", "", "Override ContextDB web URL")
}

var contextDBCmd = &cobra.Command{
	Use:   "contextdb",
	Short: "Inspect ContextDB integration state",
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
		printContextDBWorkerRuns(namespace, runs.Runs)
		return nil
	},
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

func printContextDBWorkerRuns(namespace string, runs []contextDBWorkerRun) {
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
}
