package handler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"norn/v2/api/config"
	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestFleetRunnerStartValidation(t *testing.T) {
	valid := fleet.RunnerAttemptStartRequest{
		SchemaVersion:   model.FleetRunnerAttemptSchemaVersion,
		RunnerAttemptID: "github-run-123", CommitSHA: strings.Repeat("a", 40),
		PlanSHA256: strings.Repeat("b", 64), WorkflowURL: "https://github.com/example/fleet/actions/runs/123",
		DispatchNonce: strings.Repeat("c", 64), SourceDispatchRunID: 123,
		HeartbeatTimeoutSeconds: 120,
	}
	if err := validateFleetRunnerStart(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.WorkflowURL = "https://token@example.test/run"
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("embedded workflow credentials were accepted")
	}
	invalid = valid
	invalid.WorkflowURL = "https://github.com/example/fleet/actions/runs/123?token=secret"
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("workflow URL query credentials were accepted")
	}
	invalid = valid
	invalid.HeartbeatTimeoutSeconds = 10
	if err := validateFleetRunnerStart(invalid); err == nil {
		t.Fatal("unsafe heartbeat timeout was accepted")
	}
}

func TestFleetRunnerTimingClassificationIsBoundAndConservative(t *testing.T) {
	valid := fleet.RunnerAttemptStartRequest{OperationClass: model.FleetTimingColdStart, CreatedNodeCount: 5}
	classification, err := fleetTimingClassificationFromRequest(valid)
	if err != nil || classification.OperationClass != model.FleetTimingColdStart || classification.CreatedNodeCount != 5 {
		t.Fatalf("classification=%#v err=%v", classification, err)
	}
	for _, count := range []int{0, 1000} {
		unknown, err := fleetTimingClassificationFromRequest(fleet.RunnerAttemptStartRequest{OperationClass: model.FleetTimingUnclassified, CreatedNodeCount: count})
		if err != nil || unknown.OperationClass != model.FleetTimingUnclassified || unknown.CreatedNodeCount != count {
			t.Fatalf("unknown classification count=%d classification=%#v err=%v", count, unknown, err)
		}
	}
	for _, request := range []fleet.RunnerAttemptStartRequest{
		{OperationClass: model.FleetTimingColdStart, CreatedNodeCount: 4},
		{OperationClass: "scale_out", CreatedNodeCount: 5},
		{CreatedNodeCount: 5},
		{OperationClass: model.FleetTimingUnclassified, CreatedNodeCount: -1},
		{OperationClass: model.FleetTimingUnclassified, CreatedNodeCount: 1001},
	} {
		if _, err := fleetTimingClassificationFromRequest(request); err == nil {
			t.Fatalf("unsafe timing classification accepted: %#v", request)
		}
	}
	metadata := fleetRunnerTimingMetadata(classification)
	attempt := &model.FleetRunnerAttempt{Metadata: metadata}
	if !fleetTimingClassificationEqual(classification, fleetTimingClassificationForAttempt(attempt)) {
		t.Fatalf("attempt did not preserve timing classification: %#v", attempt.Metadata)
	}
	// The same comparison protects both normal idempotent replays and the
	// unique-violation recovery paths: a concurrent candidate must never be
	// returned for a request carrying different immutable timing input.
	unknown := model.FleetTimingClassification{OperationClass: model.FleetTimingUnclassified, CreatedNodeCount: 0}
	if fleetTimingClassificationEqual(classification, unknown) {
		t.Fatal("conflicting concurrent timing classification was replay-compatible")
	}
	if !fleetTimingClassificationEqual(model.FleetTimingClassification{}, fleetTimingClassificationForAttempt(&model.FleetRunnerAttempt{})) {
		t.Fatal("absent classification became an inferred cold start")
	}
}

