package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	opsContextDBNamespace string
	opsContextDBMode      string
	opsContextDBLimit     int
)

func init() {
	rootCmd.AddCommand(opsCmd)
	opsCmd.AddCommand(opsPlatformCmd)
	opsCmd.AddCommand(opsContextDBCmd)
	opsContextDBCmd.Flags().StringVar(&opsContextDBNamespace, "namespace", "hermes-agent", "ContextDB namespace")
	opsContextDBCmd.Flags().StringVar(&opsContextDBMode, "mode", "agent_memory", "ContextDB mode")
	opsContextDBCmd.Flags().IntVar(&opsContextDBLimit, "limit", 10, "Recent rows to include")
}

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "Operator rollups across hosted services",
}

var opsPlatformCmd = &cobra.Command{
	Use:   "platform",
	Short: "Show service, provenance, secrets, snapshot, access, and observability status",
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := client.PlatformOps()
		if err != nil {
			return err
		}
		printOpsPlatform(summary)
		return nil
	},
}

var opsContextDBCmd = &cobra.Command{
	Use:   "contextdb",
	Short: "Show ContextDB worker, evaluator, audit, snapshot, and deployment status",
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := client.ContextDBOps(opsContextDBNamespace, opsContextDBMode, opsContextDBLimit)
		if err != nil {
			return err
		}
		printOpsContextDB(summary)
		return nil
	},
}

