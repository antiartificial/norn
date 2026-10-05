package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"norn/v2/cli/api"
)

// Executor-lane operator surface (ADR 0010, plan-5-7 section 2.11).

var (
	fleetResJSON            bool
	fleetResKey             string
	fleetResResume          bool
	fleetResCommit          string
	fleetResSourceAttempt   string
	fleetResResource        string
	fleetResReason          string
	fleetResEvidenceFile    string
	fleetResReleaseMode     string
	fleetResExpectedGen     int64
	fleetResExpectedRev     int64
	fleetResProviderAttempt string
)

func init() {
	fleetCmd.AddCommand(fleetStatusCmd, fleetTargetsCmd, fleetFenceCmd, fleetConvergeCmd, fleetApproveCmd, fleetModeCmd, fleetProviderEvidenceCmd, fleetReleaseCmd, fleetAbandonCmd)
	for _, c := range []*cobra.Command{fleetStatusCmd, fleetTargetsCmd, fleetFenceCmd, fleetConvergeCmd, fleetApproveCmd, fleetModeCmd, fleetProviderEvidenceCmd, fleetReleaseCmd, fleetAbandonCmd} {
		c.Flags().BoolVar(&fleetResJSON, "json", false, "Print the raw JSON response")
	}
	for _, c := range []*cobra.Command{fleetConvergeCmd, fleetApproveCmd, fleetModeCmd, fleetProviderEvidenceCmd, fleetReleaseCmd, fleetAbandonCmd} {
		c.Flags().StringVar(&fleetResKey, "idempotency-key", "", "Stable retry key (generated and printed when omitted)")
	}
	fleetConvergeCmd.Flags().BoolVar(&fleetResResume, "resume", false, "Approve one recovery attempt after an executor stop instead of rescanning")
	fleetApproveCmd.Flags().BoolVar(&fleetResResume, "resume", false, "Authorize one recovery attempt of --source-attempt")
	fleetApproveCmd.Flags().StringVar(&fleetResSourceAttempt, "source-attempt", "", "Attempt ID to recover (with --resume; --resource takes it from status.active instead)")
	fleetApproveCmd.Flags().StringVar(&fleetResCommit, "commit", "", "Plan commit SHA (looked up from the resource when omitted)")
	fleetApproveCmd.Flags().StringVar(&fleetResResource, "resource", "", "Resource name to look the commit (or, with --resume, the attempt and commit) up from (scans all resources for the commit when omitted)")
	fleetProviderEvidenceCmd.Flags().StringVar(&fleetResProviderAttempt, "source-attempt", "", "The plan's latest executor attempt ID")
	fleetProviderEvidenceCmd.Flags().StringVar(&fleetResEvidenceFile, "provider-evidence", "", "JSON file with the provider evidence")
	_ = fleetProviderEvidenceCmd.MarkFlagRequired("source-attempt")
	_ = fleetProviderEvidenceCmd.MarkFlagRequired("provider-evidence")
	fleetReleaseCmd.Flags().StringVar(&fleetResReleaseMode, "mode", "", "Release mode: terminal or abandon")
	fleetModeCmd.Flags().Int64Var(&fleetResExpectedRev, "expected-revision", -1, "Resource revision to compare against (read and printed when omitted; pass it when retrying with the same --idempotency-key)")
	fleetReleaseCmd.Flags().Int64Var(&fleetResExpectedGen, "expected-generation", -1, "Fence generation you inspected (norn fleet fence <target-id>)")
	_ = fleetReleaseCmd.MarkFlagRequired("mode")
	_ = fleetReleaseCmd.MarkFlagRequired("expected-generation")
	for _, c := range []*cobra.Command{fleetReleaseCmd, fleetAbandonCmd} {
		c.Flags().StringVar(&fleetResReason, "reason", "", "Operator reason (at most 1024 characters)")
		c.Flags().StringVar(&fleetResEvidenceFile, "provider-evidence", "", "JSON file with provider evidence (executor-lane holders only)")
	}
}

