package handler

// The executor lane's start-time protected-branch re-check (plan-5-7 section
// 2.4, "Replacing ref_protected"). The GitHub lane enforces the OIDC claim
// ref_protected at attempt admission; the executor start instead re-runs
// VerifyProtectedCommit on the plan's commit, through a verifier E6 wires with
// a 60 s cache. It runs only for a fresh, non-resume start: a recovery resumes
// the plan it already holds, which may no longer be the branch tip, and a
// replay of an admitted attempt must not depend on GitHub.

import (
	"context"
	"net/http"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
)

// FleetExecutorStartVerifier re-verifies a plan's commit at attempt start.
type FleetExecutorStartVerifier interface {
	VerifyStartCommit(ctx context.Context, repository, commitSHA, lastApplied string) (*githubapp.CommitVerification, error)
}

// SetFleetExecutorStartVerifier wires the PG start-time re-check. Without it
// fresh executor starts are refused.
func (h *Handler) SetFleetExecutorStartVerifier(verifier FleetExecutorStartVerifier) {
	h.executorStartVerifier = verifier
}

// WithStartVerifier wires the etcd start-time re-check.
func (h *EtcdFleetRunnerHandler) WithStartVerifier(verifier FleetExecutorStartVerifier) *EtcdFleetRunnerHandler {
	h.startVerifier = verifier
	return h
}

// verifyFleetExecutorStart reports whether the start may proceed, writing the
// refusal itself otherwise. replay is true when the request's runnerAttemptId
// already names an attempt of the plan.
func verifyFleetExecutorStart(w http.ResponseWriter, r *http.Request, verifier FleetExecutorStartVerifier, converge executor.ConvergePlan, request executor.AttemptStartRequest, replay bool) bool {
	if request.Resume || replay {
		return true
	}
	if verifier == nil {
		// Fail closed: a fresh start without a wired protected-branch check
		// must never run.
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_executor_trigger_unavailable", "the protected branch could not be verified; retry")
		return false
	}
	verification, err := verifier.VerifyStartCommit(r.Context(), converge.Repository, converge.CommitSHA, converge.BaseCommitSHA)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_executor_trigger_unavailable", "the protected branch could not be verified; retry")
		return false
	}
	if verification.Verdict != githubapp.CommitVerified {
		code := executor.CodeFleetExecutorTriggerUnverified
		reason := verification.Reason
		if reason == "" {
			reason = "the plan's commit is not a verified protected-branch tip"
		}
		WriteControlProblem(w, r, executor.CodeStatus[code], code, reason)
		return false
	}
	return true
}