func TestFleetRunnerAttemptReplayRequiresAllImmutableBindings(t *testing.T) {
	request := fleet.RunnerAttemptStartRequest{
		RunnerAttemptID: "github-run-123", CommitSHA: strings.Repeat("a", 40), PlanSHA256: strings.Repeat("b", 64),
		SourceDispatchRunID: 123, WorkflowURL: "https://github.com/example/fleet/actions/runs/123", HeartbeatTimeoutSeconds: 120,
	}
	classification := model.FleetTimingClassification{OperationClass: model.FleetTimingColdStart, CreatedNodeCount: 5}
	attempt := &model.FleetRunnerAttempt{
		RunnerAttemptID: request.RunnerAttemptID, CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256,
		SourceDispatchRunID: request.SourceDispatchRunID, Recovery: request.Resume, WorkflowURL: request.WorkflowURL,
		HeartbeatTimeoutSeconds: request.HeartbeatTimeoutSeconds, Metadata: fleetRunnerTimingMetadata(classification),
	}
	if !fleetRunnerAttemptMatchesStartRequest(attempt, request, classification) {
		t.Fatal("matching durable attempt was not replay-compatible")
	}

	for name, mutate := range map[string]func(*model.FleetRunnerAttempt){
		"runner attempt ID": func(candidate *model.FleetRunnerAttempt) { candidate.RunnerAttemptID = "github-run-124" },
		"commit":            func(candidate *model.FleetRunnerAttempt) { candidate.CommitSHA = strings.Repeat("c", 40) },
		"plan":              func(candidate *model.FleetRunnerAttempt) { candidate.PlanSHA256 = strings.Repeat("d", 64) },
		"source dispatch":   func(candidate *model.FleetRunnerAttempt) { candidate.SourceDispatchRunID++ },
		"pilot run":         func(candidate *model.FleetRunnerAttempt) { candidate.PilotRunID = "pilot20260907" },
		"recovery":          func(candidate *model.FleetRunnerAttempt) { candidate.Recovery = true },
		"workflow URL": func(candidate *model.FleetRunnerAttempt) {
			candidate.WorkflowURL = "https://github.com/example/fleet/actions/runs/124"
		},
		"heartbeat timeout": func(candidate *model.FleetRunnerAttempt) { candidate.HeartbeatTimeoutSeconds++ },
		"timing classification": func(candidate *model.FleetRunnerAttempt) {
			candidate.Metadata = fleetRunnerTimingMetadata(model.FleetTimingClassification{OperationClass: model.FleetTimingUnclassified, CreatedNodeCount: 0})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *attempt
			mutate(&candidate)
			if fleetRunnerAttemptMatchesStartRequest(&candidate, request, classification) {
				t.Fatal("conflicting concurrent candidate was replay-compatible")
			}
		})
	}

	previousID := "previous-attempt"
	recoveryRequest := fleet.RunnerAttemptStartRequest{
		RunnerAttemptID: request.RunnerAttemptID, CommitSHA: request.CommitSHA, PlanSHA256: request.PlanSHA256,
		SourceDispatchRunID: request.SourceDispatchRunID, Resume: true, WorkflowURL: request.WorkflowURL, HeartbeatTimeoutSeconds: request.HeartbeatTimeoutSeconds,
	}
	recovery := *attempt
	recovery.Recovery = true
	if fleetRunnerRecoveryCandidateMatchesStartRequest(&recovery, previousID, recoveryRequest, classification) {
		t.Fatal("recovery candidate without the expected retry lineage was replay-compatible")
	}
	recovery.RetryOf = previousID
	if !fleetRunnerRecoveryCandidateMatchesStartRequest(&recovery, previousID, recoveryRequest, classification) {
		t.Fatal("matching recovery candidate was not replay-compatible")
	}
	recovery.RetryOf = "unrelated-attempt"
	if fleetRunnerRecoveryCandidateMatchesStartRequest(&recovery, previousID, recoveryRequest, classification) {
		t.Fatal("wrong recovery lineage was replay-compatible")
	}
}

