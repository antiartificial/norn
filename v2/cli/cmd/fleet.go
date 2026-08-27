package cmd

import (
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	fleetDesired          int
	fleetSize             string
	fleetStrategy         string
	fleetReason           string
	fleetAllowDestructive bool
	fleetRunnerID         string
	fleetCommitSHA        string
	fleetPlanSHA256       string
	fleetWorkflowURL      string
	fleetPhase            string
	fleetMessage          string
	fleetAttemptReason    string
	fleetHeartbeatTimeout int
	fleetSequence         int64
	fleetRevision         int64
)

func init() {
	rootCmd.AddCommand(fleetCmd)
	fleetCmd.AddCommand(fleetPoolsCmd, fleetValidateCmd, fleetPlanCmd, fleetReplaceCmd, fleetReconcileCmd, fleetCheckpointsCmd, fleetAttemptsCmd, fleetAttemptCmd, fleetGitHubCmd)
	fleetAttemptCmd.AddCommand(fleetAttemptStartCmd, fleetAttemptHeartbeatCmd, fleetAttemptAdvanceCmd, fleetAttemptRetryCmd, fleetAttemptCancelCmd)
	fleetGitHubCmd.AddCommand(fleetGitHubStatusCmd, fleetGitHubPullRequestCmd, fleetGitHubApplyCmd)
	fleetGitHubApplyCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Acknowledge a reviewed replacement or contraction")
	for _, command := range []*cobra.Command{fleetPlanCmd, fleetReplaceCmd, fleetReconcileCmd} {
		command.Flags().IntVar(&fleetDesired, "desired", 0, "Proposed desired node count")
		command.Flags().StringVar(&fleetSize, "size", "", "Proposed immutable provider VM size")
		command.Flags().StringVar(&fleetStrategy, "strategy", "", "Replacement strategy: blueGreen or rolling")
		command.Flags().StringVar(&fleetReason, "reason", "", "Operator reason recorded with the durable plan")
	}
	if err := fleetReplaceCmd.MarkFlagRequired("size"); err != nil {
		panic(err)
	}
	fleetAttemptStartCmd.Flags().StringVar(&fleetRunnerID, "runner-id", "", "Unique protected runner attempt ID")
	fleetAttemptStartCmd.Flags().StringVar(&fleetCommitSHA, "commit", "", "Reviewed norn-fleet commit SHA")
	fleetAttemptStartCmd.Flags().StringVar(&fleetPlanSHA256, "plan-sha256", "", "Reviewed Terraform plan SHA-256")
	fleetAttemptStartCmd.Flags().StringVar(&fleetWorkflowURL, "workflow-url", "", "Credential-free HTTPS runner URL")
	fleetAttemptStartCmd.Flags().IntVar(&fleetHeartbeatTimeout, "heartbeat-timeout", 120, "Seconds before a silent runner is abandoned")
	for _, name := range []string{"runner-id", "commit", "plan-sha256"} {
		_ = fleetAttemptStartCmd.MarkFlagRequired(name)
	}
	fleetAttemptHeartbeatCmd.Flags().StringVar(&fleetPhase, "phase", "", "Current durable phase")
	fleetAttemptHeartbeatCmd.Flags().StringVar(&fleetMessage, "message", "", "Bounded runner progress message")
	fleetAttemptHeartbeatCmd.Flags().Int64Var(&fleetSequence, "sequence", 0, "Monotonic heartbeat sequence")
	fleetAttemptHeartbeatCmd.Flags().Int64Var(&fleetRevision, "revision", 0, "Expected durable attempt revision")
	for _, name := range []string{"phase", "sequence", "revision"} {
		_ = fleetAttemptHeartbeatCmd.MarkFlagRequired(name)
	}
	fleetAttemptAdvanceCmd.Flags().StringVar(&fleetPhase, "phase", "", "Current phase with recorded successful proof")
	fleetAttemptAdvanceCmd.Flags().Int64Var(&fleetRevision, "revision", 0, "Expected durable attempt revision")
	_ = fleetAttemptAdvanceCmd.MarkFlagRequired("phase")
	_ = fleetAttemptAdvanceCmd.MarkFlagRequired("revision")
	fleetAttemptRetryCmd.Flags().StringVar(&fleetRunnerID, "runner-id", "", "Unique replacement runner attempt ID")
	fleetAttemptRetryCmd.Flags().StringVar(&fleetAttemptReason, "reason", "", "Operator reason for retry")
	fleetAttemptRetryCmd.Flags().StringVar(&fleetWorkflowURL, "workflow-url", "", "Credential-free HTTPS replacement runner URL")
	fleetAttemptRetryCmd.Flags().Int64Var(&fleetRevision, "revision", 0, "Expected failed or abandoned attempt revision")
	for _, name := range []string{"runner-id", "reason", "revision"} {
		_ = fleetAttemptRetryCmd.MarkFlagRequired(name)
	}
	fleetAttemptCancelCmd.Flags().StringVar(&fleetAttemptReason, "reason", "", "Operator reason for cancellation")
	fleetAttemptCancelCmd.Flags().Int64Var(&fleetRevision, "revision", 0, "Expected running attempt revision")
	_ = fleetAttemptCancelCmd.MarkFlagRequired("reason")
	_ = fleetAttemptCancelCmd.MarkFlagRequired("revision")
}

