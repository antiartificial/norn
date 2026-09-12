package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
	"norn/v2/cli/style"
)

var (
	fleetDesired                int
	fleetSize                   string
	fleetStrategy               string
	fleetReason                 string
	fleetAllowDestructive       bool
	fleetDispatchNonceFile      string
	fleetApprovalEnvelopeSHA256 string
	fleetConfirmLostNonce       bool
)

func init() {
	rootCmd.AddCommand(fleetCmd)
	fleetCmd.AddCommand(fleetPoolsCmd, fleetValidateCmd, fleetPlanCmd, fleetReplaceCmd, fleetReconcileCmd, fleetCheckpointsCmd, fleetAttemptsCmd, fleetGitHubCmd)
	fleetGitHubCmd.AddCommand(fleetGitHubStatusCmd, fleetGitHubPullRequestCmd, fleetGitHubApplyCmd, fleetGitHubPrepareCmd, fleetGitHubPrepareResetCmd, fleetGitHubExecuteCmd, fleetGitHubRerunCmd)
	fleetGitHubApplyCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Acknowledge a reviewed replacement or contraction")
	fleetGitHubPrepareCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Acknowledge a reviewed replacement or contraction")
	fleetGitHubPrepareCmd.Flags().StringVar(&fleetDispatchNonceFile, "nonce-file", "", "New absolute 0600 file for the one-time external-Mac nonce")
	fleetGitHubPrepareResetCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Match the original reviewed replacement or contraction acknowledgement")
	fleetGitHubPrepareResetCmd.Flags().BoolVar(&fleetConfirmLostNonce, "confirm-lost-nonce", false, "Confirm the no-store preparation response was lost before approval or execute")
	fleetGitHubExecuteCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Acknowledge a reviewed replacement or contraction")
	fleetGitHubExecuteCmd.Flags().StringVar(&fleetDispatchNonceFile, "nonce-file", "", "Existing 0600 file holding the prepared nonce")
	fleetGitHubExecuteCmd.Flags().StringVar(&fleetApprovalEnvelopeSHA256, "approval-envelope-sha256", "", "Canonical signed approval-envelope SHA-256")
	fleetGitHubRerunCmd.Flags().BoolVar(&fleetAllowDestructive, "allow-destructive", false, "Acknowledge a reviewed replacement or contraction")
	fleetGitHubRerunCmd.Flags().StringVar(&fleetApprovalEnvelopeSHA256, "approval-envelope-sha256", "", "Exact approval-envelope SHA-256 bound to the source dispatch")
	for _, command := range []*cobra.Command{fleetPlanCmd, fleetReplaceCmd, fleetReconcileCmd} {
		command.Flags().IntVar(&fleetDesired, "desired", 0, "Proposed desired node count")
		command.Flags().StringVar(&fleetSize, "size", "", "Proposed immutable provider VM size")
		command.Flags().StringVar(&fleetStrategy, "strategy", "", "Replacement strategy: blueGreen or rolling")
		command.Flags().StringVar(&fleetReason, "reason", "", "Operator reason recorded with the durable plan")
	}
	if err := fleetReplaceCmd.MarkFlagRequired("size"); err != nil {
		panic(err)
	}
}

var fleetGitHubPrepareCmd = &cobra.Command{Use: "prepare <plan-id>", Short: "Prepare an external-Mac apply and store its one-time nonce", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if !filepath.IsAbs(fleetDispatchNonceFile) {
		return fmt.Errorf("--nonce-file must be an absolute path")
	}
	prepared, err := client.PrepareFleetApply(args[0], fleetAllowDestructive)
	if err != nil {
		return fmt.Errorf("fleet GitHub prepare: %w", err)
	}
	if len(prepared.DispatchNonce) != 64 {
		return fmt.Errorf("fleet GitHub prepare did not return a new one-time nonce; inspect the existing preparation")
	}
	f, err := os.OpenFile(fleetDispatchNonceFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private nonce file: %w; if the prepare response is irretrievably lost before approval or execute, run `norn fleet github prepare-reset %s --confirm-lost-nonce`", err, args[0])
	}
	written, err := f.WriteString(prepared.DispatchNonce + "\n")
	if err != nil || written != 65 {
		f.Close()
		return fmt.Errorf("write private nonce file: incomplete private nonce write; do not execute; if no approval was issued, run `norn fleet github prepare-reset %s --confirm-lost-nonce`", args[0])
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync private nonce file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close private nonce file: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s prepared plan %s (nonce hash %s)\n", style.Healthy.Render("recorded"), prepared.PlanID, prepared.DispatchNonceSHA256)
	return nil
}}

var fleetGitHubPrepareResetCmd = &cobra.Command{Use: "prepare-reset <plan-id>", Short: "Reset only a confirmed lost unapproved external-Mac preparation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if !fleetConfirmLostNonce {
		return fmt.Errorf("--confirm-lost-nonce is required; never reset after owner approval or execute")
	}
	if err := client.ResetFleetApplyPreparation(args[0], fleetAllowDestructive); err != nil {
		return fmt.Errorf("fleet GitHub prepare reset: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), style.Healthy.Render("unapproved preparation reset"))
	return nil
}}

