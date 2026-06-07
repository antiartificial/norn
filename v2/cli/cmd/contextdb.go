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

	contextDBPolicyWorkerURL string
	contextDBPolicyJSON      bool

	contextDBAuditNamespace string
	contextDBAuditMode      string
	contextDBAuditLimit     int
	contextDBAuditAfter     string
	contextDBAuditWebURL    string

	contextDBSmokeJSON bool

	contextDBRollbackNamespace string
	contextDBRollbackMode      string
	contextDBRollbackReason    string
	contextDBRollbackOwner     string
)

func init() {
	rootCmd.AddCommand(contextDBCmd)
	contextDBCmd.AddCommand(contextDBReviewCmd)
	contextDBCmd.AddCommand(contextDBWorkerRunsCmd)
	contextDBCmd.AddCommand(contextDBPolicyCmd)
	contextDBCmd.AddCommand(contextDBAuditCmd)
	contextDBCmd.AddCommand(contextDBEvaluatorSmokeCmd)
	contextDBCmd.AddCommand(contextDBRollbackCmd)
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
	contextDBPolicyCmd.Flags().StringVar(&contextDBPolicyWorkerURL, "worker-url", "", "Override ContextDB review worker URL")
	contextDBPolicyCmd.Flags().BoolVar(&contextDBPolicyJSON, "json", false, "Print raw JSON")
	contextDBAuditCmd.Flags().StringVar(&contextDBAuditNamespace, "namespace", "hermes-agent", "ContextDB namespace")
	contextDBAuditCmd.Flags().StringVar(&contextDBAuditMode, "mode", "agent_memory", "ContextDB mode")
	contextDBAuditCmd.Flags().IntVar(&contextDBAuditLimit, "limit", 20, "Maximum audit events to show")
	contextDBAuditCmd.Flags().StringVar(&contextDBAuditAfter, "after", "", "Only show events after this RFC3339 timestamp")
	contextDBAuditCmd.Flags().StringVar(&contextDBAuditWebURL, "web-url", "", "Override ContextDB web URL")
	contextDBEvaluatorSmokeCmd.Flags().BoolVar(&contextDBSmokeJSON, "json", false, "Print raw JSON")
	contextDBRollbackCmd.Flags().StringVar(&contextDBRollbackNamespace, "namespace", "hermes-agent", "ContextDB namespace")
	contextDBRollbackCmd.Flags().StringVar(&contextDBRollbackMode, "mode", "agent_memory", "ContextDB mode")
	contextDBRollbackCmd.Flags().StringVar(&contextDBRollbackReason, "reason", "operator rollback through norn", "Rollback reason")
	contextDBRollbackCmd.Flags().StringVar(&contextDBRollbackOwner, "owner", "norn", "Rollback owner")
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

var contextDBPolicyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Show live ContextDB review worker policy status",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := fetchContextDBWorkerStatus(contextDBPolicyWorkerURL)
		if err != nil {
			return err
		}
		if contextDBPolicyJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(status)
		}
		printContextDBWorkerPolicy(status)
		return nil
	},
}

var contextDBAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "List recent ContextDB claim mutation audit events",
	RunE: func(cmd *cobra.Command, args []string) error {
		events, err := fetchContextDBFeedbackEvents(contextDBAuditNamespace, contextDBAuditMode, contextDBAuditAfter, contextDBAuditLimit, contextDBAuditWebURL)
		if err != nil {
			return err
		}
		printContextDBAuditEvents(contextDBAuditNamespace, events)
		return nil
	},
}

var contextDBEvaluatorSmokeCmd = &cobra.Command{
	Use:   "evaluator-smoke",
	Short: "Run the deployed ContextDB review worker evaluator smoke",
	RunE: func(cmd *cobra.Command, args []string) error {
		report, raw, err := runContextDBEvaluatorSmoke()
		if err != nil {
			return err
		}
		if contextDBSmokeJSON {
			fmt.Println(raw)
			return nil
		}
		printContextDBEvaluatorSmoke(report)
		return nil
	},
}

var contextDBRollbackCmd = &cobra.Command{
	Use:   "rollback-feedback <event-id>",
	Short: "Rollback a ContextDB feedback event through the Norn-hosted web service",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		receipt, err := client.ContextDBRollbackFeedback(args[0], contextDBRollbackNamespace, contextDBRollbackMode, contextDBRollbackReason, contextDBRollbackOwner)
		if err != nil {
			return err
		}
		fmt.Println(style.SuccessBox.Render("feedback rollback recorded"))
		fmt.Printf("  %s %s\n", style.Key.Render("rollback"), receipt.EventID)
		fmt.Printf("  %s %s\n", style.Key.Render("event"), receipt.RolledBackEventID)
		fmt.Printf("  %s %s\n", style.Key.Render("node"), receipt.NodeID)
		fmt.Printf("  %s %.2f -> %.2f\n", style.Key.Render("confidence"), receipt.PreviousConfidence, receipt.RestoredConfidence)
		return nil
	},
}