var fleetCmd = &cobra.Command{Use: "fleet", Short: "Inspect and plan GitOps-managed fleet capacity"}
var fleetGitHubCmd = &cobra.Command{Use: "github", Short: "Use the repository-scoped GitHub App fleet bridge"}
var fleetAttemptCmd = &cobra.Command{Use: "attempt", Short: "Drive a durable protected-runner attempt"}

var fleetGitHubStatusCmd = &cobra.Command{
	Use: "status", Short: "Verify the fleet GitHub App installation", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := client.FleetGitHubStatus()
		if err != nil {
			return fmt.Errorf("fleet GitHub status: %w", err)
		}
		state := style.Unhealthy.Render("not connected")
		if status.Connected {
			state = style.Healthy.Render("connected")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", state, status.Repository)
		if status.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), status.Message)
		}
		if !status.Connected {
			return fmt.Errorf("fleet GitHub App is not ready")
		}
		return nil
	},
}

var fleetGitHubPullRequestCmd = &cobra.Command{
	Use: "pr <plan-id>", Short: "Open or recover the reviewed fleet pull request", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client.CreateFleetPullRequest(args[0])
		if err != nil {
			return fmt.Errorf("fleet GitHub pull request: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s pull request receipt %s\n", style.Healthy.Render("recorded"), op.ID)
		if value, ok := op.Payload["url"].(string); ok && value != "" {
			fmt.Fprintln(cmd.OutOrStdout(), value)
		}
		return nil
	},
}

var fleetGitHubApplyCmd = &cobra.Command{
	Use: "apply <plan-id>", Short: "Dispatch or recover the protected apply after review", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		op, err := client.DispatchFleetApply(args[0], fleetAllowDestructive)
		if err != nil {
			return fmt.Errorf("fleet GitHub apply: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s apply dispatch receipt %s\n", style.Healthy.Render("recorded"), op.ID)
		if value, ok := op.Payload["url"].(string); ok && value != "" {
			fmt.Fprintln(cmd.OutOrStdout(), value)
		}
		return nil
	},
}

var fleetPoolsCmd = &cobra.Command{
	Use: "pools", Short: "List desired node pools from the checked-out norn-fleet document", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		inventory, err := client.FleetInventory()
		if err != nil {
			return fmt.Errorf("fleet inventory: %w", err)
		}
		if !inventory.Configured {
			return fmt.Errorf("fleet is not configured; set NORN_FLEET_CONFIG on the Norn server")
		}
		if inventory.Validation != nil && !inventory.Validation.Valid {
			printFleetValidation(cmd.OutOrStdout(), inventory.Validation)
			return fmt.Errorf("configured fleet document is invalid")
		}
		names := make([]string, 0, len(inventory.NodePools))
		for name := range inventory.NodePools {
			names = append(names, name)
		}
		sort.Strings(names)
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "POOL\tSIZE\tMIN\tDESIRED\tMAX\tSTRATEGY\tDRAIN")
		for _, name := range names {
			pool := inventory.NodePools[name]
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", name, pool.Size, pool.Min, pool.Desired, pool.Max, pool.Replacement.Strategy, pool.Replacement.DrainTimeout)
		}
		return w.Flush()
	},
}

var fleetValidateCmd = &cobra.Command{
	Use: "validate <cluster.yaml>", Short: "Run strict schema and infrastructure sanity validation", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		document, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("read fleet document: %w", err)
		}
		report, err := client.ValidateFleetDocument(string(document))
		if err != nil {
			return fmt.Errorf("fleet validation: %w", err)
		}
		printFleetValidation(cmd.OutOrStdout(), report)
		if !report.Valid {
			return fmt.Errorf("fleet document is invalid")
		}
		return nil
	},
}

var fleetPlanCmd = fleetPlanCommand("plan", "Create a durable capacity plan without changing cloud resources", "")
var fleetReplaceCmd = fleetPlanCommand("replace", "Plan immutable blue/green replacement", "blueGreen")
var fleetReconcileCmd = fleetPlanCommand("reconcile", "Record a desired-state drift reconciliation plan", "")

