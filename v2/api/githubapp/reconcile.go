package githubapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"norn/v2/api/fleet"
)

var (
	ErrPermanentNoWrite       = errors.New("Fleet GitHub request was refused before mutation")
	ErrPermanentAfterMutation = errors.New("Fleet GitHub request was refused after a possible mutation")
)

type Reconciliation struct {
	Outcome     string       `json:"outcome"`
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
	Dispatch    *Dispatch    `json:"dispatch,omitempty"`
}

type ApplyRunObservation struct {
	RunID      int64     `json:"runId"`
	RunAttempt int64     `json:"runAttempt"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

// ReconcilePullRequest observes the deterministic branch and PR after an
// uncertain write. It requires the same exact content as CreatePullRequest,
// including the immutable review receipt when desired topology is unchanged.
func (c *Client) ReconcilePullRequest(ctx context.Context, planID, planDigest, poolName, action string, proposed fleet.NodePool, sourceDigest string) (*Reconciliation, error) {
	token, err := c.installationToken(ctx, map[string]string{"contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, err
	}
	branch := "norn/plan-" + planID
	pr, err := c.findPullRequest(ctx, token, branch)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		// GitHub lists can lag after a write. Re-read both identities; only an
		// authoritative double absence can justify a new attempt.
		for i := 0; i < 2; i++ {
			_, err = c.getRef(ctx, token, branch)
			var apiErr *apiError
			if err == nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
				return &Reconciliation{Outcome: "ambiguous"}, nil
			}
			pr, err = c.findPullRequest(ctx, token, branch)
			if err != nil {
				return nil, err
			}
			if pr != nil {
				return &Reconciliation{Outcome: "ambiguous"}, nil
			}
		}
		return &Reconciliation{Outcome: "verified-no-write"}, nil
	}
	main, _, err := c.getContent(ctx, token, c.cfg.DefaultBranch)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(main)
	if sourceDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	document, report := fleet.ParseAndValidate(main)
	if document == nil || report == nil || !report.Valid || !strings.EqualFold(document.Metadata.Repository, c.cfg.Repository) {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	current, ok := document.NodePools[poolName]
	if !ok {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	unchanged := reflect.DeepEqual(current, proposed)
	var expected []byte
	var repoPath = c.cfg.ConfigPath
	var title = fmt.Sprintf("Fleet: %s %s", action, poolName)
	var body = fmt.Sprintf("Norn capacity plan `%s`\n\nDigest: `%s`\n\nThis pull request changes desired infrastructure only. Provider and state credentials remain in the protected runner.", planID, planDigest)
	if unchanged {
		repoPath, expected, err = planReviewReceipt(planID, planDigest, sourceDigest, poolName, action, proposed)
		body = fmt.Sprintf("Norn capacity plan `%s`\n\nDigest: `%s`\n\nDesired topology is already exactly the proposed configuration. This pull request adds the immutable Norn review receipt only; it does not manufacture a topology change. Provider and state credentials remain in the protected runner.", planID, planDigest)
	} else {
		document.NodePools[poolName] = proposed
		expected, err = yaml.Marshal(document)
	}
	if err != nil {
		return nil, err
	}
	actual, _, contentErr := c.getContentAt(ctx, token, repoPath, branch)
	refSHA, refErr := c.getRef(ctx, token, branch)
	if contentErr != nil || refErr != nil || !bytes.Equal(actual, expected) || pr.HeadSHA == "" || refSHA != pr.HeadSHA || pr.HeadBranch != branch || pr.BaseBranch != c.cfg.DefaultBranch || pr.Title != title || pr.Body != body || pr.State == "closed" && !pr.Merged {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	return &Reconciliation{Outcome: "remote-success", PullRequest: pr}, nil
}

// ReconcileDispatch reads the server-bound nonce. An absent result stays
// ambiguous because the workflow listing is bounded and eventually consistent.
func (c *Client) ReconcileDispatch(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, approved *Dispatch, nonce string) (*Reconciliation, error) {
	if fleetEnvironment != c.fleetRoot() || approved == nil || approved.PlanRunID <= 0 || approved.PilotRunID != c.cfg.PilotRunID || !sha256Re.MatchString(approved.PlanSHA) || !commitSHARe.MatchString(approved.ApprovedHeadSHA) || !dispatchNonceRe.MatchString(nonce) {
		return nil, fmt.Errorf("protected dispatch binding is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read", "contents": "read", "pull_requests": "read"})
	if err != nil {
		return nil, err
	}
	actor, err := c.appActorLogin(ctx)
	if err != nil {
		return nil, err
	}
	found, err := c.findApplyRun(ctx, token, planID, fleetEnvironment, approved, nonce, actor)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	run, err := c.getApplyRun(ctx, token, found.RunID)
	if err != nil || verifyObservedApplyInputs(run, planID, fleetEnvironment, allowDestructive, approved, nonce) != nil {
		return &Reconciliation{Outcome: "ambiguous"}, nil
	}
	found.Existing = true
	return &Reconciliation{Outcome: "remote-success", Dispatch: found}, nil
}

func verifyObservedApplyInputs(run *applyRun, planID, fleetEnvironment string, allowDestructive bool, bound *Dispatch, nonce string) error {
	if run == nil || bound == nil {
		return fmt.Errorf("apply run binding is absent")
	}
	expected := map[string]string{"fleet_environment": fleetEnvironment, "plan_run_id": fmt.Sprintf("%d", bound.PlanRunID), "plan_sha256": bound.PlanSHA, "norn_plan_id": planID, "allow_destructive": fmt.Sprintf("%t", allowDestructive), "dispatch_nonce": nonce}
	if bound.PilotRunID != "" {
		expected["pilot_run_id"] = bound.PilotRunID
	}
	for key, want := range expected {
		if run.Inputs[key] != want {
			return fmt.Errorf("apply run input %s differs from protected dispatch", key)
		}
	}
	return nil
}

func (c *Client) observeApplyRun(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, bound *Dispatch, nonce string, attempt int64) (*ApplyRunObservation, error) {
	if fleetEnvironment != c.fleetRoot() || bound == nil || bound.RunID <= 0 || bound.PlanRunID <= 0 || bound.PilotRunID != c.cfg.PilotRunID || !sha256Re.MatchString(bound.PlanSHA) || !commitSHARe.MatchString(bound.ApprovedHeadSHA) || !dispatchNonceRe.MatchString(nonce) || attempt < 0 {
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
	var run *applyRun
	if attempt == 0 {
		run, err = c.getApplyRun(ctx, token, bound.RunID)
	} else {
		var exact applyRun
		err = c.request(ctx, token, http.MethodGet, c.repoPath(fmt.Sprintf("/actions/runs/%d/attempts/%d", bound.RunID, attempt)), nil, &exact)
		run = &exact
	}
	if err != nil {
		return nil, err
	}
	if run.ID != bound.RunID || c.verifyApplyRun(run, planID, fleetEnvironment, bound, nonce, actor) != nil || verifyObservedApplyInputs(run, planID, fleetEnvironment, allowDestructive, bound, nonce) != nil || run.RunAttempt <= 0 || !validApplyRunObservationState(run.Status, run.Conclusion) {
		return nil, fmt.Errorf("GitHub apply run state or identity is inconsistent")
	}
	if attempt > 0 {
		if int64(run.RunAttempt) != attempt {
			return nil, fmt.Errorf("GitHub apply run attempt differs from protected attempt")
		}
		latest, latestErr := c.getApplyRun(ctx, token, bound.RunID)
		if latestErr != nil || c.verifyApplyRun(latest, planID, fleetEnvironment, bound, nonce, actor) != nil || verifyObservedApplyInputs(latest, planID, fleetEnvironment, allowDestructive, bound, nonce) != nil || int64(latest.RunAttempt) != attempt || latest.Status != run.Status || latest.Conclusion != run.Conclusion {
			return nil, fmt.Errorf("protected apply run has a newer or disagreeing attempt")
		}
	}
	return &ApplyRunObservation{RunID: run.ID, RunAttempt: int64(run.RunAttempt), Status: run.Status, Conclusion: run.Conclusion, ObservedAt: c.now().UTC()}, nil
}

func (c *Client) ObserveApplyRun(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, bound *Dispatch, nonce string) (*ApplyRunObservation, error) {
	return c.observeApplyRun(ctx, planID, fleetEnvironment, allowDestructive, bound, nonce, 0)
}

func (c *Client) ObserveApplyRunAttempt(ctx context.Context, planID, fleetEnvironment string, allowDestructive bool, bound *Dispatch, nonce string, attempt int64) (*ApplyRunObservation, error) {
	if attempt <= 0 {
		return nil, fmt.Errorf("protected apply run attempt is invalid")
	}
	return c.observeApplyRun(ctx, planID, fleetEnvironment, allowDestructive, bound, nonce, attempt)
}

func validApplyRunObservationState(status, conclusion string) bool {
	if status == "completed" {
		switch conclusion {
		case "success", "failure", "cancelled", "timed_out", "neutral", "skipped", "action_required", "stale", "startup_failure":
			return true
		}
		return false
	}
	if conclusion != "" {
		return false
	}
	switch status {
	case "queued", "in_progress", "requested", "waiting", "pending":
		return true
	}
	return false
}

// These canonical roots are the normal staging and production Fleet lanes.
// Disposable run-bound roots are handled separately by RunBoundFleetRoot.
func fleetEnvironmentFromConfigPath(configPath string) (string, error) {
	parts := strings.Split(path.Clean(configPath), "/")
	if len(parts) != 4 || parts[0] != "environments" || parts[3] != "cluster.yaml" || (parts[1] != "staging" && parts[1] != "production") || parts[2] != "nyc3" {
		return "", fmt.Errorf("Fleet config path is not a canonical root")
	}
	return parts[1] + "/" + parts[2], nil
}

func fleetEnvironmentFromDocument(document *fleet.Document) (string, error) {
	if document == nil || (document.Metadata.Environment != "staging" && document.Metadata.Environment != "production") || document.Cluster.Region != "nyc3" {
		return "", fmt.Errorf("Fleet document is not a canonical root")
	}
	return document.Metadata.Environment + "/" + document.Cluster.Region, nil
}

func fleetPlanArtifactName(fleetEnvironment string, runID int64) string {
	return fmt.Sprintf("fleet-plan-%s-%d", strings.ReplaceAll(fleetEnvironment, "/", "-"), runID)
}