type contextDBWorkerStatus struct {
	Status string                `json:"status"`
	Worker string                `json:"worker"`
	DryRun bool                  `json:"dry_run"`
	Policy contextDBPolicyReport `json:"policy"`
}

type contextDBPolicyReport struct {
	GeneratedAt string                     `json:"generated_at"`
	DryRun      bool                       `json:"dry_run"`
	Namespaces  []contextDBNamespacePolicy `json:"namespaces"`
	Totals      contextDBPolicyTotals      `json:"totals"`
}

type contextDBNamespacePolicy struct {
	Namespace              string   `json:"namespace"`
	Mode                   string   `json:"mode"`
	PolicyPreset           string   `json:"policy_preset"`
	DryRun                 bool     `json:"dry_run"`
	Evaluator              string   `json:"evaluator"`
	Provider               string   `json:"provider"`
	ProviderKeyRequired    bool     `json:"provider_key_required"`
	ProviderKeyConfigured  bool     `json:"provider_key_configured"`
	AllowedActions         []string `json:"allowed_actions"`
	Types                  []string `json:"types"`
	LowConfidenceThreshold float64  `json:"low_confidence_threshold"`
	PruneConfidence        float64  `json:"prune_confidence"`
	Limit                  int      `json:"limit"`
	Owner                  string   `json:"owner"`
	MutationAllowed        bool     `json:"mutation_allowed"`
	Warnings               []string `json:"warnings"`
	OK                     bool     `json:"ok"`
	Error                  string   `json:"error"`
}

type contextDBPolicyTotals struct {
	Namespaces          int `json:"namespaces"`
	MutationEnabled     int `json:"mutation_enabled"`
	ProviderBacked      int `json:"provider_backed"`
	MissingProviderKeys int `json:"missing_provider_keys"`
	Warnings            int `json:"warnings"`
	Errors              int `json:"errors"`
}

type contextDBFeedbackEventsResponse struct {
	Events []contextDBFeedbackEvent `json:"events"`
}

