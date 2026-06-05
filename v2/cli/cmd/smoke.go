package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	smokeContextDBNamespace              string
	smokeContextDBMode                   string
	smokeContextDBWebURL                 string
	smokeContextDBWorkerURL              string
	smokeContextDBLowConfidenceThreshold string
)

func init() {
	rootCmd.AddCommand(smokeCmd)
	smokeCmd.AddCommand(smokeContextDBCmd)
	smokeContextDBCmd.Flags().StringVar(&smokeContextDBNamespace, "namespace", "", "ContextDB namespace for the smoke claim")
	smokeContextDBCmd.Flags().StringVar(&smokeContextDBMode, "mode", "agent_memory", "ContextDB mode for write/retrieve/review checks")
	smokeContextDBCmd.Flags().StringVar(&smokeContextDBWebURL, "web-url", "", "Override ContextDB web URL")
	smokeContextDBCmd.Flags().StringVar(&smokeContextDBWorkerURL, "worker-url", "", "Override ContextDB review worker health URL")
	smokeContextDBCmd.Flags().StringVar(&smokeContextDBLowConfidenceThreshold, "low-confidence-threshold", "0.35", "Review queue low-confidence threshold")
}

var smokeCmd = &cobra.Command{
	Use:   "smoke",
	Short: "Run app-specific operational smoke checks",
}

var smokeContextDBCmd = &cobra.Command{
	Use:   "contextdb",
	Short: "Exercise ContextDB web, review queue, and worker dry-run paths",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := contextDBSmokeConfig{
			Namespace:              smokeContextDBNamespace,
			Mode:                   smokeContextDBMode,
			WebURL:                 smokeContextDBWebURL,
			WorkerURL:              smokeContextDBWorkerURL,
			LowConfidenceThreshold: smokeContextDBLowConfidenceThreshold,
		}
		if cfg.Namespace == "" {
			cfg.Namespace = "norn-smoke-" + time.Now().UTC().Format("20060102150405")
		}
		return runContextDBSmoke(cfg)
	},
}

type contextDBSmokeConfig struct {
	Namespace              string
	Mode                   string
	WebURL                 string
	WorkerURL              string
	LowConfidenceThreshold string
}

type contextDBWriteResponse struct {
	Admitted bool   `json:"admitted"`
	NodeID   string `json:"node_id"`
}

type contextDBRetrieveResponse struct {
	Results []struct {
		ID string `json:"id"`
	} `json:"results"`
}

type contextDBReviewQueueResponse struct {
	Items []struct {
		NodeID string `json:"node_id"`
		Type   string `json:"type"`
	} `json:"items"`
}

type contextDBWorkerReport struct {
	DryRun     bool `json:"dry_run"`
	Namespaces []struct {
		Decisions []struct {
			NodeID  string `json:"node_id"`
			Applied bool   `json:"applied"`
		} `json:"decisions"`
	} `json:"namespaces"`
	Totals struct {
		Scanned int `json:"scanned"`
	} `json:"totals"`
}

type contextDBWorkerRunsResponse struct {
	Runs []contextDBWorkerRun `json:"runs"`
}

type contextDBWorkerRun struct {
	EventID     string                    `json:"event_id"`
	CycleID     string                    `json:"cycle_id"`
	Namespace   string                    `json:"namespace"`
	Mode        string                    `json:"mode"`
	GeneratedAt string                    `json:"generated_at"`
	DryRun      bool                      `json:"dry_run"`
	Evaluator   string                    `json:"evaluator"`
	Scanned     int                       `json:"scanned"`
	Applied     int                       `json:"applied"`
	Skipped     int                       `json:"skipped"`
	Errors      int                       `json:"errors"`
	Decisions   []contextDBWorkerDecision `json:"decisions"`
	TxTime      string                    `json:"tx_time"`
}

