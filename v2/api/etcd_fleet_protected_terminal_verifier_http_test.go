package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type protectedTerminalReceiptStoreFake struct {
	receipt etcdstore.FleetRunnerTerminalReceipt
	err     error
	reads   int
}

func (s *protectedTerminalReceiptStoreFake) GetFleetRunnerTerminalReceipt(context.Context, string, string) (etcdstore.FleetRunnerTerminalReceipt, error) {
	s.reads++
	return s.receipt, s.err
}

func protectedTerminalFixture() *protectedTerminalReceiptStoreFake {
	planID, attemptID := uuid.NewString(), uuid.NewString()
	operationID := uuid.NewString()
	intentID := uuid.NewString()
	acceptedAt := time.Now().UTC().Add(-time.Minute)
	finishedAt := acceptedAt
	commitSHA, planSHA256 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	prestate := &fleet.ProtectedApplyPrestate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: fleet.ProtectedApplyStateSpaces{Bucket: "bucket", Key: "state/key", Lineage: "lineage", Serial: 1, VersionID: "version", RawSHA256: strings.Repeat("d", 64)}}
	rule := &fleet.ProtectedApplyTransitionRule{ProviderTargetState: "present", StateChange: "write"}
	observed := &fleet.ObservedProtectedApplyPoststate{ProviderAccountUUID: "account", ProviderTargetID: "target", ProviderTargetState: "present", Spaces: fleet.ProtectedApplyStateSpaces{Bucket: "bucket", Key: "state/key", Lineage: "lineage", Serial: 2, VersionID: "version-2", RawSHA256: strings.Repeat("f", 64)}}
	terminal, err := fleet.BuildProtectedApplyTerminalIntent(prestate, rule, observed, planID, attemptID, commitSHA, planSHA256)
	if err != nil {
		panic(err)
	}
	reconciliation := fleet.ReconciliationRequest{SchemaVersion: fleet.ReconciliationSchemaVersion, Phase: "postchange_verified", Status: "succeeded", AttemptID: attemptID, CommitSHA: commitSHA, PlanSHA256: planSHA256, EvidenceDigest: "sha256:" + strings.Repeat("e", 64), ObservedProtectedApplyPoststate: observed, ProtectedApplyTerminalDigest: terminal.CanonicalDigest}
	return &protectedTerminalReceiptStoreFake{receipt: etcdstore.FleetRunnerTerminalReceipt{
		Attempt:                      fleet.RunnerAttempt{ID: attemptID, PlanID: planID, RunnerAttemptID: "github-actions:antiartificial/norn-fleet:7:1", WorkflowURL: "https://github.com/antiartificial/norn-fleet/actions/runs/7", CommitSHA: commitSHA, PlanSHA256: planSHA256, Status: "succeeded", CurrentPhase: "complete", FinishedAt: &finishedAt},
		Reconciliation:               store.AcceptedOperation{Operation: model.Operation{ID: operationID, Kind: "fleet.reconciliation", Ref: planID, Payload: map[string]interface{}{"schemaVersion": reconciliation.SchemaVersion, "phase": reconciliation.Phase, "status": reconciliation.Status, "attemptId": reconciliation.AttemptID, "commitSha": reconciliation.CommitSHA, "planSha256": reconciliation.PlanSHA256, "evidenceDigest": reconciliation.EvidenceDigest, "observedProtectedApplyPoststate": reconciliation.ObservedProtectedApplyPoststate, "protectedApplyTerminalDigest": reconciliation.ProtectedApplyTerminalDigest}}, AcceptanceIntentID: intentID, Intent: store.SignedAcceptanceIntent{ID: intentID, OperationID: operationID, AcceptedAt: acceptedAt, CanonicalDigest: "sha256:" + strings.Repeat("c", 64)}},
		ProtectedApplyTerminalIntent: &terminal,
	}}
}

func protectedTerminalPrincipal() handler.AccessPrincipal {
	return handler.AccessPrincipal{Subject: protectedTerminalVerifierSubject, TokenID: "verifier", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetProtectedTerminalVerify}, ExpiresAt: time.Now().Add(time.Hour)}
}