func fleetResOut(cmd *cobra.Command) io.Writer { return cmd.OutOrStdout() }

func fleetResPrintJSON(cmd *cobra.Command, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(fleetResOut(cmd), string(raw))
	return err
}

func fleetResPrintOp(cmd *cobra.Command, op *api.Operation, what string) error {
	if fleetResJSON {
		return fleetResPrintJSON(cmd, op)
	}
	_, err := fmt.Fprintf(fleetResOut(cmd), "%s: operation %s (%s)\n", what, op.ID, op.Status)
	return err
}

func readFleetProviderEvidence(path string) (*api.FleetProviderEvidence, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --provider-evidence: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ev api.FleetProviderEvidence
	if err := dec.Decode(&ev); err != nil {
		return nil, fmt.Errorf("parse --provider-evidence: %w", err)
	}
	if ev.Provider != "digitalocean" || ev.DropletID == "" || ev.ObservedAt == "" {
		return nil, fmt.Errorf("--provider-evidence needs provider digitalocean, dropletId, status and observedAt")
	}
	if _, err := time.Parse(time.RFC3339, ev.ObservedAt); err != nil {
		return nil, fmt.Errorf("--provider-evidence observedAt must be RFC 3339: %w", err)
	}
	switch ev.Status {
	case "off", "archive", "not_found":
	default:
		return nil, fmt.Errorf("--provider-evidence status must be off, archive or not_found")
	}
	return &ev, nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printFleetResource(cmd *cobra.Command, r *api.FleetResource) {
	out := fleetResOut(cmd)
	mode := r.ExecutionMode
	if mode == "" {
		mode = "github"
	}
	fmt.Fprintf(out, "%s  mode=%s  target=%s  controller=%s\n", r.Name, mode, r.TargetID, r.Controller.State)
	fmt.Fprintf(out, "  desired  gen %d  commit %s  plan %s\n", r.Desired.Generation, dash(r.Desired.CommitSHA), dash(r.Desired.PlanID))
	applied := "none"
	if la := r.Status.LastApplied; la != nil {
		applied = fmt.Sprintf("gen %d  commit %s  plan %s", la.Generation, la.CommitSHA, la.PlanID)
	}
	fmt.Fprintf(out, "  applied  %s\n", applied)
	if cv := r.Desired.Converge; cv != nil {
		fmt.Fprintf(out, "  converge requiresApproval=%t approved=%t refused=%t\n", cv.RequiresApproval, cv.Approved, cv.Refused)
	}
	if a := r.Status.Active; a != nil {
		fmt.Fprintf(out, "  active   plan %s  attempts %s  fence gen %d  %s", a.PlanID, dash(strings.Join(a.AttemptIDs, ",")), a.FenceGeneration, a.Occupancy)
		if a.ResumeApproved {
			fmt.Fprint(out, "  resumeApproved")
		}
		if a.ResumeNeededAttemptID != "" {
			fmt.Fprintf(out, "  resumeNeeded %s", a.ResumeNeededAttemptID)
		}
		fmt.Fprintln(out)
	}
	if b := r.Blocker; b != nil {
		fmt.Fprintf(out, "  blocker  %s/%s: %s\n", b.Kind, b.Reason, dash(b.Message))
	}
	fmt.Fprintf(out, "  next     %s\n", dash(r.Status.NextAction))
	for _, c := range r.Status.Conditions {
		fmt.Fprintf(out, "  cond     %s=%s %s\n", c.Type, c.Status, strings.TrimSpace(c.Reason+" "+c.Message))
	}
	p := r.ApprovalPolicy
	fmt.Fprintf(out, "  policy   basis=%s protectedBranch=%t mergedPR=%t planWorkflow=%t boundDigest=%t dispatch=%t ownerEnvelope=%t",
		p.Basis, p.ProtectedBranch, p.MergedPullRequest, p.PlanWorkflowSucceeded, p.BoundPlanDigest, p.AuthorizedDispatch, p.OwnerApprovalEnvelope)
	if p.ApprovedBy != "" {
		fmt.Fprintf(out, " approvedBy=%s", p.ApprovedBy)
	}
	fmt.Fprintln(out)
}

var fleetStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show Fleet resource status (desired vs applied, blocker, next action, policy)",
	Long:  "Show one Fleet resource, or all of them.\n\nPermission: api:read.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			r, err := client.FleetResource(args[0])
			if err != nil {
				return fmt.Errorf("fleet status: %w", err)
			}
			if fleetResJSON {
				return fleetResPrintJSON(cmd, r)
			}
			printFleetResource(cmd, r)
			return nil
		}
		var all []api.FleetResource
		var liveness api.FleetControllerLiveness
		after := ""
		for {
			page, err := client.FleetResources(100, after)
			if err != nil {
				return fmt.Errorf("fleet status: %w", err)
			}
			liveness = page.Controller
			all = append(all, page.Items...)
			if page.Next == "" {
				break
			}
			after = page.Next
		}
		if fleetResJSON {
			return fleetResPrintJSON(cmd, api.FleetResourceList{Items: all, Controller: liveness})
		}
		w := tabwriter.NewWriter(fleetResOut(cmd), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tMODE\tDESIRED\tAPPLIED\tNEXT\tBLOCKER")
		for _, r := range all {
			mode := r.ExecutionMode
			if mode == "" {
				mode = "github"
			}
			blocker := "-"
			if r.Blocker != nil {
				blocker = r.Blocker.Reason
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n", r.Name, mode, r.Desired.Generation, r.Status.LastAppliedGeneration, dash(r.Status.NextAction), blocker)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(fleetResOut(cmd), "controller: %s\n", liveness.State)
		return nil
	},
}

// fleetPlanCommit finds planID's commit in the resource's desired revision or
// its desiredHistory ("" when absent).
func fleetPlanCommit(r *api.FleetResource, planID string) string {
	for _, d := range append([]api.FleetResourceDesired{r.Desired}, r.DesiredHistory...) {
		if d.PlanID == planID && d.CommitSHA != "" {
			return d.CommitSHA
		}
	}
	return ""
}

// fleetResumeSource is the stopped holder attempt the server says needs a
// resume approve (status.active.resumeNeededAttemptId) and the holder plan's
// commit (status.active.commitSha). Both are structured server fields; the
// blocker message is never parsed.
func fleetResumeSource(r *api.FleetResource) (attempt, commit string, err error) {
	a := r.Status.Active
	if a == nil {
		return "", "", fmt.Errorf("%s holds no fence; nothing to resume", r.Name)
	}
	if a.ResumeNeededAttemptID == "" {
		if a.ResumeApproved {
			return "", "", fmt.Errorf("%s already has an unconsumed resume approve for plan %s", r.Name, a.PlanID)
		}
		return "", "", fmt.Errorf("%s has no stopped attempt awaiting a resume approve (it needs a stop attestation or provider evidence first); see `norn fleet status %s`", r.Name, r.Name)
	}
	if a.CommitSHA == "" {
		return "", "", fmt.Errorf("the server did not report holder plan %s's commit for %s; use `norn fleet approve %s --resume --source-attempt %s --commit <sha>`", a.PlanID, r.Name, a.PlanID, a.ResumeNeededAttemptID)
	}
	return a.ResumeNeededAttemptID, a.CommitSHA, nil
}

func fleetResolveCommit(planID, resource string) (string, error) {
	match := func(r api.FleetResource) string { return fleetPlanCommit(&r, planID) }
	if resource != "" {
		r, err := client.FleetResource(resource)
		if err != nil {
			return "", err
		}
		if c := match(*r); c != "" {
			return c, nil
		}
		return "", fmt.Errorf("resource %s has no desired revision for plan %s; pass --commit", resource, planID)
	}
	after := ""
	for {
		page, err := client.FleetResources(100, after)
		if err != nil {
			return "", err
		}
		for _, r := range page.Items {
			if c := match(r); c != "" {
				return c, nil
			}
		}
		if page.Next == "" {
			return "", fmt.Errorf("no resource has a desired revision for plan %s; pass --commit", planID)
		}
		after = page.Next
	}
}

var fleetConvergeCmd = &cobra.Command{
	Use:   "converge <name> [--resume]",
	Short: "Rescan the protected main tip for a converge plan, or approve one recovery with --resume",
	Long: `Without --resume: rescan the protected main tip and record a converge plan only if
the verified tip has no admissible one yet. It never starts an attempt.
Permission: platform:operate (non-CI).

With --resume: after an executor stop, approve one recovery attempt of the holder
plan's latest attempt (fleet.converge.approve with resume). The plan, commit and
source attempt come from status.active (planId, commitSha, resumeNeededAttemptId).
Permission: platform:operate (non-CI).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-converge")
		if err != nil {
			return err
		}
		if !fleetResResume {
			d, err := client.RescanFleetResourceConverge(args[0], key)
			if err != nil {
				return fmt.Errorf("fleet converge: %w", err)
			}
			if fleetResJSON {
				return fleetResPrintJSON(cmd, d)
			}
			_, err = fmt.Fprintf(fleetResOut(cmd), "%s: %s  tip %s  plan %s  %s\n", d.Resource, d.Action, dash(d.TipSHA), dash(d.PlanID), strings.TrimSpace(d.Code+" "+d.Reason))
			return err
		}
		r, err := client.FleetResource(args[0])
		if err != nil {
			return fmt.Errorf("fleet converge --resume: %w", err)
		}
		source, commit, err := fleetResumeSource(r)
		if err != nil {
			return fmt.Errorf("fleet converge --resume: %w", err)
		}
		plan := r.Status.Active.PlanID
		// The retry must send the same body: name it so a re-run cannot
		// pick up a later attempt under the same key.
		fmt.Fprintf(cmd.ErrOrStderr(), "retry with: norn fleet approve %s --resume --source-attempt %s --commit %s --idempotency-key %s\n", plan, source, commit, key)
		op, err := client.ApproveFleetConverge(plan, commit, true, source, key)
		if err != nil {
			return fmt.Errorf("fleet converge --resume: %w", err)
		}
		return fleetResPrintOp(cmd, op, "resume approved")
	},
}

var fleetApproveCmd = &cobra.Command{
	Use:   "approve <plan-id>",
	Short: "Approve one converge plan commit (fleet.converge.approve)",
	Long: `Approve exactly one converge plan commit for the executor lane. The commit is
looked up from the resource whose desired plan is <plan-id> unless --commit is given.
With --resume it authorizes one recovery attempt instead: give --source-attempt
(and --commit), or --resource to take both from status.active.
Permission: platform:operate (non-CI).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if fleetResSourceAttempt != "" && !fleetResResume {
			return fmt.Errorf("--source-attempt requires --resume")
		}
		source, commit := fleetResSourceAttempt, fleetResCommit
		if fleetResResume && source == "" {
			if fleetResResource == "" {
				return fmt.Errorf("--resume needs --source-attempt, or --resource to take the attempt from status.active.resumeNeededAttemptId")
			}
			r, err := client.FleetResource(fleetResResource)
			if err != nil {
				return fmt.Errorf("fleet approve: %w", err)
			}
			if r.Status.Active == nil || r.Status.Active.PlanID != args[0] {
				return fmt.Errorf("fleet approve: %s does not hold plan %s; nothing to resume", fleetResResource, args[0])
			}
			var viewCommit string
			if source, viewCommit, err = fleetResumeSource(r); err != nil {
				return fmt.Errorf("fleet approve: %w", err)
			}
			if commit == "" {
				commit = viewCommit
			}
		}
		if commit == "" {
			var err error
			if commit, err = fleetResolveCommit(args[0], fleetResResource); err != nil {
				return fmt.Errorf("fleet approve: %w", err)
			}
		}
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-approve")
		if err != nil {
			return err
		}
		op, err := client.ApproveFleetConverge(args[0], commit, fleetResResume, source, key)
		if err != nil {
			return fmt.Errorf("fleet approve: %w", err)
		}
		return fleetResPrintOp(cmd, op, "approved "+commit[:min(len(commit), 12)])
	},
}