func TestFleetRunnerStartRequiresExactDispatchAndOIDCProvenance(t *testing.T) {
	nonce := strings.Repeat("c", 64)
	commit := strings.Repeat("a", 40)
	planSHA := strings.Repeat("b", 64)
	cfg := &config.Config{GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202"}
	applyCI := &CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RunID: "93", RunAttempt: "1", Environment: "production", SHA: commit, RefProtected: true, Intent: "apply"}
	principal := AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: applyCI}
	request := fleet.RunnerAttemptStartRequest{RunnerAttemptID: canonicalFleetRunnerAttemptID(applyCI), CommitSHA: commit, PlanSHA256: planSHA, DispatchNonce: nonce, SourceDispatchRunID: 93, WorkflowURL: canonicalFleetWorkflowURL(applyCI)}
	if request.RunnerAttemptID != "github-actions:acme/norn-fleet:93:1" {
		t.Fatalf("Fleet workflow runner identity must use the canonical github-actions prefix, got %q", request.RunnerAttemptID)
	}
	binding := store.FleetGitHubDispatch{PlanID: "plan-1", PlanRunID: 91, PlanSHA256: planSHA, ApprovedHeadSHA: commit, FleetEnvironment: "production/nyc3", DispatchNonceSHA256: fleetDispatchNonceHash(nonce), RunID: 93, RunAttempt: 1}
	if err := validateFleetRunnerDispatchBinding(cfg, principal, "plan-1", request, binding); err != nil {
		t.Fatalf("bound apply rejected: %v", err)
	}
	for name, mutate := range map[string]func(*fleet.RunnerAttemptStartRequest, *CIIdentity, *store.FleetGitHubDispatch){
		"wrong nonce": func(r *fleet.RunnerAttemptStartRequest, _ *CIIdentity, _ *store.FleetGitHubDispatch) {
			r.DispatchNonce = strings.Repeat("d", 64)
		},
		"wrong source": func(r *fleet.RunnerAttemptStartRequest, _ *CIIdentity, _ *store.FleetGitHubDispatch) {
			r.SourceDispatchRunID = 94
		},
		"wrong commit": func(r *fleet.RunnerAttemptStartRequest, _ *CIIdentity, _ *store.FleetGitHubDispatch) {
			r.CommitSHA = strings.Repeat("d", 40)
		},
		"wrong plan": func(r *fleet.RunnerAttemptStartRequest, _ *CIIdentity, _ *store.FleetGitHubDispatch) {
			r.PlanSHA256 = strings.Repeat("d", 64)
		},
		"wrong environment": func(_ *fleet.RunnerAttemptStartRequest, ci *CIIdentity, _ *store.FleetGitHubDispatch) {
			ci.Environment = "staging"
		},
		"pre-Finish pending dispatch": func(_ *fleet.RunnerAttemptStartRequest, _ *CIIdentity, b *store.FleetGitHubDispatch) {
			b.RunID = 0
		},
		"independent claimant": func(r *fleet.RunnerAttemptStartRequest, ci *CIIdentity, _ *store.FleetGitHubDispatch) {
			ci.RunID = "94"
			r.RunnerAttemptID = canonicalFleetRunnerAttemptID(ci)
			r.WorkflowURL = canonicalFleetWorkflowURL(ci)
		},
		"stale GitHub run attempt": func(r *fleet.RunnerAttemptStartRequest, ci *CIIdentity, _ *store.FleetGitHubDispatch) {
			ci.RunAttempt = "2"
			r.RunnerAttemptID, r.WorkflowURL = canonicalFleetRunnerAttemptID(ci), canonicalFleetWorkflowURL(ci)
		},
		"malformed GitHub run attempt": func(r *fleet.RunnerAttemptStartRequest, ci *CIIdentity, _ *store.FleetGitHubDispatch) {
			ci.RunAttempt = "not-a-number"
			r.RunnerAttemptID, r.WorkflowURL = canonicalFleetRunnerAttemptID(ci), canonicalFleetWorkflowURL(ci)
		},
		"missing durable GitHub run attempt": func(_ *fleet.RunnerAttemptStartRequest, _ *CIIdentity, b *store.FleetGitHubDispatch) {
			b.RunAttempt = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			ci := *applyCI
			b := binding
			mutate(&r, &ci, &b)
			if err := validateFleetRunnerDispatchBinding(cfg, AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: &ci}, "plan-1", r, b); err == nil {
				t.Fatal("mismatched protected provenance was accepted")
			}
		})
	}
	recoveryCI := *applyCI
	recoveryCI.RunID, recoveryCI.RunAttempt, recoveryCI.Intent = "104", "2", "recover"
	// Recovery is intentionally allowed from a later protected main commit.
	// The reviewed commit and plan remain pinned by the request and binding.
	recoveryCI.SHA = strings.Repeat("d", 40)
	recovery := request
	recovery.Resume = true
	recovery.RunnerAttemptID, recovery.WorkflowURL = canonicalFleetRunnerAttemptID(&recoveryCI), canonicalFleetWorkflowURL(&recoveryCI)
	binding.RunID = 93
	if err := validateFleetRunnerDispatchBinding(cfg, AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: &recoveryCI}, "plan-1", recovery, binding); err != nil {
		t.Fatalf("bound recovery rejected: %v", err)
	}
}