func protectedTerminalRequest(s *protectedTerminalReceiptStoreFake, p handler.AccessPrincipal, path string, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.With(offlineEffectNoStore).Get("/api/v1/fleet/verifier/plans/{planID}/attempts/{attemptID}/terminal-receipt", etcdProtectedTerminalVerifierRead(s, protectedTerminalVerifierRepository))
	request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, strings.NewReader(body)), &p)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}

func protectedTerminalPath(s *protectedTerminalReceiptStoreFake) string {
	a := s.receipt.Attempt
	return "/api/v1/fleet/verifier/plans/" + a.PlanID + "/attempts/" + a.ID + "/terminal-receipt?runId=7&runAttempt=1&sourceCommit=" + a.CommitSHA + "&planSha256=" + a.PlanSHA256
}

func protectedTerminalRebind(t *testing.T, intent *fleet.ProtectedApplyTerminalIntent) {
	t.Helper()
	rebuilt, err := fleet.BuildProtectedApplyTerminalIntent(&intent.ApprovedPrestate, &intent.ApprovedTransitionRule, &intent.ObservedPoststate, intent.PlanID, intent.AttemptID, intent.SourceCommit, intent.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	*intent = rebuilt
}

func TestProtectedTerminalVerifierReadIsExactNoStoreAndReceiptOnly(t *testing.T) {
	s := protectedTerminalFixture()
	// GetFleetRunnerTerminalReceipt already performs the private uncapped scan:
	// this route intentionally reads only its verified result once.
	response := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || s.reads != 1 {
		t.Fatalf("read=%d headers=%v reads=%d body=%s", response.Code, response.Header(), s.reads, response.Body)
	}
	var result struct {
		PlanID                 string `json:"planId"`
		AttemptID              string `json:"attemptId"`
		GitHubRunID            string `json:"githubRunId"`
		GitHubRunAttempt       string `json:"githubRunAttempt"`
		SourceCommit           string `json:"sourceCommit"`
		PlanSHA256             string `json:"planSha256"`
		Verification           string `json:"verification"`
		TerminalReconciliation struct {
			OperationID                  string                                `json:"operationId"`
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
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	a := s.receipt.Attempt
	if result.Verification != "norn-server-verified" || result.PlanID != a.PlanID || result.AttemptID != a.ID || result.GitHubRunID != "7" || result.GitHubRunAttempt != "1" || result.SourceCommit != a.CommitSHA || result.PlanSHA256 != a.PlanSHA256 || result.TerminalReconciliation.OperationID != s.receipt.Reconciliation.Operation.ID || result.TerminalReconciliation.Status != "succeeded" || result.TerminalReconciliation.AttemptID != a.ID || result.TerminalReconciliation.CommitSHA != a.CommitSHA || result.TerminalReconciliation.PlanSHA256 != a.PlanSHA256 || !result.TerminalReconciliation.ApprovedPrestate.Valid() || !result.TerminalReconciliation.ApprovedTransitionRule.Valid() || !result.TerminalReconciliation.ObservedPoststate.Valid() || result.TerminalReconciliation.ProtectedApplyTerminalDigest != s.receipt.ProtectedApplyTerminalIntent.CanonicalDigest || strings.Contains(response.Body.String(), "signature") || strings.Contains(response.Body.String(), a.RunnerAttemptID) {
		t.Fatalf("unexpected verifier projection: %s", response.Body)
	}
}

func TestProtectedTerminalVerifierReadRejectsWrongPrincipalBeforeStoreRead(t *testing.T) {
	s := protectedTerminalFixture()
	for name, principal := range map[string]handler.AccessPrincipal{
		"runner":        {Subject: "runner", TokenID: "r", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetOperate}, ExpiresAt: time.Now().Add(time.Hour)},
		"admin":         {Subject: "admin", TokenID: "a", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		"mixed":         {Subject: protectedTerminalVerifierSubject, TokenID: "m", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetProtectedTerminalVerify, handler.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		"wrong subject": {Subject: "other", TokenID: "o", Source: handler.AccessPrincipalSourceManagedToken, Scopes: []string{handler.ScopeFleetProtectedTerminalVerify}, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			before := s.reads
			if got := protectedTerminalRequest(s, principal, protectedTerminalPath(s), ""); got.Code != http.StatusForbidden || s.reads != before {
				t.Fatalf("code=%d reads=%d body=%s", got.Code, s.reads-before, got.Body)
			}
		})
	}
}

func TestProtectedTerminalVerifierReadRejectsNonterminalAndWrongSelectors(t *testing.T) {
	s := protectedTerminalFixture()
	s.err = etcdstore.ErrNotFound
	if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "protected_terminal_unproven") {
		t.Fatalf("nonterminal=%d %s", got.Code, got.Body)
	}
	s.err = nil
	for _, replacement := range []string{
		"runId=8", "runAttempt=2", "sourceCommit=" + strings.Repeat("c", 40), "planSha256=" + strings.Repeat("d", 64),
	} {
		path := protectedTerminalPath(s)
		key := strings.SplitN(replacement, "=", 2)[0]
		start := strings.Index(path, key+"=")
		end := strings.Index(path[start:], "&")
		if end < 0 {
			end = len(path) - start
		}
		path = path[:start] + replacement + path[start+end:]
		if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), path, ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "protected_terminal_selector_mismatch") {
			t.Fatalf("selector %q: %d %s", replacement, got.Code, got.Body)
		}
	}
	if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), "client-assertion"); got.Code != http.StatusBadRequest {
		t.Fatalf("body accepted: %d %s", got.Code, got.Body)
	}
	if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s)+"&runId=7", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query accepted: %d %s", got.Code, got.Body)
	}
}