var fleetModeCmd = &cobra.Command{
	Use:   "mode <name> controller|github",
	Short: "Set a resource's execution mode (admin; the target fence must be Free)",
	Long: `Set executionMode with the resource revision as the compare-and-set token.
The revision is read and printed when --expected-revision is omitted; a retry with
the same --idempotency-key must pass that printed revision. A conflict is not
retried automatically. Permission: admin (non-CI).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[1] != "controller" && args[1] != "github" {
			return fmt.Errorf("mode must be controller or github")
		}
		revision := fleetResExpectedRev
		if revision < 0 {
			r, err := client.FleetResource(args[0])
			if err != nil {
				return fmt.Errorf("fleet mode: %w", err)
			}
			revision = r.Revision
			fmt.Fprintf(cmd.ErrOrStderr(), "expected revision: %d\n", revision)
		}
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-mode")
		if err != nil {
			return err
		}
		op, err := client.SetFleetResourceExecutionMode(args[0], args[1], revision, key)
		if err != nil {
			if strings.Contains(err.Error(), "fleet_resource_revision_conflict") {
				return fmt.Errorf("fleet mode: %s changed after revision %d was read; check `norn fleet status %s` and run again without --expected-revision and with a new key: %w", args[0], revision, args[0], err)
			}
			return fmt.Errorf("fleet mode: %w", err)
		}
		return fleetResPrintOp(cmd, op, args[0]+" mode "+args[1])
	},
}

var fleetProviderEvidenceCmd = &cobra.Command{
	Use:   "provider-evidence <plan-id> --source-attempt <id> --provider-evidence <file>",
	Short: "Record provider evidence for a stopped executor attempt (admin; non-releasing)",
	Long: `Record the admin's assertion that the old executor droplet is off, archived or
not found (observed within the last hour). Norn cannot check it. The old executor
device must already be revoked. Permission: admin (non-CI, no step-up).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ev, err := readFleetProviderEvidence(fleetResEvidenceFile)
		if err != nil {
			return err
		}
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-evidence")
		if err != nil {
			return err
		}
		op, err := client.RecordFleetProviderEvidence(args[0], fleetResProviderAttempt, *ev, key)
		if err != nil {
			return fmt.Errorf("fleet provider-evidence: %w", err)
		}
		return fleetResPrintOp(cmd, op, "provider evidence recorded")
	},
}