func TestDisposableFleetRunnerStartRequiresExactCurrentPilotRun(t *testing.T) {
	nonce := strings.Repeat("c", 64)
	commit := strings.Repeat("a", 40)
	planSHA := strings.Repeat("b", 64)
	pilotA, pilotB := "pilot20260907", "pilot20260908"
	cfg := &config.Config{GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", FleetGitHubPilotRunID: pilotA}
	ci := &CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RunID: "93", RunAttempt: "1", Environment: "staging", SHA: commit, RefProtected: true, Intent: "apply"}
	principal := AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: ci}
	request := fleet.RunnerAttemptStartRequest{RunnerAttemptID: canonicalFleetRunnerAttemptID(ci), CommitSHA: commit, PlanSHA256: planSHA, DispatchNonce: nonce, SourceDispatchRunID: 93, PilotRunID: pilotA, WorkflowURL: canonicalFleetWorkflowURL(ci)}
	binding := store.FleetGitHubDispatch{PlanID: "plan-1", PlanRunID: 91, PlanSHA256: planSHA, ApprovedHeadSHA: commit, PilotRunID: pilotA, FleetEnvironment: "disposable/fleet/nyc3", DispatchNonceSHA256: fleetDispatchNonceHash(nonce), RunID: 93, RunAttempt: 1}
	if err := validateFleetRunnerDispatchBinding(cfg, principal, "plan-1", request, binding); err != nil {
		t.Fatalf("exact disposable pilot binding rejected: %v", err)
	}
	for name, mutate := range map[string]func(*fleet.RunnerAttemptStartRequest, *store.FleetGitHubDispatch, *config.Config){
		"request names new authority": func(r *fleet.RunnerAttemptStartRequest, _ *store.FleetGitHubDispatch, _ *config.Config) {
			r.PilotRunID = pilotB
		},
		"dispatch names new authority": func(_ *fleet.RunnerAttemptStartRequest, b *store.FleetGitHubDispatch, _ *config.Config) {
			b.PilotRunID = pilotB
		},
		"authority moved to B": func(_ *fleet.RunnerAttemptStartRequest, _ *store.FleetGitHubDispatch, c *config.Config) {
			c.FleetGitHubPilotRunID = pilotB
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, b, c := request, binding, *cfg
			mutate(&r, &b, &c)
			if err := validateFleetRunnerDispatchBinding(&c, principal, "plan-1", r, b); err == nil {
				t.Fatal("stale or mismatched disposable pilot run was accepted")
			}
		})
	}
	ordinaryBinding := binding
	ordinaryBinding.FleetEnvironment, ordinaryBinding.PilotRunID = "production/nyc3", ""
	ordinaryRequest := request
	ordinaryRequest.PilotRunID = ""
	ordinaryCfg := *cfg
	ordinaryCfg.FleetGitHubPilotRunID = ""
	ordinaryCI := *ci
	ordinaryCI.Environment = "production"
	ordinaryRequest.RunnerAttemptID, ordinaryRequest.WorkflowURL = canonicalFleetRunnerAttemptID(&ordinaryCI), canonicalFleetWorkflowURL(&ordinaryCI)
	if err := validateFleetRunnerDispatchBinding(&ordinaryCfg, AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: &ordinaryCI}, "plan-1", ordinaryRequest, ordinaryBinding); err != nil {
		t.Fatalf("ordinary all-empty pilot binding rejected: %v", err)
	}
	ordinaryRequest.PilotRunID = pilotA
	if err := validateFleetRunnerDispatchBinding(&ordinaryCfg, AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: &ordinaryCI}, "plan-1", ordinaryRequest, ordinaryBinding); err == nil {
		t.Fatal("ordinary lane accepted a pilot run")
	}
}

