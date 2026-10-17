package githubapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type canceledRunJobs struct {
	TotalCount int                 `json:"total_count"`
	Jobs       []reconciliationJob `json:"jobs"`
}

type canceledRunArtifacts struct {
	TotalCount int `json:"total_count"`
	Artifacts  []struct {
		ID int64 `json:"id"`
	} `json:"artifacts"`
}

type canceledRunSnapshot struct {
	Run       applyRun
	Jobs      canceledRunJobs
	Artifacts canceledRunArtifacts
}

// ReconcileCanceledBeforeExecution proves that the exact nonce-bound apply
// run was cancelled after its fixed hosted admission job, but before the
// provider-bearing apply job acquired a runner or executed a step. It is
// lookup-only and makes no claim about provider state outside the workflow.
func (c *Client) ReconcileCanceledBeforeExecution(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonceHash string, runID, runAttempt int64) (*Reconciliation, error) {
	if (fleetEnvironment != "staging/nyc3" && fleetEnvironment != "production/nyc3") || fleetEnvironment != c.fleetRoot() || c.cfg.PilotRunID != "" || approved == nil || runID <= 0 || runAttempt != 1 || approved.PlanRunID <= 0 || approved.PilotRunID != "" || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !sha256Re.MatchString(nonceHash) {
		return nil, ErrDispatchAmbiguous
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read"})
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	actor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, ErrDispatchAmbiguous
	}
	read := func() (canceledRunSnapshot, error) {
		var snapshot canceledRunSnapshot
		run, err := c.getApplyRun(ctx, token, runID)
		if err != nil {
			return snapshot, err
		}
		snapshot.Run = *run
		if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/jobs?filter=all&per_page=100", runID)), nil, &snapshot.Jobs); err != nil {
			return snapshot, err
		}
		if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/artifacts?per_page=100", runID)), nil, &snapshot.Artifacts); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}
	first, err := read()
	if err != nil {
		return nil, fmt.Errorf("read cancelled run snapshot: %w", ErrDispatchAmbiguous)
	}
	second, err := read()
	if err != nil || !reflect.DeepEqual(first, second) {
		return nil, fmt.Errorf("cancelled run snapshot changed: %w", ErrDispatchAmbiguous)
	}
	run := &first.Run
	if run.ID != runID || int64(run.RunAttempt) != runAttempt || run.Status != "completed" || run.Conclusion != "cancelled" || c.verifyApplyRunByNonceHash(run, planID, fleetEnvironment, approved, nonceHash, actor) != nil {
		return nil, fmt.Errorf("cancelled run identity differs: %w", ErrDispatchAmbiguous)
	}
	_, _, nonce, ok := parseApplyRunDisplayTitle(run.DisplayTitle, c.cfg.PilotRunID)
	// GitHub may return inputs:null for a cancelled workflow. The immutable
	// title still binds the server-only nonce; when inputs are present, require
	// the complete protected binding as an additional check.
	if !ok || (len(run.Inputs) != 0 && verifyObservedApplyInputs(run, planID, fleetEnvironment, allowDestructive, approved, nonce) != nil) {
		return nil, fmt.Errorf("cancelled run inputs differ: %w", ErrDispatchAmbiguous)
	}
	if first.Jobs.TotalCount != 2 || len(first.Jobs.Jobs) != 2 || first.Artifacts.TotalCount != len(first.Artifacts.Artifacts) || first.Artifacts.TotalCount != 0 {
		return nil, fmt.Errorf("cancelled run job or artifact count differs: %w", ErrDispatchAmbiguous)
	}
	seenAdmission, seenApply := false, false
	for _, job := range first.Jobs.Jobs {
		if job.RunID != runID || int64(job.RunAttempt) != runAttempt || job.Status != "completed" {
			return nil, fmt.Errorf("cancelled run job identity differs: %w", ErrDispatchAmbiguous)
		}
		switch job.Name {
		case "pilot_admission":
			expectedSteps := []string{"Set up job", "Require selected admission runner", "Run set -euo pipefail", "Complete job"}
			if seenAdmission || job.Conclusion != "success" || job.RunnerID <= 0 || job.RunnerName != fmt.Sprintf("GitHub Actions %d", job.RunnerID) || job.StartedAt == "" || !reflect.DeepEqual(job.Labels, []string{"ubuntu-24.04"}) || len(job.Steps) != len(expectedSteps) {
				return nil, fmt.Errorf("pilot admission job differs: %w", ErrDispatchAmbiguous)
			}
			for i, step := range job.Steps {
				if step.Number != i+1 || step.Name != expectedSteps[i] || step.Status != "completed" || step.Conclusion != "success" {
					return nil, fmt.Errorf("pilot admission step differs: %w", ErrDispatchAmbiguous)
				}
			}
			seenAdmission = true
		case "apply":
			expectedLabels := []string{"self-hosted", "Linux", "X64", "norn-fleet-protected", "norn-work-staging-fleet-effect"}
			if fleetEnvironment == "production/nyc3" {
				expectedLabels = []string{"self-hosted", "norn-fleet"}
			}
			if seenApply || job.Conclusion != "cancelled" || job.RunnerID != 0 || job.RunnerName != "" || !reflect.DeepEqual(job.Labels, expectedLabels) || len(job.Steps) != 0 {
				return nil, fmt.Errorf("provider apply job executed or differs: %w", ErrDispatchAmbiguous)
			}
			seenApply = true
		default:
			return nil, fmt.Errorf("unexpected cancelled run job: %w", ErrDispatchAmbiguous)
		}
	}
	if !seenAdmission || !seenApply {
		return nil, fmt.Errorf("required cancelled run jobs are absent: %w", ErrDispatchAmbiguous)
	}
	return &Reconciliation{Outcome: "verified-cancelled-before-execution", CanceledRun: &CanceledBeforeExecution{RunID: runID, RunAttempt: runAttempt, URL: run.HTMLURL, Conclusion: run.Conclusion, JobCount: first.Jobs.TotalCount, ArtifactCount: first.Artifacts.TotalCount, ObservedAt: c.now().UTC()}}, nil
}