var fleetReleaseCmd = &cobra.Command{
	Use:   "release <target-id> --mode terminal|abandon --expected-generation <n> [--provider-evidence <file>]",
	Short: "Release a held target fence and abandon its holder plan (admin)",
	Long: `Release a held fence; either mode also permanently abandons the holder plan.
--expected-generation is the fence generation you inspected (norn fleet fence);
a stale value is refused. For an executor-lane holder, supply --provider-evidence
(or have a stop attestation). Permission: admin (non-CI, no step-up).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if fleetResReleaseMode != "terminal" && fleetResReleaseMode != "abandon" {
			return fmt.Errorf("--mode must be terminal or abandon")
		}
		ev, err := readFleetProviderEvidence(fleetResEvidenceFile)
		if err != nil {
			return err
		}
		gen := fleetResExpectedGen
		if gen < 0 {
			return fmt.Errorf("--expected-generation is required; read it with `norn fleet fence %s`", args[0])
		}
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-release")
		if err != nil {
			return err
		}
		op, err := client.ReleaseFleetTargetFence(args[0], fleetResReleaseMode, gen, fleetResReason, ev, key)
		if err != nil {
			return fmt.Errorf("fleet release: %w", err)
		}
		return fleetResPrintOp(cmd, op, "fence released")
	},
}

var fleetAbandonCmd = &cobra.Command{
	Use:   "abandon <plan-id> [--provider-evidence <file>]",
	Short: "Permanently abandon a plan, releasing its fence if it holds one (admin)",
	Long:  "Permanently abandon a plan (fleet.target.abandon-plan).\n\nPermission: admin (non-CI, no step-up).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ev, err := readFleetProviderEvidence(fleetResEvidenceFile)
		if err != nil {
			return err
		}
		key, err := requestIdempotencyKey(cmd, fleetResKey, "norn-fleet-abandon")
		if err != nil {
			return err
		}
		op, err := client.AbandonFleetPlan(args[0], fleetResReason, ev, key)
		if err != nil {
			return fmt.Errorf("fleet abandon: %w", err)
		}
		return fleetResPrintOp(cmd, op, "plan abandoned")
	},
}

func printFleetFence(cmd *cobra.Command, f *api.FleetTargetFence) {
	out := fleetResOut(cmd)
	state := "free"
	if f.Held {
		state = "held by " + dash(f.HolderPlanID)
	}
	fmt.Fprintf(out, "fence    generation %d  %s  occupancy %s", f.Generation, state, f.Occupancy)
	if f.OccupancyReason != "" {
		fmt.Fprintf(out, " (%s)", f.OccupancyReason)
	}
	fmt.Fprintf(out, "\n  epoch    %d (current %d)  revision %d\n", f.AuthorityEpoch, f.CurrentAuthorityEpoch, f.Revision)
	if lr := f.LastRelease; lr != nil {
		fmt.Fprintf(out, "  released plan %s  %s  at %s\n", lr.PlanID, lr.Reason, lr.At)
	}
}

var fleetTargetsCmd = &cobra.Command{
	Use:   "targets [target-id]",
	Short: "Show a Fleet target (identity, aliases, fence), or list targets bound to resources",
	Long: `With an ID: GET targets/{id}. Without: the targets bound to Fleet resources (the
server has no target list route), with the fence state from each resource.

Permission: api:read.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			t, err := client.FleetTarget(args[0])
			if err != nil {
				return fmt.Errorf("fleet targets: %w", err)
			}
			if fleetResJSON {
				return fleetResPrintJSON(cmd, t)
			}
			out := fleetResOut(cmd)
			fmt.Fprintf(out, "%s  provider=%s account=%s state=%s\n", t.TargetID, t.Provider, t.ProviderAccount, t.StateBackend)
			fmt.Fprintf(out, "  aliases  %s\n", dash(strings.Join(t.Aliases, ", ")))
			fmt.Fprintf(out, "  created  %s  operation %s\n", t.CreatedAt, t.RegistrationOperationID)
			printFleetFence(cmd, &t.Fence)
			return nil
		}
		var all []api.FleetResource
		for after := ""; ; {
			page, err := client.FleetResources(100, after)
			if err != nil {
				return fmt.Errorf("fleet targets: %w", err)
			}
			all = append(all, page.Items...)
			if after = page.Next; after == "" {
				break
			}
		}
		if fleetResJSON {
			return fleetResPrintJSON(cmd, all)
		}
		w := tabwriter.NewWriter(fleetResOut(cmd), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TARGET\tRESOURCE\tFENCE")
		for _, r := range all {
			fence := "free"
			if a := r.Status.Active; a != nil {
				fence = fmt.Sprintf("held gen %d %s", a.FenceGeneration, a.Occupancy)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.TargetID, r.Name, fence)
		}
		return w.Flush()
	},
}

var fleetFenceCmd = &cobra.Command{
	Use:   "fence <target-id>",
	Short: "Show a target's fence generation, holder and occupancy",
	Long:  "GET targets/{id}/fence. The generation is the value norn fleet release needs.\n\nPermission: api:read.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := client.FleetTargetFence(args[0])
		if err != nil {
			return fmt.Errorf("fleet fence: %w", err)
		}
		if fleetResJSON {
			return fleetResPrintJSON(cmd, f)
		}
		fmt.Fprintln(fleetResOut(cmd), f.TargetID)
		printFleetFence(cmd, f)
		return nil
	},
}