func TestExternalMacRunnerUsesStagingPilotBindingAndExactConsumedAuthority(t *testing.T) {
	nonce := strings.Repeat("c", 64)
	commit := strings.Repeat("a", 40)
	planSHA := strings.Repeat("b", 64)
	approval, receipt := strings.Repeat("d", 64), strings.Repeat("e", 64)
	pilot := "pilot20260907"
	cfg := &config.Config{GitHubActionsFleetAllowedRepository: "acme/norn-fleet@101@202", FleetGitHubPilotRunID: pilot}
	ci := &CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RunID: "93", RunAttempt: "1", Environment: "staging", SHA: commit, RefProtected: true, Intent: "apply"}
	request := fleet.RunnerAttemptStartRequest{RunnerAttemptID: canonicalFleetRunnerAttemptID(ci), CommitSHA: commit, PlanSHA256: planSHA, DispatchNonce: nonce, SourceDispatchRunID: 93, PilotRunID: pilot, WorkflowURL: canonicalFleetWorkflowURL(ci), ApprovalEnvelopeSHA256: approval, ConsumptionReceiptSHA256: receipt}
	binding := store.FleetGitHubDispatch{PlanID: "plan-1", PlanRunID: 91, PlanSHA256: planSHA, ApprovedHeadSHA: commit, PilotRunID: pilot, FleetEnvironment: "disposable/external-mac/nyc3", DispatchNonceSHA256: fleetDispatchNonceHash(nonce), ApprovalEnvelopeSHA256: approval, RunID: 93, RunAttempt: 1}
	if err := validateFleetRunnerDispatchBinding(cfg, AccessPrincipal{Scopes: []string{ScopeFleetOperate}, CI: ci}, "plan-1", request, binding); err != nil {
		t.Fatalf("exact external-Mac staging binding rejected: %v", err)
	}
	if fleetControlEnvironment("disposable/external-mac/nyc3") != "staging" {
		t.Fatal("external-Mac lane did not map to protected staging environment")
	}
	previous := &model.FleetRunnerAttempt{Metadata: mergeFleetRunnerMetadata(map[string]interface{}{"authorityConsumption": fleetRunnerAuthorityMetadata(request)})}
	if !authorityMetadataMatches(previous, request) {
		t.Fatal("exact consumed authority did not match original attempt")
	}
	for name, mutate := range map[string]func(*fleet.RunnerAttemptStartRequest){
		"approval": func(value *fleet.RunnerAttemptStartRequest) { value.ApprovalEnvelopeSHA256 = strings.Repeat("f", 64) },
		"receipt":  func(value *fleet.RunnerAttemptStartRequest) { value.ConsumptionReceiptSHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate)
			if authorityMetadataMatches(previous, candidate) {
				t.Fatal("recovery accepted a substituted authority-consumption digest")
			}
		})
	}
}

func TestFleetRunnerAttemptNeverSerializesDispatchNonce(t *testing.T) {
	attempt := model.FleetRunnerAttempt{SourceDispatchRunID: 93}
	encoded, err := json.Marshal(attempt)
	if err != nil || strings.Contains(string(encoded), "dispatchNonce") {
		t.Fatalf("runner attempt leaked write-only nonce: %s, %v", encoded, err)
	}
	if _, found := reflect.TypeOf(store.FleetGitHubDispatch{}).FieldByName("DispatchNonce"); found {
		t.Fatal("durable Fleet dispatch store retained the raw nonce")
	}
}

func TestFleetRunnerMutationRequiresBoundCurrentGitHubRun(t *testing.T) {
	ci := &CIIdentity{Provider: "github-actions", Repository: "acme/norn-fleet", RunID: "93", RunAttempt: "1"}
	attempt := &model.FleetRunnerAttempt{
		RunnerAttemptID: canonicalFleetRunnerAttemptID(ci), WorkflowURL: canonicalFleetWorkflowURL(ci), PrincipalSubject: "runner-93",
	}
	bound := AccessPrincipal{Subject: "runner-93", Scopes: []string{ScopeFleetOperate}, CI: ci}
	if !fleetRunnerPrincipalOwnsAttempt(bound, attempt) {
		t.Fatal("bound Fleet runner was rejected")
	}
	if !fleetRunnerCanReadAttempt(bound, attempt) {
		t.Fatal("bound Fleet runner could not read its own attempt")
	}
	otherCI := *ci
	otherCI.RunID = "94"
	other := AccessPrincipal{Subject: "runner-94", Scopes: []string{ScopeFleetOperate}, CI: &otherCI}
	if fleetRunnerPrincipalOwnsAttempt(other, attempt) {
		t.Fatal("different trusted GitHub Actions run could mutate another attempt")
	}
	if fleetRunnerCanReadAttempt(other, attempt) {
		t.Fatal("different trusted GitHub Actions run could read another attempt")
	}
	compatibility := AccessPrincipal{Subject: "runner-93", Scopes: []string{ScopeAPIWrite}, CI: ci}
	if fleetRunnerPrincipalOwnsAttempt(compatibility, attempt) {
		t.Fatal("temporary api:write compatibility could mutate a runner-owned attempt")
	}
	if fleetRunnerCanReadAttempt(compatibility, attempt) {
		t.Fatal("temporary api:write compatibility could read a runner-owned attempt")
	}
	if !fleetRunnerCanReadAttempt(AccessPrincipal{Scopes: []string{ScopeAPIRead}}, attempt) {
		t.Fatal("api:read could not retain ordinary attempt reads")
	}
}