// RunSummary is one entry of a bounded, unauthoritative listing snapshot used
// only as break-glass abandon evidence. It does not verify the dispatch
// nonce, the actor or the head commit of any entry.
type RunSummary struct {
	RunID        int64     `json:"runId"`
	RunAttempt   int64     `json:"runAttempt,omitempty"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion,omitempty"`
	DisplayTitle string    `json:"displayTitle"`
	URL          string    `json:"url"`
	ObservedAt   time.Time `json:"observedAt"`
}

// planRunsSnapshotLimit bounds the break-glass evidence snapshot so a plan ID
// that happens to match many runs cannot grow it unboundedly.
const planRunsSnapshotLimit = 20

// ObserveApplyRunByNonceHash observes one specific attempt of the bound apply
// dispatch run using only the dispatch nonce hash. PG durably retains the
// nonce hash alone (migration 44 drops the raw nonce column), so a release
// proof computed on PG must verify by hash rather than by the raw nonce that
// verifyApplyRun and observeApplyRun require. A run that cannot be fetched,
// matched to the bound identity, or confirmed consistent with the run's
// latest attempt stays unobserved: callers must treat the error as
// ambiguous, never as proof the run finished.
func (c *Client) ObserveApplyRunByNonceHash(ctx context.Context, planID, fleetEnvironment string, approved *Dispatch, nonceHash string, runAttempt int64) (*ApplyRunObservation, error) {
	if fleetEnvironment != c.fleetRoot() || approved == nil || approved.RunID <= 0 || approved.PlanRunID <= 0 || approved.PilotRunID != c.cfg.PilotRunID || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !sha256Re.MatchString(nonceHash) || runAttempt <= 0 {
		return nil, fmt.Errorf("protected apply run binding is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read"})
	if err != nil {
		return nil, err
	}
	actor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, err
	}
	var run applyRun
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/attempts/%d", approved.RunID, runAttempt)), nil, &run); err != nil {
		return nil, err
	}
	if run.ID != approved.RunID || c.verifyApplyRunByNonceHash(&run, planID, fleetEnvironment, approved, nonceHash, actor) != nil || int64(run.RunAttempt) != runAttempt || !validApplyRunObservationState(run.Status, run.Conclusion) {
		return nil, fmt.Errorf("GitHub apply run state or identity is inconsistent")
	}
	// A rerun can advance the same run ID to a newer attempt after the one
	// requested here completed. Cross-check the run's current attempt to
	// avoid treating a superseded attempt as the final word.
	latest, err := c.getApplyRun(ctx, token, approved.RunID)
	if err != nil {
		return nil, err
	}
	if c.verifyApplyRunByNonceHash(latest, planID, fleetEnvironment, approved, nonceHash, actor) != nil || int64(latest.RunAttempt) != runAttempt || latest.Status != run.Status || latest.Conclusion != run.Conclusion {
		return nil, fmt.Errorf("protected apply run has a newer or disagreeing attempt")
	}
	return &ApplyRunObservation{RunID: run.ID, RunAttempt: int64(run.RunAttempt), Status: run.Status, Conclusion: run.Conclusion, ObservedAt: c.now().UTC()}, nil
}