func TestProtectedTerminalVerifierReadRejectsInconsistentStoredTerminalReceipt(t *testing.T) {
	for name, mutate := range map[string]func(*etcdstore.FleetRunnerTerminalReceipt){
		"wrong repository": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Attempt.RunnerAttemptID = "github-actions:other/fleet:7:1"
		},
		"wrong workflow": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Attempt.WorkflowURL = "https://github.com/other/fleet/actions/runs/7"
		},
		"missing finished": func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Attempt.FinishedAt = nil },
		"wrong status":     func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Attempt.Status = "running" },
		"wrong phase":      func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Attempt.CurrentPhase = "readiness_verified" },
		"missing terminal intent": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := protectedTerminalFixture()
			mutate(&s.receipt)
			if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "protected_terminal_selector_mismatch") {
				t.Fatalf("inconsistent receipt accepted: %d %s", got.Code, got.Body)
			}
		})
	}
}

func TestProtectedTerminalVerifierReadRejectsEveryProjectedReconciliationBindingMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*etcdstore.FleetRunnerTerminalReceipt){
		"operation id":         func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Operation.ID = uuid.NewString() },
		"operation kind":       func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Operation.Kind = "other" },
		"operation ref":        func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Operation.Ref = uuid.NewString() },
		"acceptance intent id": func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.AcceptanceIntentID = uuid.NewString() },
		"accepted at":          func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Intent.AcceptedAt = time.Time{} },
		"canonical digest":     func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Intent.CanonicalDigest = "" },
		"phase": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Reconciliation.Operation.Payload["phase"] = "prechange_verified"
		},
		"status": func(r *etcdstore.FleetRunnerTerminalReceipt) { r.Reconciliation.Operation.Payload["status"] = "failed" },
		"attempt id": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Reconciliation.Operation.Payload["attemptId"] = uuid.NewString()
		},
		"commit": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Reconciliation.Operation.Payload["commitSha"] = strings.Repeat("c", 40)
		},
		"plan": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Reconciliation.Operation.Payload["planSha256"] = strings.Repeat("d", 64)
		},
		"terminal plan": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent.PlanID = uuid.NewString()
			protectedTerminalRebind(t, r.ProtectedApplyTerminalIntent)
		},
		"terminal attempt": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent.AttemptID = uuid.NewString()
			protectedTerminalRebind(t, r.ProtectedApplyTerminalIntent)
		},
		"terminal commit": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent.SourceCommit = strings.Repeat("c", 40)
			protectedTerminalRebind(t, r.ProtectedApplyTerminalIntent)
		},
		"terminal plan sha": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent.PlanSHA256 = strings.Repeat("d", 64)
			protectedTerminalRebind(t, r.ProtectedApplyTerminalIntent)
		},
		"terminal transition rule": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.ProtectedApplyTerminalIntent.ApprovedTransitionRule.StateChange = "no-op"
		},
		"terminal digest": func(r *etcdstore.FleetRunnerTerminalReceipt) {
			r.Reconciliation.Operation.Payload["protectedApplyTerminalDigest"] = "sha256:" + strings.Repeat("0", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := protectedTerminalFixture()
			mutate(&s.receipt)
			if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "protected_terminal_selector_mismatch") {
				t.Fatalf("binding mismatch accepted: %d %s", got.Code, got.Body)
			}
		})
	}
}