type contextDBWorkerDecision struct {
	ReviewID string `json:"review_id"`
	Type     string `json:"type"`
	NodeID   string `json:"node_id"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Applied  bool   `json:"applied"`
}

func runContextDBSmoke(cfg contextDBSmokeConfig) error {
	fmt.Println(style.Title.Render("contextdb smoke"))

	if err := validateContextDBSpec(); err != nil {
		return err
	}
	printSmokeStep("norn validate contextdb")

	if err := discoverContextDBURLs(&cfg); err != nil {
		return err
	}
	printSmokeStep("service manifest")

	fmt.Printf("web=%s\n", cfg.WebURL)
	fmt.Printf("worker=%s\n", cfg.WorkerURL)
	fmt.Printf("namespace=%s\n", cfg.Namespace)
	fmt.Println()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	beforeWorker := time.Now().UTC().Format(time.RFC3339)

	if err := contextDBGetJSON(httpClient, cfg.WebURL+"/v1/ping", nil); err != nil {
		return fmt.Errorf("web ping: %w", err)
	}
	printSmokeStep("web ping")

	var workerPing struct {
		Status string `json:"status"`
		Worker string `json:"worker"`
	}
	if err := contextDBGetJSON(httpClient, cfg.WorkerURL+"/v1/ping", &workerPing); err != nil {
		return fmt.Errorf("worker ping: %w", err)
	}
	if workerPing.Status != "ok" || workerPing.Worker != "review" {
		return fmt.Errorf("worker ping returned status=%q worker=%q", workerPing.Status, workerPing.Worker)
	}
	printSmokeStep("worker ping")

	nodeID, err := contextDBSmokeWrite(httpClient, cfg)
	if err != nil {
		return err
	}
	printSmokeStep("write claim")

	if err := contextDBSmokeRetrieve(httpClient, cfg, nodeID); err != nil {
		return err
	}
	printSmokeStep("retrieve claim")

	if err := contextDBSmokeReviewQueue(httpClient, cfg, nodeID); err != nil {
		return err
	}
	printSmokeStep("review queue")

	if err := contextDBSmokeWorkerDryRun(cfg, nodeID); err != nil {
		return err
	}
	printSmokeStep("worker dry run")

	if err := contextDBSmokeWorkerRuns(httpClient, cfg, beforeWorker, nodeID); err != nil {
		return err
	}
	printSmokeStep("worker run receipt")

	fmt.Printf("\nok namespace=%s node=%s\n", cfg.Namespace, nodeID)
	return nil
}

func validateContextDBSpec() error {
	result, err := client.ValidateApp("contextdb")
	if err != nil {
		return fmt.Errorf("validate contextdb: %w", err)
	}
	if !result.Valid {
		return fmt.Errorf("contextdb infraspec is invalid")
	}
	for _, finding := range result.Findings {
		if finding.Severity == "warning" && strings.Contains(finding.Field, ".env.") {
			return fmt.Errorf("contextdb has plaintext env warning: %s", finding.Field)
		}
	}
	return nil
}

func discoverContextDBURLs(cfg *contextDBSmokeConfig) error {
	manifest, err := client.ServiceManifest()
	if err != nil {
		return fmt.Errorf("service manifest: %w", err)
	}
	var web, worker *api.ServiceManifestEntry
	for i := range manifest.Services {
		svc := &manifest.Services[i]
		if svc.App != "contextdb" {
			continue
		}
		switch svc.Process {
		case "web":
			web = svc
		case "review-worker":
			worker = svc
		}
	}
	if web == nil {
		return fmt.Errorf("contextdb web service not found in manifest")
	}
	if worker == nil {
		return fmt.Errorf("contextdb review-worker not found in manifest")
	}
	if web.Status != "passing" {
		return fmt.Errorf("contextdb web status is %q", web.Status)
	}
	if worker.Status != "passing" {
		return fmt.Errorf("contextdb review-worker status is %q", worker.Status)
	}
	if worker.Type != "worker" {
		return fmt.Errorf("contextdb review-worker manifest type is %q", worker.Type)
	}
	if len(worker.Endpoints) > 0 {
		return fmt.Errorf("contextdb review-worker should not expose app endpoints")
	}
	if cfg.WebURL == "" {
		if len(web.Endpoints) == 0 || web.Endpoints[0].URL == "" {
			return fmt.Errorf("contextdb web has no manifest endpoint; pass --web-url")
		}
		cfg.WebURL = strings.TrimRight(web.Endpoints[0].URL, "/")
	}
	if cfg.WorkerURL == "" {
		if len(worker.Instances) == 0 || worker.Instances[0].Address == "" || worker.Instances[0].Port == 0 {
			return fmt.Errorf("contextdb review-worker has no manifest instance; pass --worker-url")
		}
		cfg.WorkerURL = fmt.Sprintf("http://%s:%d", worker.Instances[0].Address, worker.Instances[0].Port)
	}
	cfg.WebURL = strings.TrimRight(cfg.WebURL, "/")
	cfg.WorkerURL = strings.TrimRight(cfg.WorkerURL, "/")
	return nil
}

func contextDBSmokeWrite(httpClient *http.Client, cfg contextDBSmokeConfig) (string, error) {
	body := map[string]any{
		"mode":       cfg.Mode,
		"content":    "ContextDB Norn smoke claim for review worker validation",
		"source_id":  "contextdb:norn-smoke",
		"labels":     []string{"Claim", "Smoke"},
		"vector":     []float64{0.51, 0.21, 0.13, 0.08, 0.05, 0.03, 0.02, 0.01},
		"confidence": 0.22,
	}
	var out contextDBWriteResponse
	if err := contextDBPostJSON(httpClient, cfg.WebURL+"/v1/namespaces/"+url.PathEscape(cfg.Namespace)+"/write", body, &out); err != nil {
		return "", fmt.Errorf("write claim: %w", err)
	}
	if !out.Admitted || out.NodeID == "" {
		return "", fmt.Errorf("write claim returned admitted=%t node_id=%q", out.Admitted, out.NodeID)
	}
	return out.NodeID, nil
}

func contextDBSmokeRetrieve(httpClient *http.Client, cfg contextDBSmokeConfig, nodeID string) error {
	body := map[string]any{
		"vector": []float64{0.51, 0.21, 0.13, 0.08, 0.05, 0.03, 0.02, 0.01},
		"top_k":  3,
	}
	var out contextDBRetrieveResponse
	if err := contextDBPostJSON(httpClient, cfg.WebURL+"/v1/namespaces/"+url.PathEscape(cfg.Namespace)+"/retrieve", body, &out); err != nil {
		return fmt.Errorf("retrieve claim: %w", err)
	}
	for _, result := range out.Results {
		if result.ID == nodeID {
			return nil
		}
	}
	return fmt.Errorf("retrieve results did not include node %s", nodeID)
}

func contextDBSmokeReviewQueue(httpClient *http.Client, cfg contextDBSmokeConfig, nodeID string) error {
	path := fmt.Sprintf("%s/v1/namespaces/%s/review/queue?mode=%s&low_confidence_threshold=%s&limit=5",
		cfg.WebURL, url.PathEscape(cfg.Namespace), url.QueryEscape(cfg.Mode), url.QueryEscape(cfg.LowConfidenceThreshold))
	var out contextDBReviewQueueResponse
	if err := contextDBGetJSON(httpClient, path, &out); err != nil {
		return fmt.Errorf("review queue: %w", err)
	}
	for _, item := range out.Items {
		if item.NodeID == nodeID && item.Type == "low_confidence" {
			return nil
		}
	}
	return fmt.Errorf("review queue did not include low_confidence item for node %s", nodeID)
}

func contextDBSmokeWorkerDryRun(cfg contextDBSmokeConfig, nodeID string) error {
	conn, err := client.Exec("contextdb", "review-worker", []string{
		"/contextdb", "worker", "review",
		"--db-mode", "standard",
		"--namespaces", cfg.Namespace,
		"--mode", cfg.Mode,
		"--once",
		"--dry-run",
		"--report",
	})
	if err != nil {
		return fmt.Errorf("worker dry run exec: %w", err)
	}
	defer conn.Close()

	stdout, stderr, err := captureExec(conn)
	if err != nil {
		if stderr != "" {
			return fmt.Errorf("worker dry run: %w: %s", err, strings.TrimSpace(stderr))
		}
		return fmt.Errorf("worker dry run: %w", err)
	}
	reportJSON, err := extractJSONObject(stdout)
	if err != nil {
		return fmt.Errorf("worker dry run report: %w", err)
	}
	var report contextDBWorkerReport
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
		return fmt.Errorf("worker dry run report: %w", err)
	}
	if !report.DryRun {
		return fmt.Errorf("worker dry run report was not dry_run")
	}
	if report.Totals.Scanned < 1 {
		return fmt.Errorf("worker dry run scanned %d items", report.Totals.Scanned)
	}
	for _, ns := range report.Namespaces {
		for _, decision := range ns.Decisions {
			if decision.NodeID == nodeID && !decision.Applied {
				return nil
			}
		}
	}
	return fmt.Errorf("worker dry run did not include unapplied decision for node %s", nodeID)
}

func contextDBSmokeWorkerRuns(httpClient *http.Client, cfg contextDBSmokeConfig, after, nodeID string) error {
	path := fmt.Sprintf("%s/v1/namespaces/%s/review/worker-runs?mode=%s&after=%s",
		cfg.WebURL, url.PathEscape(cfg.Namespace), url.QueryEscape(cfg.Mode), url.QueryEscape(after))
	var out contextDBWorkerRunsResponse
	if err := contextDBGetJSON(httpClient, path, &out); err != nil {
		return fmt.Errorf("worker runs: %w", err)
	}
	for _, run := range out.Runs {
		if !run.DryRun || run.Evaluator != "rules" || run.Scanned < 1 {
			continue
		}
		for _, decision := range run.Decisions {
			if decision.NodeID == nodeID && !decision.Applied {
				return nil
			}
		}
	}
	return fmt.Errorf("worker run receipts did not include node %s", nodeID)
}

func contextDBGetJSON(httpClient *http.Client, url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeContextDBResponse(resp, out)
}

func contextDBPostJSON(httpClient *http.Client, url string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeContextDBResponse(resp, out)
}

func decodeContextDBResponse(resp *http.Response, out any) error {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	return nil
}

func captureExec(conn *websocket.Conn) (string, string, error) {
	var stdout strings.Builder
	var stderr strings.Builder
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) || err == io.EOF {
				return stdout.String(), stderr.String(), nil
			}
			return stdout.String(), stderr.String(), err
		}
		var msg struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
			Exit   *int   `json:"exit"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		stdout.WriteString(msg.Stdout)
		stderr.WriteString(msg.Stderr)
		if msg.Exit != nil {
			if *msg.Exit != 0 {
				return stdout.String(), stderr.String(), fmt.Errorf("remote command exited with %d", *msg.Exit)
			}
			return stdout.String(), stderr.String(), nil
		}
	}
}

func extractJSONObject(raw string) (string, error) {
	start := strings.Index(raw, "{")
	if start == -1 {
		return "", fmt.Errorf("no JSON object found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated JSON object")
}

func printSmokeStep(name string) {
	fmt.Printf("%s %s\n", style.Healthy.Render("ok"), name)
}