// recoverRunDisplayTitleFormat mirrors the recover workflow's run-name,
// "Recover Fleet apply ${{ github.event.workflow_run.id || inputs.apply_run_id }}".
const recoverRunDisplayTitleFormat = "Recover Fleet apply %d"

// ObserveRecoverRun observes one specific attempt of a recover workflow run
// that is already recorded on a runner attempt (its OIDC-verified run ID,
// run attempt and commit). It is release evidence that this exact run has or
// has not finished, not an authorization check: the runner's authority was
// established at admission.
//
// Identity: the run lives in the canonical repository on the default branch,
// at the configured RecoverWorkflow path and at recordedHeadSHA, and it is
// bound to the plan's apply run boundApplyRunID:
//   - workflow_dispatch: the apply_run_id input equals boundApplyRunID and
//     the dispatch_nonce input hashes to nonceHash. Inputs that are absent
//     fail closed.
//   - workflow_run: the REST run object exposes no triggering run ID, so the
//     binding is the GitHub-rendered display title, which the workflow file
//     (pinned here by path, branch and head SHA) derives from
//     github.event.workflow_run.id. That event payload is GitHub-generated
//     and cannot be supplied by a caller.
//
// In both cases the display title must equal "Recover Fleet apply
// <boundApplyRunID>". There is deliberately no actor restriction:
// workflow_run recoveries inherit the actor that triggered apply, and manual
// recovery is dispatched by a human operator, so neither the bot nor any
// fixed login identifies a legitimate recover run.
//
// The exact attempt is cross-checked against the run's latest attempt, so a
// superseded attempt is never reported as final. Any error means the run is
// unobserved, never that it finished.
func (c *Client) ObserveRecoverRun(ctx context.Context, boundApplyRunID int64, nonceHash string, runID, runAttempt int64, recordedHeadSHA string) (*ApplyRunObservation, error) {
	if boundApplyRunID <= 0 || !sha256Re.MatchString(nonceHash) || runID <= 0 || runID == boundApplyRunID || runAttempt <= 0 || !commitSHARe.MatchString(recordedHeadSHA) {
		return nil, fmt.Errorf("recover run binding is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read"})
	if err != nil {
		return nil, err
	}
	var run applyRun
	if err := c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/attempts/%d", runID, runAttempt)), nil, &run); err != nil {
		return nil, err
	}
	if err := c.verifyRecoverRunIdentity(&run, runID, boundApplyRunID, nonceHash, recordedHeadSHA); err != nil {
		return nil, err
	}
	if int64(run.RunAttempt) != runAttempt || !validApplyRunObservationState(run.Status, run.Conclusion) {
		return nil, fmt.Errorf("GitHub recover run state is inconsistent")
	}
	latest, err := c.getApplyRun(ctx, token, runID)
	if err != nil {
		return nil, err
	}
	if err := c.verifyRecoverRunIdentity(latest, runID, boundApplyRunID, nonceHash, recordedHeadSHA); err != nil {
		return nil, err
	}
	if int64(latest.RunAttempt) != runAttempt || latest.Status != run.Status || latest.Conclusion != run.Conclusion {
		return nil, fmt.Errorf("protected recover run has a newer or disagreeing attempt")
	}
	return &ApplyRunObservation{RunID: run.ID, RunAttempt: int64(run.RunAttempt), Status: run.Status, Conclusion: run.Conclusion, ObservedAt: c.now().UTC()}, nil
}