func TestProtectedTerminalVerifierReadRejectsChunkedGETBody(t *testing.T) {
	s := protectedTerminalFixture()
	router := chi.NewRouter()
	router.With(offlineEffectNoStore).Get("/api/v1/fleet/verifier/plans/{planID}/attempts/{attemptID}/terminal-receipt", etcdProtectedTerminalVerifierRead(s, protectedTerminalVerifierRepository))
	request := handler.WithAccessPrincipal(httptest.NewRequest(http.MethodGet, protectedTerminalPath(s), nil), ptrPrincipal(protectedTerminalPrincipal()))
	request.ContentLength = -1
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_protected_terminal_verifier_request") {
		t.Fatalf("chunked GET body accepted: %d %s", rec.Code, rec.Body)
	}
}

func ptrPrincipal(p handler.AccessPrincipal) *handler.AccessPrincipal { return &p }

func TestProtectedTerminalVerifierRouteIsDisabledByDefault(t *testing.T) {
	router := chi.NewRouter()
	registerEtcdFleetProtectedTerminalVerifierRoute(router, &config.Config{}, nil, protectedTerminalFixture())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/verifier/plans/"+uuid.NewString()+"/attempts/"+uuid.NewString()+"/terminal-receipt", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("default-off verifier route=%d body=%s", rec.Code, rec.Body)
	}
	router = chi.NewRouter()
	registerEtcdFleetProtectedTerminalVerifierRoute(router, &config.Config{FleetProtectedTerminalVerifierReadEnabled: true, FleetGitHubRepository: "other/fleet"}, nil, protectedTerminalFixture())
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/verifier/plans/"+uuid.NewString()+"/attempts/"+uuid.NewString()+"/terminal-receipt", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong-repository verifier route=%d body=%s", rec.Code, rec.Body)
	}
}

func TestProtectedTerminalVerifierCapabilityIsExplicit(t *testing.T) {
	capabilities := etcdFleetCapabilities(&config.Config{}, false)
	features := capabilities["features"].([]string)
	for _, feature := range features {
		if feature == "fleet-protected-terminal-verifier-read-v1" {
			t.Fatal("default capabilities advertised protected terminal verifier")
		}
	}
	if _, found := capabilities["endpoints"].(map[string]string)["fleetProtectedTerminalVerifierRead"]; found {
		t.Fatal("default capabilities exposed protected terminal verifier endpoint")
	}
	capabilities = etcdFleetCapabilities(&config.Config{FleetProtectedTerminalVerifierReadEnabled: true, FleetGitHubRepository: protectedTerminalVerifierRepository}, false, true)
	if got := capabilities["endpoints"].(map[string]string)["fleetProtectedTerminalVerifierRead"]; got != "/api/v1/fleet/verifier/plans/{planID}/attempts/{attemptID}/terminal-receipt" {
		t.Fatalf("protected terminal verifier endpoint=%q", got)
	}
}

func TestProtectedTerminalVerifierReadReturnsUnavailableForStoreFailure(t *testing.T) {
	s := protectedTerminalFixture()
	s.err = errors.New("etcd unavailable")
	if got := protectedTerminalRequest(s, protectedTerminalPrincipal(), protectedTerminalPath(s), ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable=%d %s", got.Code, got.Body)
	}
}
