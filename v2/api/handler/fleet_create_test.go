package handler

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

	"norn/v2/api/fleet/fleetcreate"
)

type fleetCreateHTTPStore struct {
	intent    fleetcreate.Intent
	binding   fleetcreate.DispatchBinding
	attempt   fleetcreate.Attempt
	admission fleetcreate.Admission
	recovery  fleetcreate.RecoveryReadback
	grant     fleetcreate.Grant
	consumed  bool
}

func (s *fleetCreateHTTPStore) RecordFleetCreateIntent(_ context.Context, intent fleetcreate.Intent, _ string) error {
	s.intent = intent
	return nil
}
func (s *fleetCreateHTTPStore) GetFleetCreateIntent(_ context.Context, cluster string) (fleetcreate.Intent, error) {
	if s.intent.Cluster != cluster {
		return fleetcreate.Intent{}, errFleetCreateHTTPNotFound
	}
	return s.intent, nil
}
func (s *fleetCreateHTTPStore) RecordFleetCreateDispatch(_ context.Context, binding fleetcreate.DispatchBinding, _ string) error {
	s.binding = binding
	return nil
}
func (s *fleetCreateHTTPStore) GetFleetCreateDispatch(_ context.Context, intentID string) (fleetcreate.DispatchBinding, error) {
	if s.binding.IntentID != intentID {
		return fleetcreate.DispatchBinding{}, errFleetCreateHTTPNotFound
	}
	return s.binding, nil
}
func (s *fleetCreateHTTPStore) StartFleetCreateAttempt(_ context.Context, request fleetcreate.AttemptStartRequest, _ string) (fleetcreate.Attempt, error) {
	if !s.binding.MatchesStart(request) {
		return fleetcreate.Attempt{}, errFleetCreateHTTPRefused
	}
	s.attempt = fleetcreate.AttemptFromStart(request)
	return s.attempt, nil
}
func (s *fleetCreateHTTPStore) GetFleetCreateAttempt(_ context.Context, cluster, intentID, attemptID string) (fleetcreate.Attempt, error) {
	if s.attempt.AttemptID != attemptID || s.attempt.IntentID != intentID || s.binding.Cluster != cluster {
		return fleetcreate.Attempt{}, errFleetCreateHTTPNotFound
	}
	return s.attempt, nil
}
func (s *fleetCreateHTTPStore) RecordFleetCreateAdmission(_ context.Context, admission fleetcreate.Admission, _ string) error {
	s.admission = admission
	return nil
}
func (s *fleetCreateHTTPStore) GetFleetCreateAdmission(_ context.Context, cluster, intentID, attemptID string) (fleetcreate.Admission, error) {
	if s.admission.Cluster != cluster || s.admission.IntentID != intentID || s.admission.AttemptID != attemptID {
		return fleetcreate.Admission{}, errFleetCreateHTTPNotFound
	}
	return s.admission, nil
}
func (s *fleetCreateHTTPStore) IssueFleetCreateEffectGrant(_ context.Context, request fleetcreate.Request) (fleetcreate.Grant, error) {
	if !request.MatchesAdmission(s.admission) {
		return fleetcreate.Grant{}, errFleetCreateHTTPRefused
	}
	s.grant = fleetcreate.GrantFromRequest(request)
	return s.grant, nil
}
func (s *fleetCreateHTTPStore) GetFleetCreateEffectGrant(_ context.Context, intentID, attemptID string) (fleetcreate.Grant, bool, error) {
	if s.grant.IntentID != intentID || s.grant.AttemptID != attemptID {
		return fleetcreate.Grant{}, false, errFleetCreateHTTPNotFound
	}
	return s.grant, s.consumed, nil
}
func (s *fleetCreateHTTPStore) ConsumeFleetCreateEffectGrant(_ context.Context, request fleetcreate.Request) (fleetcreate.Grant, error) {
	if !s.grant.MatchesRequest(request) || s.consumed {
		return fleetcreate.Grant{}, errFleetCreateHTTPRefused
	}
	s.consumed = true
	return s.grant, nil
}
func (s *fleetCreateHTTPStore) RecordFleetCreateRecoveryReadback(_ context.Context, readback fleetcreate.RecoveryReadback, _ string) error {
	s.recovery = readback
	return nil
}
func (s *fleetCreateHTTPStore) StartFleetCreateRecoveryAttempt(_ context.Context, request fleetcreate.AttemptStartRequest, priorAttemptID, _ string) (fleetcreate.Attempt, error) {
	if priorAttemptID == "" || !s.binding.MatchesStart(request) {
		return fleetcreate.Attempt{}, errFleetCreateHTTPRefused
	}
	s.attempt = fleetcreate.AttemptForGeneration(request, 2)
	return s.attempt, nil
}