func (c *Client) verifyRecoverRunIdentity(run *applyRun, runID, boundApplyRunID int64, nonceHash, recordedHeadSHA string) error {
	if run == nil || run.ID != runID || !canonicalWorkflowURL(c.cfg.Repository, runID, run.HTMLURL) || run.HeadBranch != c.cfg.DefaultBranch || !workflowPathMatches(run.Path, c.cfg.RecoverWorkflow, c.cfg.DefaultBranch) || run.HeadSHA != recordedHeadSHA || run.DisplayTitle != fmt.Sprintf(recoverRunDisplayTitleFormat, boundApplyRunID) {
		return fmt.Errorf("GitHub recover run does not match the recorded recovery identity")
	}
	switch run.Event {
	case "workflow_run":
		return nil
	case "workflow_dispatch":
		nonce := run.Inputs["dispatch_nonce"]
		sum := sha256.Sum256([]byte(nonce))
		if run.Inputs["apply_run_id"] != strconv.FormatInt(boundApplyRunID, 10) || !dispatchNonceRe.MatchString(nonce) || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(nonceHash)) != 1 {
			return fmt.Errorf("GitHub recover run is not bound to the protected apply dispatch")
		}
		return nil
	}
	return fmt.Errorf("GitHub recover run event is not a recovery trigger")
}

// titleHasPlanIDToken reports whether displayTitle contains planID as a whole
// token, so a title carrying a longer identifier that embeds planID does not
// match.
func titleHasPlanIDToken(displayTitle, planID string) bool {
	if planID == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(displayTitle[offset:], planID)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(planID)
		if (start == 0 || !planIDTokenByte(displayTitle[start-1])) && (end == len(displayTitle) || !planIDTokenByte(displayTitle[end])) {
			return true
		}
		offset = start + 1
	}
}

func planIDTokenByte(b byte) bool {
	return b == '-' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// ListPlanRuns returns a bounded, unauthoritative snapshot of apply-workflow
// runs whose display title carries the given plan ID, for use as
// break-glass abandon evidence (the GitHub listing snapshot referenced by the
// fence design). It performs a single listing call and verifies none of the
// nonce, actor or head commit of any entry, unlike the two observers above.
// Only the newest page of the apply workflow's runs is read, so recover-workflow
// runs and older runs never appear: an empty result is a recorded observation,
// never proof that no run exists, and no caller may release a fence on it
// except through the age-gated abandon mode.
func (c *Client) ListPlanRuns(ctx context.Context, planID string) ([]RunSummary, error) {
	if !planIDRe.MatchString(planID) {
		return nil, fmt.Errorf("plan ID is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read"})
	if err != nil {
		return nil, err
	}
	var runs struct {
		WorkflowRuns []struct {
			ID           int64  `json:"id"`
			HTMLURL      string `json:"html_url"`
			DisplayTitle string `json:"display_title"`
			Status       string `json:"status"`
			Conclusion   string `json:"conclusion"`
			RunAttempt   int    `json:"run_attempt"`
		} `json:"workflow_runs"`
	}
	query := "?event=workflow_dispatch&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&per_page=100"
	if err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(c.cfg.ApplyWorkflow)+"/runs"+query), nil, &runs); err != nil {
		return nil, err
	}
	observedAt := c.now().UTC()
	summaries := make([]RunSummary, 0, planRunsSnapshotLimit)
	for _, run := range runs.WorkflowRuns {
		if !titleHasPlanIDToken(run.DisplayTitle, planID) {
			continue
		}
		summaries = append(summaries, RunSummary{RunID: run.ID, RunAttempt: int64(run.RunAttempt), Status: run.Status, Conclusion: run.Conclusion, DisplayTitle: run.DisplayTitle, URL: run.HTMLURL, ObservedAt: observedAt})
		if len(summaries) == planRunsSnapshotLimit {
			break
		}
	}
	return summaries, nil
}