var fleetCheckpointsCmd = &cobra.Command{
	Use: "checkpoints <plan-id>", Short: "Show durable hands-off recovery checkpoints", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := client.FleetReconciliations(args[0])
		if err != nil {
			return fmt.Errorf("fleet checkpoints: %w", err)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PHASE\tSTATUS\tSTATE\tEVIDENCE")
		for index := len(result.Reconciliations) - 1; index >= 0; index-- {
			op := result.Reconciliations[index]
			phase, _ := op.Payload["phase"].(string)
			evidence, _ := op.Payload["evidenceDigest"].(string)
			state := ""
			if value, ok := op.Payload["stateSerial"].(float64); ok && value > 0 {
				state = fmt.Sprintf("%.0f", value)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", phase, op.Status, state, evidence)
		}
		return w.Flush()
	},
}

var fleetAttemptsCmd = &cobra.Command{
	Use: "attempts <plan-id>", Short: "Show durable runner liveness and phase state", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := client.FleetRunnerAttempts(args[0])
		if err != nil {
			return fmt.Errorf("fleet runner attempts: %w", err)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ATTEMPT\tSTATUS\tPHASE\tHEARTBEAT\tREVISION\tRUNNER")
		for _, attempt := range result.Attempts {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\n", attempt.Attempt, attempt.Status, attempt.CurrentPhase, attempt.HeartbeatAt, attempt.Revision, attempt.RunnerAttemptID)
		}
		return w.Flush()
	},
}

var fleetAttemptStartCmd = &cobra.Command{
	Use: "start <plan-id>", Short: "Register a protected runner and begin durable heartbeats", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attempt, err := client.StartFleetRunnerAttempt(args[0], fleetRunnerID, fleetCommitSHA, fleetPlanSHA256, fleetWorkflowURL, fleetHeartbeatTimeout)
		if err != nil {
			return fmt.Errorf("start fleet runner attempt: %w", err)
		}
		return printFleetAttempt(cmd.OutOrStdout(), attempt)
	},
}

var fleetAttemptHeartbeatCmd = &cobra.Command{
	Use: "heartbeat <plan-id> <attempt-id>", Short: "Record monotonic liveness for the current phase", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		attempt, err := client.HeartbeatFleetRunnerAttempt(args[0], args[1], fleetPhase, fleetMessage, fleetSequence, fleetRevision)
		if err != nil {
			return fmt.Errorf("heartbeat fleet runner attempt: %w", err)
		}
		return printFleetAttempt(cmd.OutOrStdout(), attempt)
	},
}

var fleetAttemptAdvanceCmd = &cobra.Command{
	Use: "advance <plan-id> <attempt-id>", Short: "Advance only after successful phase evidence exists", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		attempt, err := client.AdvanceFleetRunnerAttempt(args[0], args[1], fleetPhase, fleetRevision)
		if err != nil {
			return fmt.Errorf("advance fleet runner attempt: %w", err)
		}
		return printFleetAttempt(cmd.OutOrStdout(), attempt)
	},
}

var fleetAttemptRetryCmd = &cobra.Command{
	Use: "retry <plan-id> <attempt-id>", Short: "Create the next numbered attempt from a failed or abandoned run", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		attempt, err := client.RetryFleetRunnerAttempt(args[0], args[1], fleetRunnerID, fleetAttemptReason, fleetWorkflowURL, fleetRevision)
		if err != nil {
			return fmt.Errorf("retry fleet runner attempt: %w", err)
		}
		return printFleetAttempt(cmd.OutOrStdout(), attempt)
	},
}

var fleetAttemptCancelCmd = &cobra.Command{
	Use: "cancel <plan-id> <attempt-id>", Short: "Cancel the current revision of a live runner attempt", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		attempt, err := client.CancelFleetRunnerAttempt(args[0], args[1], fleetAttemptReason, fleetRevision)
		if err != nil {
			return fmt.Errorf("cancel fleet runner attempt: %w", err)
		}
		return printFleetAttempt(cmd.OutOrStdout(), attempt)
	},
}

func printFleetAttempt(out io.Writer, attempt *api.FleetRunnerAttempt) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ATTEMPT\tSTATUS\tPHASE\tHEARTBEAT\tREVISION\tID")
	fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\n", attempt.Attempt, attempt.Status, attempt.CurrentPhase, attempt.HeartbeatAt, attempt.Revision, attempt.ID)
	return w.Flush()
}

func fleetPlanCommand(use, short, forcedStrategy string) *cobra.Command {
	return &cobra.Command{Use: use + " <pool>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var desired *int
		if cmd.Flags().Changed("desired") {
			value := fleetDesired
			desired = &value
		}
		strategy := fleetStrategy
		if forcedStrategy != "" {
			strategy = forcedStrategy
		}
		op, err := client.PlanFleetCapacity(args[0], desired, fleetSize, strategy, fleetReason)
		if err != nil {
			return fmt.Errorf("fleet plan: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s capacity plan %s (%s)\n", style.Healthy.Render("recorded"), op.ID, op.Message)
		if workflow, ok := op.Payload["workflowUrl"].(string); ok && workflow != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "review/apply: %s\n", workflow)
		}
		return nil
	}}
}

func printFleetValidation(out io.Writer, report *api.FleetValidationReport) {
	status := style.Healthy.Render("✓ valid")
	if !report.Valid {
		status = style.Unhealthy.Render("✗ invalid")
	}
	fmt.Fprintf(out, "%s  %s\n", style.Bold.Render(report.Name), status)
	for _, finding := range report.Findings {
		fmt.Fprintf(out, "  %s  %s  %s\n", finding.Severity, finding.Code, finding.Message)
	}
}
