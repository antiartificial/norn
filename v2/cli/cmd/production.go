package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	productionJSON             bool
	productionLimit            int
	productionDrillTarget      string
	productionDrillStatus      string
	productionDrillEvidence    []string
	productionAuditReason      string
	productionAuditExplanation string
)

func init() {
	rootCmd.AddCommand(productionCmd)
	productionCmd.AddCommand(productionCheckCmd)
	productionCmd.AddCommand(productionAuditCmd)
	productionCmd.AddCommand(productionAuditIncidentCmd)
	productionCmd.AddCommand(productionDrillsCmd)
	productionCmd.AddCommand(productionDrillCmd)
	productionDrillCmd.AddCommand(productionDrillStartCmd)
	productionDrillCmd.AddCommand(productionDrillCompleteCmd)
	productionCheckCmd.Flags().BoolVar(&productionJSON, "json", false, "Print the machine-readable readiness report")
	productionAuditCmd.Flags().IntVar(&productionLimit, "limit", 50, "Maximum mutation receipts to show")
	productionAuditIncidentCmd.Flags().StringVar(&productionAuditReason, "reason", "", "Controlled incident reason code")
	productionAuditIncidentCmd.Flags().StringVar(&productionAuditExplanation, "explanation", "", "Human-readable incident analysis")
	_ = productionAuditIncidentCmd.MarkFlagRequired("reason")
	_ = productionAuditIncidentCmd.MarkFlagRequired("explanation")
	productionDrillsCmd.Flags().IntVar(&productionLimit, "limit", 50, "Maximum recovery drills to show")
	productionDrillStartCmd.Flags().StringVar(&productionDrillTarget, "target", "", "Bounded target identifier for the drill")
	productionDrillCompleteCmd.Flags().StringVar(&productionDrillStatus, "status", "passed", "Drill result: passed or failed")
	productionDrillCompleteCmd.Flags().StringArrayVar(&productionDrillEvidence, "evidence", nil, "Bounded key=value evidence (repeatable)")
}

var productionCmd = &cobra.Command{
	Use:   "production",
	Short: "Inspect production admission and hardening posture",
}

var productionCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Fail unless every required production-readiness gate passes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := client.ProductionReadiness()
		if err != nil {
			return fmt.Errorf("production readiness failed: %w", err)
		}
		if productionJSON {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				return err
			}
		} else {
			printProductionReadiness(cmd.OutOrStdout(), report)
		}
		if report.Status != "ready" {
			return fmt.Errorf("production readiness blocked by %d failed check(s)", report.Summary.Failed)
		}
		return nil
	},
}

var productionAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "List durable principal-aware mutation receipts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		events, err := client.MutationAudits(productionLimit)
		if err != nil {
			return fmt.Errorf("mutation audit failed: %w", err)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tPRINCIPAL\tMETHOD\tPATH\tSTATUS\tOUTCOME\tINTEGRITY")
		for _, event := range events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", event.StartedAt, event.PrincipalSubject, event.Method, event.Path, event.Status, event.Outcome, event.Integrity)
		}
		return w.Flush()
	},
}

var productionAuditIncidentCmd = &cobra.Command{
	Use:   "audit-incident <audit-event-id>",
	Short: "Acknowledge an investigated invalid mutation receipt without deleting it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		incident, err := client.AcknowledgeMutationAuditIncident(args[0], productionAuditReason, productionAuditExplanation)
		if err != nil {
			return fmt.Errorf("acknowledge mutation audit incident: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "acknowledged audit incident %s for %s (%s)\n", incident.ID, incident.AuditEventID, incident.Integrity)
		return nil
	},
}

var productionDrillsCmd = &cobra.Command{
	Use:   "drills",
	Short: "List durable recovery-drill receipts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		drills, err := client.RecoveryDrills(productionLimit)
		if err != nil {
			return fmt.Errorf("recovery drills failed: %w", err)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "STARTED\tKIND\tTARGET\tSTATUS\tOPERATOR\tID")
		for _, drill := range drills {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", drill.StartedAt, drill.Kind, drill.Target, drill.Status, drill.InitiatedBy, drill.ID)
		}
		return w.Flush()
	},
}

var productionDrillCmd = &cobra.Command{
	Use:   "drill",
	Short: "Start or complete a recovery drill receipt",
}

var productionDrillStartCmd = &cobra.Command{
	Use:   "start <kind>",
	Short: "Start a database.restore, artifact.rollback, or node.failover drill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		drill, err := client.StartRecoveryDrill(args[0], productionDrillTarget)
		if err != nil {
			return fmt.Errorf("start recovery drill: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "started %s drill %s\n", drill.Kind, drill.ID)
		return nil
	},
}

var productionDrillCompleteCmd = &cobra.Command{
	Use:   "complete <id>",
	Short: "Complete a recovery drill with bounded evidence",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		evidence, err := parseProductionEvidence(productionDrillEvidence)
		if err != nil {
			return err
		}
		drill, err := client.CompleteRecoveryDrill(args[0], productionDrillStatus, evidence)
		if err != nil {
			return fmt.Errorf("complete recovery drill: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s drill %s\n", drill.Status, drill.Kind, drill.ID)
		return nil
	},
}

func parseProductionEvidence(values []string) (map[string]string, error) {
	evidence := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("evidence must use key=value")
		}
		evidence[key] = strings.TrimSpace(val)
	}
	if len(evidence) > 20 {
		return nil, fmt.Errorf("evidence must contain at most 20 entries")
	}
	return evidence, nil
}

func printProductionReadiness(out io.Writer, report *api.ProductionReadinessReport) {
	fmt.Fprintln(out, style.Title.Render("norn production readiness"))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "status=%s passed=%d warnings=%d failed=%d\n\n", report.Status, report.Summary.Passed, report.Summary.Warnings, report.Summary.Failed)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tCATEGORY\tCHECK\tDETAIL")
	for _, check := range report.Checks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", check.Status, check.Category, check.ID, check.Detail)
	}
	_ = w.Flush()

	if report.Summary.Failed == 0 && report.Summary.Warnings == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, style.Subtitle.Render("  remediation"))
	for _, check := range report.Checks {
		if check.Status == "pass" || check.Remediation == "" {
			continue
		}
		fmt.Fprintf(out, "  %s: %s\n", check.ID, check.Remediation)
	}
}
