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

func init() {
	rootCmd.AddCommand(operatorCmd)
	operatorCmd.AddCommand(operatorInboxCmd)
	operatorCmd.AddCommand(operatorCronCmd)
	operatorCmd.AddCommand(operatorWakeCmd)
	operatorCmd.AddCommand(operatorDeployCmd)
	operatorCmd.AddCommand(operatorSnapshotsCmd)
	operatorCmd.AddCommand(operatorAuthCmd)
	operatorCmd.AddCommand(operatorActionsCmd)
}

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "Operator-confidence release surfaces",
}

var operatorInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Show incidents, cron, deploy, snapshot, secret, and operation work",
	RunE: func(cmd *cobra.Command, args []string) error {
		inbox, err := client.OperatorInbox()
		if err != nil {
			return err
		}
		printOperatorInbox(inbox)
		return nil
	},
}

var operatorCronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Show cron and function operator overview",
	RunE: func(cmd *cobra.Command, args []string) error {
		overview, err := client.OperatorCronOverview()
		if err != nil {
			return err
		}
		printOperatorCron(overview)
		return nil
	},
}

var operatorWakeCmd = &cobra.Command{
	Use:   "wake-targets",
	Short: "Show wake-on-request endpoints and readiness",
	RunE: func(cmd *cobra.Command, args []string) error {
		targets, err := client.OperatorWakeTargets()
		if err != nil {
			return err
		}
		printOperatorWakeTargets(targets)
		return nil
	},
}

var operatorDeployCmd = &cobra.Command{
	Use:   "deploy-confidence",
	Short: "Show deploy confidence and recommended preflight/deploy actions",
	RunE: func(cmd *cobra.Command, args []string) error {
		confidence, err := client.OperatorDeployConfidence()
		if err != nil {
			return err
		}
		printOperatorDeployConfidence(confidence)
		return nil
	},
}

var operatorSnapshotsCmd = &cobra.Command{
	Use:   "snapshot-readiness",
	Short: "Show snapshot and restore readiness",
	RunE: func(cmd *cobra.Command, args []string) error {
		readiness, err := client.OperatorSnapshotReadiness()
		if err != nil {
			return err
		}
		printOperatorSnapshotReadiness(readiness)
		return nil
	},
}

var operatorAuthCmd = &cobra.Command{
	Use:   "auth-hints",
	Short: "Show secret-safe operator authentication patterns",
	RunE: func(cmd *cobra.Command, args []string) error {
		hints, err := client.OperatorAuthHints()
		if err != nil {
			return err
		}
		printOperatorAuthHints(hints)
		return nil
	},
}

var operatorActionsCmd = &cobra.Command{
	Use:   "actions",
	Short: "Show mobile-ready operator actions",
	RunE: func(cmd *cobra.Command, args []string) error {
		actions, err := client.OperatorActions()
		if err != nil {
			return err
		}
		printOperatorActions(actions.Actions)
		return nil
	},
}

func printOperatorInbox(inbox *api.OperatorInbox) {
	fmt.Println(style.Title.Render("operator inbox"))
	fmt.Printf("generated=%s recommended=%d incidents=%d ops=%d deploy=%d cron=%d snapshots=%d secrets=%d wakeTargets=%d\n\n",
		localTime(inbox.GeneratedAt),
		inbox.Summary.RecommendedActions,
		inbox.Summary.OpenIncidents,
		inbox.Summary.ActiveOperations,
		inbox.Summary.DeployRisks,
		inbox.Summary.CronRisks,
		inbox.Summary.SnapshotRisks,
		inbox.Summary.SecretRisks,
		inbox.Summary.WakeTargets,
	)
	if len(inbox.Items) == 0 {
		fmt.Println(style.Healthy.Render("  no recommended actions"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("SEV")+"\t"+
		style.TableHeader.Render("KIND")+"\t"+
		style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("ACTION")+"\t"+
		style.TableHeader.Render("TITLE"))
	for _, item := range inbox.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			renderSeverity(item.Severity),
			item.Kind,
			emptyDash(item.App),
			emptyDash(item.Status),
			emptyDash(item.Action),
			item.Title,
		)
	}
	w.Flush()
}