var fleetGitHubExecuteCmd = &cobra.Command{Use: "execute <plan-id>", Short: "Execute a prepared external-Mac apply with owner approval", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if !filepath.IsAbs(fleetDispatchNonceFile) || len(fleetApprovalEnvelopeSHA256) != 64 {
		return fmt.Errorf("--nonce-file and a 64-character --approval-envelope-sha256 are required")
	}
	nonce, err := readPrivateFleetDispatchNonce(fleetDispatchNonceFile)
	if err != nil {
		return err
	}
	op, err := client.ExecuteFleetApply(args[0], fleetAllowDestructive, nonce, fleetApprovalEnvelopeSHA256)
	if err != nil {
		return fmt.Errorf("fleet GitHub execute: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s apply dispatch receipt %s\n", style.Healthy.Render("recorded"), op.ID)
	if value, ok := op.Payload["url"].(string); ok && value != "" {
		fmt.Fprintln(cmd.OutOrStdout(), value)
	}
	return nil
}}

// readPrivateFleetDispatchNonce binds validation and reading to one opened
// inode. The write-only nonce is sensitive until execute submits it to GitHub;
// following a swapped path or symlink here would silently authorize another
// process's value.
func readPrivateFleetDispatchNonce(path string) (string, error) {
	pre, err := os.Lstat(path)
	if err != nil || !pre.Mode().IsRegular() || pre.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("--nonce-file must be an existing regular mode-0600 file")
	}
	preStat, ok := pre.Sys().(*syscall.Stat_t)
	if !ok || preStat.Uid != uint32(os.Geteuid()) || preStat.Nlink != 1 || pre.Size() != 65 {
		return "", fmt.Errorf("--nonce-file must be a single-link 65-byte private file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open private nonce file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var opened syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil || opened.Dev != preStat.Dev || opened.Ino != preStat.Ino || opened.Uid != uint32(os.Geteuid()) || opened.Nlink != 1 || opened.Size != 65 || opened.Mode&syscall.S_IFMT != syscall.S_IFREG || opened.Mode&0o777 != 0o600 {
		return "", fmt.Errorf("--nonce-file changed or is not an exact private regular inode")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil || len(raw) != 65 || raw[64] != '\n' {
		return "", fmt.Errorf("read private nonce file: expected one nonce line")
	}
	return string(raw[:64]), nil
}

var fleetGitHubRerunCmd = &cobra.Command{Use: "rerun <plan-id>", Short: "Rerun one conclusively failed or cancelled external-Mac apply generation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if len(fleetApprovalEnvelopeSHA256) != 64 {
		return fmt.Errorf("a 64-character --approval-envelope-sha256 is required")
	}
	op, err := client.RerunFleetApply(args[0], fleetAllowDestructive, fleetApprovalEnvelopeSHA256)
	if err != nil {
		return fmt.Errorf("fleet GitHub rerun: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s apply rerun receipt %s\n", style.Healthy.Render("recorded"), op.ID)
	if value, ok := op.Payload["url"].(string); ok && value != "" {
		fmt.Fprintln(cmd.OutOrStdout(), value)
	}
	return nil
}}

var fleetCmd = &cobra.Command{Use: "fleet", Short: "Inspect and plan GitOps-managed fleet capacity"}
var fleetGitHubCmd = &cobra.Command{Use: "github", Short: "Use the repository-scoped GitHub App fleet bridge"}

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
		if status.PilotRunID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "disposable pilot run: %s\n", status.PilotRunID)
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
	Use: "attempts <plan-id>", Short: "Show durable runner liveness and phase state (runner mutations are GitHub-workflow-only)", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := client.FleetRunnerAttempts(args[0])
		if err != nil {
			return fmt.Errorf("fleet runner attempts: %w", err)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ATTEMPT\tSTATUS\tPHASE\tELAPSED\tETA\tCONFIDENCE\tHEARTBEAT\tREVISION\tRUNNER")
		for _, attempt := range result.Attempts {
			elapsed, eta, confidence := fleetAttemptTimingDisplay(attempt.Timing)
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", attempt.Attempt, attempt.Status, attempt.CurrentPhase, elapsed, eta, confidence, attempt.HeartbeatAt, attempt.Revision, attempt.RunnerAttemptID)
		}
		return w.Flush()
	},
}

func fleetAttemptTimingDisplay(timing *api.FleetRunnerTiming) (elapsed, eta, confidence string) {
	if timing == nil {
		return "—", "—", "—"
	}
	elapsed = (time.Duration(timing.ElapsedMs) * time.Millisecond).Round(time.Second).String()
	confidence = timing.Confidence
	if timing.Availability != "available" || timing.EstimatedRemaining == nil {
		return elapsed, "—", confidence
	}
	low := (time.Duration(timing.EstimatedRemaining.LowMs) * time.Millisecond).Round(time.Second).String()
	high := (time.Duration(timing.EstimatedRemaining.HighMs) * time.Millisecond).Round(time.Second).String()
	eta = low
	if low != high {
		eta += "–" + high
	}
	return elapsed, eta, confidence
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