func TestFleetRunnerPhaseAdvanceIsOrderedAndTerminal(t *testing.T) {
	for index, phase := range fleetReconciliationPhases {
		next, terminal, err := nextFleetRunnerPhase(phase, true)
		if err != nil {
			t.Fatal(err)
		}
		if index == len(fleetReconciliationPhases)-1 {
			if !terminal || next != "complete" {
				t.Fatalf("terminal advance = %q, %v", next, terminal)
			}
			continue
		}
		if terminal || next != fleetReconciliationPhases[index+1] {
			t.Fatalf("%s advance = %q, %v", phase, next, terminal)
		}
	}
	if _, _, err := nextFleetRunnerPhase("invented", true); err == nil {
		t.Fatal("unknown phase was accepted")
	}
	next, terminal, err := nextFleetRunnerPhase("readiness_verified", false)
	if err != nil || terminal || next != "complete" {
		t.Fatalf("non-drain advance = %q, %v, %v", next, terminal, err)
	}
	if _, _, err := nextFleetRunnerPhase("old_nodes_drained", false); err == nil {
		t.Fatal("non-drain plan accepted a drain phase")
	}
	if next, _, err := nextFleetRunnerPhase("prechange_verified", true); err != nil || next != "provider_applying" {
		t.Fatalf("destructive preflight transition = %q, %v", next, err)
	}
	if _, _, err := nextFleetRunnerPhase("prechange_verified", false); err == nil {
		t.Fatal("non-destructive plan accepted a destructive preflight phase")
	}
}

func TestFleetRunnerResumesAtFirstIncompletePhase(t *testing.T) {
	plan := &model.Operation{Payload: map[string]interface{}{"action": "scale", "current": map[string]interface{}{"desired": 3.0}, "proposed": map[string]interface{}{"desired": 2.0}}}
	attemptID := "11111111-1111-4111-8111-111111111111"
	checkpoints := []model.Operation{
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"attemptId": attemptID, "phase": "prechange_verified"}},
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"attemptId": attemptID, "phase": "provider_applying"}},
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"attemptId": attemptID, "phase": "infrastructure_applied"}},
		{Status: model.OperationSucceeded, Payload: map[string]interface{}{"attemptId": attemptID, "phase": "inventory_generated"}},
		{Status: model.OperationFailed, Payload: map[string]interface{}{"attemptId": attemptID, "phase": "nodes_configured"}},
	}
	phase, complete := firstIncompleteFleetRunnerPhase(plan, checkpoints, attemptID)
	if complete || phase != "nodes_configured" {
		t.Fatalf("resume phase = %q, complete=%v", phase, complete)
	}
	for _, phase := range fleetRunnerPhases(true)[4:] {
		checkpoints = append(checkpoints, model.Operation{Status: model.OperationSucceeded, Payload: map[string]interface{}{"attemptId": attemptID, "phase": phase}})
	}
	phase, complete = firstIncompleteFleetRunnerPhase(plan, checkpoints, attemptID)
	if !complete || phase != "complete" {
		t.Fatalf("completed phase = %q, complete=%v", phase, complete)
	}
}

func TestLegacyUnboundReconciliationCannotSkipProtectedRunnerPhase(t *testing.T) {
	plan := &model.Operation{Payload: map[string]interface{}{"action": "replace", "current": map[string]interface{}{"desired": 2.0}, "proposed": map[string]interface{}{"desired": 2.0}}}
	legacy := []model.Operation{}
	for _, phase := range fleetRunnerPhases(true) {
		legacy = append(legacy, model.Operation{Status: model.OperationSucceeded, Payload: map[string]interface{}{"phase": phase}})
	}
	phase, complete := firstIncompleteFleetRunnerPhase(plan, legacy, "22222222-2222-4222-8222-222222222222")
	if complete || phase != "prechange_verified" {
		t.Fatalf("legacy unbound evidence skipped protected phase: %q, complete=%v", phase, complete)
	}
}