var (
	errFleetCreateHTTPNotFound = errors.New("not found")
	errFleetCreateHTTPRefused  = errors.New("refused")
)

func fleetCreateHTTPFixtures() (fleetcreate.Intent, fleetcreate.DispatchBinding, fleetcreate.AttemptStartRequest) {
	digest := func(value string) string { return fleetcreate.SHA256([]byte(value)) }
	intent := fleetcreate.Intent{SchemaVersion: fleetcreate.IntentSchema, IntentID: uuid.NewString(), PlanID: uuid.NewString(),
		Cluster: "fresh-fleet", CommitSHA: strings.Repeat("a", 40), PlanSHA256: digest("plan"),
		ProviderAccountSHA256: digest("account"), StateBackendSHA256: digest("backend"), StateLineageSHA256: digest("lineage"),
		StateSerial: 4, TopologySHA256: digest("topology"), SSHInputsSHA256: digest("ssh")}
	intentDigest, _ := intent.Digest()
	binding := fleetcreate.DispatchBinding{SchemaVersion: fleetcreate.DispatchSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, IntentSHA256: intentDigest,
		Repository: "antiartificial/norn-fleet", RepositoryID: "101", RepositoryOwnerID: "202",
		WorkflowPath: ".github/workflows/fresh-fleet-create.yml", WorkflowSHA: strings.Repeat("b", 40),
		Environment: "work-staging", RunID: "4321", RunAttempt: "1", CommitSHA: intent.CommitSHA,
		DispatchReceiptSHA256: digest("verified-run-receipt")}
	request := fleetcreate.AttemptStartRequest{SchemaVersion: fleetcreate.StartSchema,
		IntentID: intent.IntentID, PlanID: intent.PlanID, Cluster: intent.Cluster, AttemptID: uuid.NewString(),
		IntentSHA256: intentDigest, RunnerAttemptID: binding.RunnerAttemptID(),
		DispatchReceiptSHA256: binding.DispatchReceiptSHA256}
	return intent, binding, request
}

func fleetCreateHTTPPrincipal(binding fleetcreate.DispatchBinding) AccessPrincipal {
	return AccessPrincipal{Subject: "runner:4321", TokenID: "managed-oidc-token", Source: AccessPrincipalSourceManagedToken,
		Scopes: []string{ScopeFleetOperate}, ExpiresAt: time.Now().Add(time.Hour), CI: &CIIdentity{
			Provider: "github-actions", Repository: binding.Repository, RepositoryID: binding.RepositoryID,
			RepositoryOwnerID: binding.RepositoryOwnerID,
			WorkflowRef:       binding.Repository + "/" + binding.WorkflowPath + "@refs/heads/main",
			WorkflowSHA:       binding.WorkflowSHA, Environment: binding.Environment,
			RunID: binding.RunID, RunAttempt: binding.RunAttempt, SHA: binding.CommitSHA,
			Ref: "refs/heads/main", RefType: "branch", EventName: "workflow_dispatch", RefProtected: true,
			Intent: "create",
		}}
}