type contextDBFeedbackEvent struct {
	EventID     string  `json:"event_id"`
	Namespace   string  `json:"namespace"`
	NodeID      string  `json:"node_id"`
	NodeVersion int64   `json:"node_version"`
	Action      string  `json:"action"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
	TxTime      string  `json:"tx_time"`
}

type contextDBEvaluatorSmokeReport struct {
	GeneratedAt string `json:"generated_at"`
	DryRun      bool   `json:"dry_run"`
	Namespaces  []struct {
		Namespace          string   `json:"namespace"`
		Mode               string   `json:"mode"`
		PolicyPreset       string   `json:"policy_preset"`
		DryRun             bool     `json:"dry_run"`
		Evaluator          string   `json:"evaluator"`
		AllowedActions     []string `json:"allowed_actions"`
		DisagreesWithRules bool     `json:"disagrees_with_rules"`
		OK                 bool     `json:"ok"`
		Error              string   `json:"error"`
	} `json:"namespaces"`
	Totals struct {
		Checked int `json:"checked"`
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
	} `json:"totals"`
}

func fetchContextDBWorkerStatus(workerURL string) (*contextDBWorkerStatus, error) {
	cfg := contextDBSmokeConfig{WorkerURL: workerURL}
	if err := discoverContextDBURLs(&cfg); err != nil {
		return nil, err
	}
	var status contextDBWorkerStatus
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if err := contextDBGetJSON(httpClient, cfg.WorkerURL+"/v1/status", &status); err != nil {
		return nil, fmt.Errorf("worker policy status: %w", err)
	}
	return &status, nil
}

func fetchContextDBFeedbackEvents(namespace, mode, after string, limit int, webURL string) ([]contextDBFeedbackEvent, error) {
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
	path := fmt.Sprintf("%s/v1/namespaces/%s/feedback/events", cfg.WebURL, url.PathEscape(namespace))
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out contextDBFeedbackEventsResponse
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if err := contextDBGetJSON(httpClient, path, &out); err != nil {
		return nil, fmt.Errorf("feedback events: %w", err)
	}
	if limit > 0 && len(out.Events) > limit {
		out.Events = out.Events[:limit]
	}
	return out.Events, nil
}

func runContextDBEvaluatorSmoke() (*contextDBEvaluatorSmokeReport, string, error) {
	conn, err := client.Exec("contextdb", "review-worker", []string{
		"/contextdb", "worker", "review",
		"--config", "/etc/contextdb/review-worker.hermes.json",
		"--smoke-evaluator",
		"--report",
	})
	if err != nil {
		return nil, "", fmt.Errorf("evaluator smoke exec: %w", err)
	}
	defer conn.Close()
	stdout, stderr, err := captureExec(conn)
	if err != nil {
		if strings.TrimSpace(stderr) != "" {
			return nil, "", fmt.Errorf("evaluator smoke: %w: %s", err, strings.TrimSpace(stderr))
		}
		return nil, "", fmt.Errorf("evaluator smoke: %w", err)
	}
	raw, err := extractJSONObject(stdout)
	if err != nil {
		return nil, "", fmt.Errorf("evaluator smoke report: %w", err)
	}
	var report contextDBEvaluatorSmokeReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, "", fmt.Errorf("evaluator smoke report: %w", err)
	}
	return &report, raw, nil
}

func printContextDBWorkerPolicy(status *contextDBWorkerStatus) {
	fmt.Println(style.Title.Render("contextdb worker policy"))
	fmt.Printf("status=%s worker=%s dry_run=%t\n\n", status.Status, status.Worker, status.DryRun)
	fmt.Printf("totals namespaces=%d mutation_enabled=%d provider_backed=%d missing_provider_keys=%d warnings=%d errors=%d\n\n",
		status.Policy.Totals.Namespaces,
		status.Policy.Totals.MutationEnabled,
		status.Policy.Totals.ProviderBacked,
		status.Policy.Totals.MissingProviderKeys,
		status.Policy.Totals.Warnings,
		status.Policy.Totals.Errors,
	)
	if len(status.Policy.Namespaces) == 0 {
		fmt.Println(style.DimText.Render("no namespace policies reported"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("NAMESPACE")+"\t"+
		style.TableHeader.Render("MODE")+"\t"+
		style.TableHeader.Render("PRESET")+"\t"+
		style.TableHeader.Render("EVALUATOR")+"\t"+
		style.TableHeader.Render("DRY")+"\t"+
		style.TableHeader.Render("MUTATE")+"\t"+
		style.TableHeader.Render("KEY")+"\t"+
		style.TableHeader.Render("ACTIONS")+"\t"+
		style.TableHeader.Render("WARN")+"\t"+
		style.TableHeader.Render("OK"))
	for _, ns := range status.Policy.Namespaces {
		keyState := "-"
		if ns.ProviderKeyRequired {
			keyState = "missing"
			if ns.ProviderKeyConfigured {
				keyState = "configured"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%t\t%s\t%s\t%d\t%t\n",
			ns.Namespace,
			ns.Mode,
			ns.PolicyPreset,
			ns.Evaluator,
			ns.DryRun,
			ns.MutationAllowed,
			keyState,
			strings.Join(ns.AllowedActions, ","),
			len(ns.Warnings),
			ns.OK,
		)
	}
	w.Flush()
	for _, ns := range status.Policy.Namespaces {
		if ns.Error == "" && len(ns.Warnings) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", style.Bold.Render(ns.Namespace))
		if ns.Error != "" {
			fmt.Printf("  %s %s\n", style.Unhealthy.Render("error"), ns.Error)
		}
		for _, warning := range ns.Warnings {
			fmt.Printf("  %s %s\n", style.Warning.Render("warning"), warning)
		}
	}
}

func printContextDBAuditEvents(namespace string, events []contextDBFeedbackEvent) {
	fmt.Println(style.Title.Render("contextdb audit for " + namespace))
	if len(events) == 0 {
		fmt.Println(style.DimText.Render("no feedback events found"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("TIME")+"\t"+
		style.TableHeader.Render("ACTION")+"\t"+
		style.TableHeader.Render("CONF")+"\t"+
		style.TableHeader.Render("NODE")+"\t"+
		style.TableHeader.Render("REASON"))
	for _, event := range events {
		txTime := event.TxTime
		if ts, err := time.Parse(time.RFC3339Nano, event.TxTime); err == nil {
			txTime = ts.Local().Format("2006-01-02 15:04:05")
		}
		nodeID := event.NodeID
		if len(nodeID) > 8 {
			nodeID = nodeID[:8]
		}
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%s\t%s\n", txTime, event.Action, event.Confidence, nodeID, event.Reason)
	}
	w.Flush()
}

func printContextDBEvaluatorSmoke(report *contextDBEvaluatorSmokeReport) {
	fmt.Println(style.Title.Render("contextdb evaluator smoke"))
	fmt.Printf("checked=%d passed=%d failed=%d dry_run=%t\n\n",
		report.Totals.Checked,
		report.Totals.Passed,
		report.Totals.Failed,
		report.DryRun,
	)
	if len(report.Namespaces) == 0 {
		fmt.Println(style.DimText.Render("no namespace evaluators checked"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("NAMESPACE")+"\t"+
		style.TableHeader.Render("MODE")+"\t"+
		style.TableHeader.Render("PRESET")+"\t"+
		style.TableHeader.Render("EVALUATOR")+"\t"+
		style.TableHeader.Render("DRY")+"\t"+
		style.TableHeader.Render("DIFF")+"\t"+
		style.TableHeader.Render("OK")+"\t"+
		style.TableHeader.Render("ERROR"))
	for _, ns := range report.Namespaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%t\t%t\t%s\n",
			ns.Namespace,
			ns.Mode,
			ns.PolicyPreset,
			ns.Evaluator,
			ns.DryRun,
			ns.DisagreesWithRules,
			ns.OK,
			ns.Error,
		)
	}
	w.Flush()
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
