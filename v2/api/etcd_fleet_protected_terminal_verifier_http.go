package main

// A deliberately narrow server-verified snapshot for the independent
// protected-run verifier. It projects Norn's durable terminal state without
// providing a portable proof, admitting a runner, releasing a fence, or
// opening any gate.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

const protectedTerminalVerifierSubject = "fleet-protected-terminal-verifier:work-staging"
const protectedTerminalVerifierRepository = "antiartificial/norn-fleet"

var protectedTerminalCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
var protectedTerminalPlanRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

type protectedTerminalReceiptStore interface {
	GetFleetRunnerTerminalReceipt(context.Context, string, string) (etcdstore.FleetRunnerTerminalReceipt, error)
}

func registerEtcdFleetProtectedTerminalVerifierRoute(router chi.Router, cfg *config.Config, identities *etcdstore.AuthStore, state protectedTerminalReceiptStore) {
	if cfg == nil || !cfg.FleetProtectedTerminalVerifierReadEnabled || cfg.FleetGitHubRepository != protectedTerminalVerifierRepository {
		return
	}
	auth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetProtectedTerminalVerify)
	router.With(offlineEffectNoStore, auth).Get("/api/v1/fleet/verifier/plans/{planID}/attempts/{attemptID}/terminal-receipt", etcdProtectedTerminalVerifierRead(state, protectedTerminalVerifierRepository))
}

func protectedTerminalVerifierPrincipal(r *http.Request) bool {
	p, ok := handler.AccessPrincipalFromRequest(r)
	return ok && p.Source == handler.AccessPrincipalSourceManagedToken && !p.Legacy && p.CI == nil &&
		p.DeviceID == "" && p.Subject == protectedTerminalVerifierSubject && p.TokenID != "" &&
		len(p.Scopes) == 1 && p.Scopes[0] == handler.ScopeFleetProtectedTerminalVerify &&
		!p.ExpiresAt.IsZero() && time.Now().Before(p.ExpiresAt)
}

func protectedTerminalRunMatches(runnerAttemptID, workflowURL, repository, runID, runAttempt string) bool {
	return repository != "" && runnerAttemptID == "github-actions:"+repository+":"+runID+":"+runAttempt &&
		workflowURL == "https://github.com/"+repository+"/actions/runs/"+runID
}