func TestFleetCreateAttemptRequiresExactVerifierBoundOIDCRun(t *testing.T) {
	intent, binding, request := fleetCreateHTTPFixtures()
	store := &fleetCreateHTTPStore{intent: intent, binding: binding}
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts", FleetCreateAttemptHandler(store))
	route := "/api/v1/fleet/create/" + intent.Cluster + "/intents/" + intent.IntentID + "/attempts"
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	principal := fleetCreateHTTPPrincipal(binding)
	recorder := httptest.NewRecorder()
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, route, strings.NewReader(string(body))), &principal)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated || store.attempt.AttemptID != request.AttemptID || store.attempt.RunnerAttemptID != binding.RunnerAttemptID() {
		t.Fatalf("valid exact run: status=%d body=%s attempt=%+v", recorder.Code, recorder.Body.String(), store.attempt)
	}

	for name, mutate := range map[string]func(*AccessPrincipal){
		"wrong_run":     func(p *AccessPrincipal) { p.CI.RunID = "4322" },
		"wrong_attempt": func(p *AccessPrincipal) { p.CI.RunAttempt = "2" },
		"wrong_workflow": func(p *AccessPrincipal) {
			p.CI.WorkflowRef = "antiartificial/norn-fleet/.github/workflows/other.yml@refs/heads/main"
		},
		"wrong_source":      func(p *AccessPrincipal) { p.CI.SHA = strings.Repeat("c", 40) },
		"unprotected_ref":   func(p *AccessPrincipal) { p.CI.RefProtected = false },
		"wrong_environment": func(p *AccessPrincipal) { p.CI.Environment = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := fleetCreateHTTPPrincipal(binding)
			mutate(&bad)
			recorder := httptest.NewRecorder()
			req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, route, strings.NewReader(string(body))), &bad)
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("mutated OIDC identity status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestFleetCreateIntentAndDispatchHandlersRequireDedicatedVerifier(t *testing.T) {
	intent, binding, _ := fleetCreateHTTPFixtures()
	store := &fleetCreateHTTPStore{}
	router := chi.NewRouter()
	base := "/api/v1/fleet/create/{cluster}/intents"
	router.MethodFunc(http.MethodPost, base, FleetCreateIntentHandler(store))
	router.MethodFunc(http.MethodGet, base, FleetCreateIntentHandler(store))
	router.MethodFunc(http.MethodPost, base+"/{intentID}/dispatch-binding", FleetCreateDispatchBindingHandler(store))
	router.MethodFunc(http.MethodGet, base+"/{intentID}/dispatch-binding", FleetCreateDispatchBindingHandler(store))
	intentURL := "/api/v1/fleet/create/" + intent.Cluster + "/intents"
	intentBody, _ := json.Marshal(intent)
	verifier := AccessPrincipal{Subject: fleetCreateVerifierSubject, TokenID: "verifier-token",
		Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetCreateVerify}, ExpiresAt: time.Now().Add(time.Hour)}

	unauthorized := verifier
	unauthorized.Scopes = []string{ScopeFleetCreateVerify, ScopeAPIRead}
	unauthorizedRecorder := httptest.NewRecorder()
	unauthorizedRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, intentURL, strings.NewReader(string(intentBody))), &unauthorized)
	router.ServeHTTP(unauthorizedRecorder, unauthorizedRequest)
	if unauthorizedRecorder.Code != http.StatusForbidden {
		t.Fatalf("combined verifier scope status=%d body=%s", unauthorizedRecorder.Code, unauthorizedRecorder.Body.String())
	}

	intentRecorder := httptest.NewRecorder()
	intentRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, intentURL, strings.NewReader(string(intentBody))), &verifier)
	router.ServeHTTP(intentRecorder, intentRequest)
	if intentRecorder.Code != http.StatusCreated || store.intent != intent {
		t.Fatalf("intent record status=%d body=%s", intentRecorder.Code, intentRecorder.Body.String())
	}

	dispatchURL := intentURL + "/" + intent.IntentID + "/dispatch-binding"
	dispatchBody, _ := json.Marshal(binding)
	dispatchRecorder := httptest.NewRecorder()
	dispatchRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, dispatchURL, strings.NewReader(string(dispatchBody))), &verifier)
	router.ServeHTTP(dispatchRecorder, dispatchRequest)
	if dispatchRecorder.Code != http.StatusCreated || store.binding != binding {
		t.Fatalf("dispatch record status=%d body=%s", dispatchRecorder.Code, dispatchRecorder.Body.String())
	}
}