func printOpsPlatform(summary *api.PlatformOpsSummary) {
	fmt.Println(style.Title.Render("norn platform operations"))
	fmt.Printf("generated=%s network=%s\n\n", summary.GeneratedAt, emptyDash(summary.NetworkMode))
	fmt.Printf("services: total=%d public=%d private=%d local=%d internal=%d\n",
		summary.Services.Total,
		summary.Services.Public,
		summary.Services.Private,
		summary.Services.Local,
		summary.Services.Internal,
	)
	fmt.Printf("deploys:  recent=%d success=%d failed=%d dirty=%d\n",
		len(summary.Deployments.Recent),
		summary.Deployments.Successful,
		summary.Deployments.Failed,
		len(summary.Deployments.Dirty),
	)
	fmt.Printf("ops:      recent=%d active=%d status=%s\n",
		len(summary.Operations.Recent),
		len(summary.Operations.Active),
		mapCounts(summary.Operations.ByStatus),
	)
	fmt.Printf("secrets:  ok=%d needs_attention=%d migration_items=%d\n", summary.Secrets.OK, summary.Secrets.NeedsAttention, summary.Secrets.MigrationItems)
	fmt.Printf("access:   recent=%d status=%s\n", summary.Access.TotalRecent, mapCounts(summary.Access.ByStatus))
	fmt.Printf("otel:     enabled=%t logs=%t format=%s endpoint=%s bundle=%t retention=%s\n\n",
		summary.Observability.Enabled,
		summary.Observability.LogsEnabled,
		summary.Observability.LogFormat,
		emptyDash(summary.Observability.OTLPEndpoint),
		summary.Observability.BundleAvailable,
		emptyDash(summary.Observability.Retention),
	)

	if len(summary.Snapshots) > 0 {
		fmt.Println(style.Subtitle.Render("  snapshots"))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+style.TableHeader.Render("APP")+"\t"+
			style.TableHeader.Render("DB")+"\t"+
			style.TableHeader.Render("COUNT")+"\t"+
			style.TableHeader.Render("KEEP")+"\t"+
			style.TableHeader.Render("OVER")+"\t"+
			style.TableHeader.Render("LATEST"))
		for _, snapshot := range summary.Snapshots {
			latest := "-"
			if snapshot.Latest != nil {
				latest = snapshot.Latest.Timestamp
			}
			fmt.Fprintf(w, "  %s\t%s\t%d\t%d\t%d\t%s\n",
				snapshot.App,
				snapshot.Database,
				snapshot.Count,
				snapshot.Keep,
				snapshot.OverLimit,
				latest,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if len(summary.Deployments.Dirty) > 0 {
		fmt.Println(style.Subtitle.Render("  dirty deployments"))
		for _, deployment := range summary.Deployments.Dirty {
			fmt.Printf("  %s %s %s changes=%d\n",
				deployment.App,
				shortValue(deployment.CommitSHA, 12),
				deployment.ImageTag,
				len(deployment.SourceChanges),
			)
		}
		fmt.Println()
	}

	for _, warning := range summary.Warnings {
		fmt.Printf("%s %s\n", style.Warning.Render("warning"), warning)
	}
}

func printOpsContextDB(summary *api.ContextDBOpsSummary) {
	fmt.Println(style.Title.Render("contextdb operations"))
	fmt.Printf("generated=%s web=%s worker=%s\n\n", summary.GeneratedAt, emptyDash(summary.WebURL), emptyDash(summary.WorkerURL))

	health := "unknown"
	if summary.App != nil {
		health = fmt.Sprintf("nomad=%s healthy=%t allocs=%d", summary.App.NomadStatus, summary.App.Healthy, len(summary.App.Allocations))
	}
	secretState := "unknown"
	if summary.Secrets != nil {
		secretState = fmt.Sprintf("ok=%t declared=%d encrypted=%d", summary.Secrets.OK, len(summary.Secrets.Declared), len(summary.Secrets.Encrypted))
	}
	workerState := "missing"
	if summary.Worker != nil {
		workerState = fmt.Sprintf("%s dry_run=%t", summary.Worker.Status, summary.Worker.DryRun)
	}
	fmt.Printf("app:      %s\n", health)
	fmt.Printf("worker:   %s\n", workerState)
	fmt.Printf("secrets:  %s\n", secretState)
	fmt.Printf("snapshots:%d\n", len(summary.Snapshots))
	fmt.Printf("rollbacks:%d\n", len(summary.Rollbacks))
	fmt.Printf("queue:    %d", summary.Queue.Total)
	if summary.Queue.Error != "" {
		fmt.Printf(" error=%s", summary.Queue.Error)
	}
	fmt.Println()
	fmt.Printf("provider: ready=%t", summary.ProviderGate.Ready)
	if summary.ProviderGate.Reason != "" {
		fmt.Printf(" reason=%s", summary.ProviderGate.Reason)
	}
	fmt.Printf(" provider_backed=%d mutation_enabled=%d missing_keys=%d warnings=%d errors=%d\n\n",
		summary.ProviderGate.ProviderBacked,
		summary.ProviderGate.MutationEnabled,
		summary.ProviderGate.MissingProviderKeys,
		summary.ProviderGate.Warnings,
		summary.ProviderGate.Errors,
	)

	if len(summary.WorkerRuns) > 0 {
		fmt.Println(style.Subtitle.Render("  recent worker runs"))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+style.TableHeader.Render("TIME")+"\t"+
			style.TableHeader.Render("EVAL")+"\t"+
			style.TableHeader.Render("DRY")+"\t"+
			style.TableHeader.Render("SCANNED")+"\t"+
			style.TableHeader.Render("APPLIED")+"\t"+
			style.TableHeader.Render("ERRORS"))
		for _, run := range summary.WorkerRuns {
			fmt.Fprintf(w, "  %s\t%s\t%t\t%d\t%d\t%d\n",
				localTime(run.GeneratedAt),
				run.Evaluator,
				run.DryRun,
				run.Scanned,
				run.Applied,
				run.Errors,
			)
		}
		w.Flush()
		fmt.Println()
	}

	if len(summary.FeedbackEvents) > 0 {
		fmt.Println(style.Subtitle.Render("  recent feedback audit"))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  "+style.TableHeader.Render("TIME")+"\t"+
			style.TableHeader.Render("ACTION")+"\t"+
			style.TableHeader.Render("CONF")+"\t"+
			style.TableHeader.Render("NODE")+"\t"+
			style.TableHeader.Render("REASON"))
		for _, event := range summary.FeedbackEvents {
			nodeID := event.NodeID
			if len(nodeID) > 8 {
				nodeID = nodeID[:8]
			}
			fmt.Fprintf(w, "  %s\t%s\t%.2f\t%s\t%s\n", localTime(event.TxTime), event.Action, event.Confidence, nodeID, event.Reason)
		}
		w.Flush()
	}

	for _, warning := range summary.Warnings {
		fmt.Printf("%s %s\n", style.Warning.Render("warning"), warning)
	}
}

func localTime(value string) string {
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.Local().Format("2006-01-02 15:04:05")
	}
	return value
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func mapCounts(values map[string]int) string {
	if len(values) == 0 {
		return "-"
	}
	out := ""
	for key, value := range values {
		if out != "" {
			out += ","
		}
		out += fmt.Sprintf("%s=%d", key, value)
	}
	return out
}