func etcdProtectedTerminalVerifierRead(state protectedTerminalReceiptStore, repository string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !protectedTerminalVerifierPrincipal(r) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "protected_terminal_verifier_required", "a dedicated protected terminal verifier token is required")
			return
		}
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		query := r.URL.Query()
		runID, runAttempt := query.Get("runId"), query.Get("runAttempt")
		sourceCommit, planSHA256 := query.Get("sourceCommit"), query.Get("planSha256")
		if r.ContentLength != 0 || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil ||
			len(query) != 4 || len(query["runId"]) != 1 || len(query["runAttempt"]) != 1 ||
			len(query["sourceCommit"]) != 1 || len(query["planSha256"]) != 1 ||
			!githubRunSelector(runID) || !githubRunSelector(runAttempt) || !protectedTerminalCommitRE.MatchString(sourceCommit) || !protectedTerminalPlanRE.MatchString(planSHA256) {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_protected_terminal_verifier_request", "exact run, run attempt, source commit, plan digest, plan and attempt are required")
			return
		}
		if state == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "protected_terminal_verifier_unavailable", "protected terminal receipt store is unavailable")
			return
		}
		receipt, err := state.GetFleetRunnerTerminalReceipt(r.Context(), planID, attemptID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "protected_terminal_unproven", "the exact protected attempt has no terminal reconciliation receipt")
			return
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "protected_terminal_verifier_unavailable", "terminal reconciliation receipt could not be verified")
			return
		}
		attempt, accepted := receipt.Attempt, receipt.Reconciliation
		terminal := receipt.ProtectedApplyTerminalIntent
		reconciliation, reconciliationErr := store.FleetReconciliationRequestFromOperation(accepted.Operation)
		predecessor := "postchange_verified"
		if attempt.PlanID != planID || attempt.ID != attemptID || attempt.Status != "succeeded" || attempt.CurrentPhase != "complete" || attempt.FinishedAt == nil ||
			attempt.CommitSHA != sourceCommit || attempt.PlanSHA256 != planSHA256 ||
			!protectedTerminalRunMatches(attempt.RunnerAttemptID, attempt.WorkflowURL, repository, runID, runAttempt) || accepted.Operation.ID == "" ||
			accepted.Operation.Kind != "fleet.reconciliation" || accepted.Operation.Ref != planID || accepted.AcceptanceIntentID == "" ||
			accepted.Intent.ID != accepted.AcceptanceIntentID || accepted.Intent.OperationID != accepted.Operation.ID || accepted.Intent.AcceptedAt.IsZero() ||
			accepted.Intent.CanonicalDigest == "" || terminal == nil || !terminal.Valid() ||
			terminal.PlanID != attempt.PlanID || terminal.AttemptID != attempt.ID || terminal.SourceCommit != attempt.CommitSHA || terminal.PlanSHA256 != attempt.PlanSHA256 ||
			reconciliationErr != nil || reconciliation.ProtectedApplyTerminalDigest != terminal.CanonicalDigest ||
			reconciliation.Status != "succeeded" || reconciliation.Phase != predecessor || reconciliation.AttemptID != attempt.ID ||
			reconciliation.CommitSHA != attempt.CommitSHA || reconciliation.PlanSHA256 != attempt.PlanSHA256 {
			handler.WriteControlProblem(w, r, http.StatusConflict, "protected_terminal_selector_mismatch", "stored terminal receipt does not match the exact protected runner selectors")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			SchemaVersion          string     `json:"schemaVersion"`
			Verification           string     `json:"verification"`
			ObservedAt             time.Time  `json:"observedAt"`
			PlanID                 string     `json:"planId"`
			AttemptID              string     `json:"attemptId"`
			GitHubRunID            string     `json:"githubRunId"`
			GitHubRunAttempt       string     `json:"githubRunAttempt"`
			SourceCommit           string     `json:"sourceCommit"`
			PlanSHA256             string     `json:"planSha256"`
			Status                 string     `json:"status"`
			CurrentPhase           string     `json:"currentPhase"`
			FinishedAt             *time.Time `json:"finishedAt"`
			TerminalReconciliation struct {
				OperationID                  string                                `json:"operationId"`
				OperationKind                string                                `json:"operationKind"`
				OperationRef                 string                                `json:"operationRef"`
				AcceptanceIntentID           string                                `json:"acceptanceIntentId"`
				AcceptedAt                   time.Time                             `json:"acceptedAt"`
				CanonicalDigest              string                                `json:"canonicalDigest"`
				Phase                        string                                `json:"phase"`
				Status                       string                                `json:"status"`
				AttemptID                    string                                `json:"attemptId"`
				CommitSHA                    string                                `json:"commitSha"`
				PlanSHA256                   string                                `json:"planSha256"`
				ApprovedPrestate             fleet.ProtectedApplyPrestate          `json:"approvedPrestate"`
				ApprovedTransitionRule       fleet.ProtectedApplyTransitionRule    `json:"approvedTransitionRule"`
				ObservedPoststate            fleet.ObservedProtectedApplyPoststate `json:"observedPoststate"`
				ProtectedApplyTerminalDigest string                                `json:"protectedApplyTerminalDigest"`
			} `json:"terminalReconciliation"`
		}{
			SchemaVersion: "norn.fleet-protected-terminal-verifier-snapshot/v1", Verification: "norn-server-verified", ObservedAt: time.Now().UTC(), PlanID: attempt.PlanID, AttemptID: attempt.ID,
			GitHubRunID: runID, GitHubRunAttempt: runAttempt, SourceCommit: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256,
			Status: attempt.Status, CurrentPhase: attempt.CurrentPhase, FinishedAt: attempt.FinishedAt,
			TerminalReconciliation: struct {
				OperationID                  string                                `json:"operationId"`
				OperationKind                string                                `json:"operationKind"`
				OperationRef                 string                                `json:"operationRef"`
				AcceptanceIntentID           string                                `json:"acceptanceIntentId"`
				AcceptedAt                   time.Time                             `json:"acceptedAt"`
				CanonicalDigest              string                                `json:"canonicalDigest"`
				Phase                        string                                `json:"phase"`
				Status                       string                                `json:"status"`
				AttemptID                    string                                `json:"attemptId"`
				CommitSHA                    string                                `json:"commitSha"`
				PlanSHA256                   string                                `json:"planSha256"`
				ApprovedPrestate             fleet.ProtectedApplyPrestate          `json:"approvedPrestate"`
				ApprovedTransitionRule       fleet.ProtectedApplyTransitionRule    `json:"approvedTransitionRule"`
				ObservedPoststate            fleet.ObservedProtectedApplyPoststate `json:"observedPoststate"`
				ProtectedApplyTerminalDigest string                                `json:"protectedApplyTerminalDigest"`
			}{accepted.Operation.ID, accepted.Operation.Kind, accepted.Operation.Ref, accepted.AcceptanceIntentID, accepted.Intent.AcceptedAt, accepted.Intent.CanonicalDigest, reconciliation.Phase, reconciliation.Status, reconciliation.AttemptID, reconciliation.CommitSHA, reconciliation.PlanSHA256, terminal.ApprovedPrestate, terminal.ApprovedTransitionRule, terminal.ObservedPoststate, terminal.CanonicalDigest},
		})
	}
}

func githubRunSelector(value string) bool {
	return len(value) <= 20 && value != "" && strings.TrimLeft(value, "0123456789") == "" && value[0] != '0'
}