func TestFleetCreateAdmissionHandlerIsVerifierBoundAndPathExact(t *testing.T) {
	intent, _, request := fleetCreateHTTPFixtures()
	store := &fleetCreateHTTPStore{}
	router := chi.NewRouter()
	router.MethodFunc(http.MethodPost, "/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts/{attemptID}/admission", FleetCreateAdmissionHandler(store))
	router.MethodFunc(http.MethodGet, "/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts/{attemptID}/admission", FleetCreateAdmissionHandler(store))
	route := "/api/v1/fleet/create/" + intent.Cluster + "/intents/" + intent.IntentID + "/attempts/" + request.AttemptID + "/admission"
	admission := fleetcreate.Admission{SchemaVersion: fleetcreate.AdmissionSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, AttemptID: request.AttemptID,
		IntentSHA256: request.IntentSHA256, RemainderSHA256: fleetcreate.SHA256([]byte("remainder")),
		ProviderReadbackSHA256: fleetcreate.SHA256([]byte("provider")), StateReadbackSHA256: fleetcreate.SHA256([]byte("state")),
		StateLineageSHA256: intent.StateLineageSHA256, StateSerial: intent.StateSerial + 1,
		ExpectedNodeCount: 9, ObservedNodeCount: 0}
	body, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	verifier := AccessPrincipal{Subject: fleetCreateVerifierSubject, TokenID: "verifier-token",
		Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetCreateVerify}, ExpiresAt: time.Now().Add(time.Hour)}
	recorder := httptest.NewRecorder()
	requestWithPrincipal := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, route, strings.NewReader(string(body))), &verifier)
	router.ServeHTTP(recorder, requestWithPrincipal)
	if recorder.Code != http.StatusCreated || store.admission != admission {
		t.Fatalf("admission record status=%d body=%s stored=%+v", recorder.Code, recorder.Body.String(), store.admission)
	}

	badPath := admission
	badPath.AttemptID = uuid.NewString()
	badBody, _ := json.Marshal(badPath)
	badRecorder := httptest.NewRecorder()
	badRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, route, strings.NewReader(string(badBody))), &verifier)
	router.ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("mismatched attempt path status=%d body=%s", badRecorder.Code, badRecorder.Body.String())
	}

	unauthorized := verifier
	unauthorized.CI = &CIIdentity{Provider: "github-actions"}
	unauthorizedRecorder := httptest.NewRecorder()
	unauthorizedRequest := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, route, strings.NewReader(string(body))), &unauthorized)
	router.ServeHTTP(unauthorizedRecorder, unauthorizedRequest)
	if unauthorizedRecorder.Code != http.StatusForbidden {
		t.Fatalf("verifier with CI identity status=%d body=%s", unauthorizedRecorder.Code, unauthorizedRecorder.Body.String())
	}
}

func TestFleetCreateRecoveryHandlersBindVerifierAndRunnerPaths(t *testing.T) {
	intent, binding, request := fleetCreateHTTPFixtures()
	store := &fleetCreateHTTPStore{intent: intent, binding: binding}
	router := chi.NewRouter()
	base := "/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts/{attemptID}"
	router.Post(base+"/recovery-readback", FleetCreateRecoveryReadbackHandler(store))
	router.Post("/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts/{priorAttemptID}/recoveries", FleetCreateRecoveryAttemptHandler(store))
	readback := fleetcreate.RecoveryReadback{SchemaVersion: fleetcreate.RecoverySchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, PriorAttemptID: request.AttemptID,
		IntentSHA256: request.IntentSHA256, PriorGrantSHA256: strings.Repeat("1", 64),
		ProviderReadbackSHA256: strings.Repeat("2", 64), StateReadbackSHA256: strings.Repeat("3", 64),
		StateLineageSHA256: intent.StateLineageSHA256, StateSerial: intent.StateSerial + 1,
		ExpectedNodeCount: 9, ObservedNodeCount: 8, RemainderSHA256: strings.Repeat("4", 64)}
	body, _ := json.Marshal(readback)
	verifier := AccessPrincipal{Subject: fleetCreateVerifierSubject, TokenID: "verifier-token", Source: AccessPrincipalSourceManagedToken,
		Scopes: []string{ScopeFleetCreateVerify}, ExpiresAt: time.Now().Add(time.Hour)}
	readbackURL := "/api/v1/fleet/create/" + intent.Cluster + "/intents/" + intent.IntentID + "/attempts/" + request.AttemptID + "/recovery-readback"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, readbackURL, strings.NewReader(string(body))), &verifier))
	if response.Code != http.StatusCreated || store.recovery != readback {
		t.Fatalf("recovery readback status=%d body=%s", response.Code, response.Body.String())
	}
	bad := readback
	bad.PriorAttemptID = uuid.NewString()
	badBody, _ := json.Marshal(bad)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, readbackURL, strings.NewReader(string(badBody))), &verifier))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mismatched prior attempt status=%d", response.Code)
	}

	runner := fleetCreateHTTPPrincipal(binding)
	requestBody, _ := json.Marshal(request)
	recoveryURL := "/api/v1/fleet/create/" + intent.Cluster + "/intents/" + intent.IntentID + "/attempts/" + request.AttemptID + "/recoveries"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, recoveryURL, strings.NewReader(string(requestBody))), &runner))
	if response.Code != http.StatusCreated || store.attempt.Generation != 2 {
		t.Fatalf("recovery attempt status=%d body=%s attempt=%+v", response.Code, response.Body.String(), store.attempt)
	}
}