func printOperatorCron(overview *api.OperatorCronOverview) {
	fmt.Println(style.Title.Render("operator cron"))
	if len(overview.Entries) == 0 {
		fmt.Println(style.DimText.Render("  no cron processes"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("RISK")+"\t"+
		style.TableHeader.Render("SCHEDULE")+"\t"+
		style.TableHeader.Render("TZ")+"\t"+
		style.TableHeader.Render("LAST")+"\t"+
		style.TableHeader.Render("NEXT")+"\t"+
		style.TableHeader.Render("CHILDREN"))
	for _, entry := range overview.Entries {
		children := fmt.Sprintf("p=%d r=%d d=%d", entry.ChildrenPending, entry.ChildrenRunning, entry.ChildrenDead)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.App,
			entry.Process,
			emptyDash(entry.Risk),
			entry.Schedule,
			entry.Timezone,
			emptyDash(entry.LastRunAtLocal),
			emptyDash(entry.NextRunAtLocal),
			children,
		)
	}
	w.Flush()
}

func printOperatorWakeTargets(targets *api.OperatorWakeTargets) {
	fmt.Println(style.Title.Render("wake targets"))
	if len(targets.Targets) == 0 {
		fmt.Println(style.DimText.Render("  no endpoints"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("PROCESS")+"\t"+
		style.TableHeader.Render("READY")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("EXPOSURE")+"\t"+
		style.TableHeader.Render("ENDPOINT"))
	for _, target := range targets.Targets {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
			target.App,
			target.Process,
			target.Ready,
			target.Status,
			target.Exposure,
			target.Endpoint,
		)
	}
	w.Flush()
}

func printOperatorDeployConfidence(confidence *api.OperatorDeployConfidence) {
	fmt.Println(style.Title.Render("deploy confidence"))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("CONFIDENCE")+"\t"+
		style.TableHeader.Render("LAST")+"\t"+
		style.TableHeader.Render("AUTO-ROLLBACK")+"\t"+
		style.TableHeader.Render("CANARY")+"\t"+
		style.TableHeader.Render("EVIDENCE"))
	for _, app := range confidence.Apps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\n",
			app.App,
			app.Confidence,
			emptyDash(app.LastStatus),
			app.AutoRollback,
			emptyDash(strings.Join(app.CanaryProcesses, ",")),
			emptyDash(strings.Join(app.Evidence, "; ")),
		)
	}
	w.Flush()
}

func printOperatorSnapshotReadiness(readiness *api.OperatorSnapshotReadiness) {
	fmt.Println(style.Title.Render("snapshot readiness"))
	if len(readiness.Apps) == 0 {
		fmt.Println(style.DimText.Render("  no app-owned postgres snapshots"))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("APP")+"\t"+
		style.TableHeader.Render("STATUS")+"\t"+
		style.TableHeader.Render("DB")+"\t"+
		style.TableHeader.Render("COUNT")+"\t"+
		style.TableHeader.Render("KEEP")+"\t"+
		style.TableHeader.Render("OVER")+"\t"+
		style.TableHeader.Render("LATEST")+"\t"+
		style.TableHeader.Render("REMOTE"))
	for _, app := range readiness.Apps {
		latest := "-"
		if app.Latest != nil {
			latest = app.Latest.Timestamp
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%t\n",
			app.App,
			app.Status,
			emptyDash(app.Database),
			app.Count,
			app.Keep,
			app.OverLimit,
			latest,
			app.RemoteExport,
		)
	}
	w.Flush()
}

func printOperatorAuthHints(hints *api.OperatorAuthHints) {
	fmt.Println(style.Title.Render("auth hints"))
	for _, principle := range hints.Principles {
		fmt.Printf("  %s\n", principle)
	}
	fmt.Println()
	for _, hint := range hints.Patterns {
		fmt.Println(style.Subtitle.Render(hint.Name))
		fmt.Printf("  %s %s\n", style.Key.Render("use"), hint.UseWhen)
		fmt.Printf("  %s %t\n", style.Key.Render("secret-safe"), hint.SecretSafe)
		fmt.Printf("  %s %s\n", style.Key.Render("command"), hint.Command)
		if len(hint.Evidence) > 0 {
			fmt.Printf("  %s %s\n", style.Key.Render("evidence"), strings.Join(hint.Evidence, "; "))
		}
		fmt.Println()
	}
}

func printOperatorActions(actions []api.OperatorActionDescriptor) {
	fmt.Println(style.Title.Render("operator actions"))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, style.TableHeader.Render("ID")+"\t"+
		style.TableHeader.Render("RISK")+"\t"+
		style.TableHeader.Render("MOBILE")+"\t"+
		style.TableHeader.Render("METHOD")+"\t"+
		style.TableHeader.Render("PATH")+"\t"+
		style.TableHeader.Render("LABEL"))
	for _, action := range actions {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
			action.ID,
			action.Risk,
			action.MobileReady,
			action.Method,
			action.Path,
			action.Label,
		)
	}
	w.Flush()
}