func TestFleetCreateEffectGrantRoutesBindVerifierAdmissionAndExactRunnerDispatch(t *testing.T) {
	intent, binding, start := fleetCreateHTTPFixtures()
	attempt := fleetcreate.AttemptFromStart(start)
	intentDigest, _ := intent.Digest()
	admission := fleetcreate.Admission{SchemaVersion: fleetcreate.AdmissionSchema, IntentID: intent.IntentID,
		PlanID: intent.PlanID, Cluster: intent.Cluster, AttemptID: attempt.AttemptID, IntentSHA256: intentDigest,
		RemainderSHA256:        fleetcreate.SHA256([]byte("exact remainder")),
		ProviderReadbackSHA256: fleetcreate.SHA256([]byte("provider readback")),
		StateReadbackSHA256:    fleetcreate.SHA256([]byte("state readback")), StateLineageSHA256: intent.StateLineageSHA256,
		StateSerial: intent.StateSerial + 1, ExpectedNodeCount: 9, ObservedNodeCount: 0}
	request := fleetcreate.Request{SchemaVersion: fleetcreate.RequestSchema, IntentID: intent.IntentID, PlanID: intent.PlanID,
		AttemptID: attempt.AttemptID, IntentSHA256: intentDigest, AdmissionSHA256: admission.Digest(), RemainderSHA256: admission.RemainderSHA256}
	store := &fleetCreateHTTPStore{intent: intent, binding: binding, attempt: attempt, admission: admission}
	router := chi.NewRouter()
	base := "/api/v1/fleet/create/{cluster}/intents/{intentID}/attempts/{attemptID}/effect-grant"
	router.Post(base, FleetCreateEffectGrantHandler(store))
	router.Get(base, FleetCreateEffectGrantHandler(store))
	router.Post(base+"/consume", FleetCreateEffectGrantConsumeHandler(store))
	router.Get(base+"/status", FleetCreateEffectGrantStatusHandler(store))
	path := "/api/v1/fleet/create/" + intent.Cluster + "/intents/" + intent.IntentID + "/attempts/" + attempt.AttemptID + "/effect-grant"
	body, _ := json.Marshal(request)
	verifier := AccessPrincipal{Subject: fleetCreateVerifierSubject, TokenID: "verifier-token", Source: AccessPrincipalSourceManagedToken,
		Scopes: []string{ScopeFleetCreateVerify}, ExpiresAt: time.Now().Add(time.Hour)}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))), &verifier))
	if response.Code != http.StatusCreated || !store.grant.MatchesRequest(request) || store.consumed {
		t.Fatalf("issue grant status=%d body=%s grant=%+v", response.Code, response.Body.String(), store.grant)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodGet, path, nil), &verifier))
	var readback fleetcreate.GrantReadback
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &readback) != nil || readback.Grant != store.grant || readback.Consumed {
		t.Fatalf("grant readback status=%d body=%s readback=%+v", response.Code, response.Body.String(), readback)
	}
	runner := fleetCreateHTTPPrincipal(binding)
	consumePath := path + "/consume"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, consumePath, strings.NewReader(string(body))), &runner))
	readback = fleetcreate.GrantReadback{}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &readback) != nil || readback.Grant != store.grant || !readback.Consumed {
		t.Fatalf("consume grant status=%d body=%s readback=%+v", response.Code, response.Body.String(), readback)
	}
	statusPath := path + "/status"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodGet, statusPath, nil), &runner))
	var status fleetcreate.GrantStatus
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil ||
		status.IntentID != intent.IntentID || status.AttemptID != attempt.AttemptID ||
		status.GrantSHA256 != store.grant.Digest() || !status.Consumed || strings.Contains(response.Body.String(), `"grant"`) {
		t.Fatalf("grant status readback status=%d body=%s status=%+v", response.Code, response.Body.String(), status)
	}
	wrongRunner := fleetCreateHTTPPrincipal(binding)
	wrongRunner.CI.RunID = "4322"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodPost, consumePath, strings.NewReader(string(body))), &wrongRunner))
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong runner consume status=%d body=%s", response.Code, response.Body.String())
	}
	wrongRunner = fleetCreateHTTPPrincipal(binding)
	wrongRunner.CI.RunID = "4322"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, WithAccessPrincipal(httptest.NewRequest(http.MethodGet, statusPath, nil), &wrongRunner))
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong runner status readback=%d body=%s", response.Code, response.Body.String())
	}
}
