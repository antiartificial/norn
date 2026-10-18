package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

const opsTestSecret = "executor-ops-test-secret"

// --- fakes ---

type fakeOpsStore struct {
	mu       sync.Mutex
	accepted map[string]store.OperationAcceptance
	order    []string
	failNext error
}

func opsIdentityKey(i store.OperationRequestIdentity) string {
	return strings.Join([]string{i.Authority, i.Actor.Issuer, i.Actor.Subject, i.Kind, i.Resource, i.Key}, "|")
}

func (f *fakeOpsStore) Authority(context.Context) (string, error) {
	return "4f0d9f0e-8c4d-4c5b-9a5c-0d6f7d2f2b11", nil
}

func (f *fakeOpsStore) Accept(_ context.Context, a store.OperationAcceptance) (store.AcceptedOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return store.AcceptedOperation{}, err
	}
	key := opsIdentityKey(a.Identity)
	if prior, ok := f.accepted[key]; ok {
		if prior.Fingerprint != a.Fingerprint {
			return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: a.Identity}
		}
		return store.AcceptedOperation{Operation: prior.Operation, Replayed: true}, nil
	}
	if f.accepted == nil {
		f.accepted = map[string]store.OperationAcceptance{}
	}
	f.accepted[key] = a
	f.order = append(f.order, key)
	return store.AcceptedOperation{Operation: a.Operation}, nil
}

func (f *fakeOpsStore) Resolve(ctx context.Context, i store.OperationRequestIdentity, fp store.RequestFingerprint) (store.AcceptedOperation, error) {
	prior, err := f.ResolveIdentity(ctx, i)
	if err != nil {
		return prior, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accepted[opsIdentityKey(i)].Fingerprint != fp {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: i}
	}
	return prior, nil
}

func (f *fakeOpsStore) ResolveIdentity(_ context.Context, i store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prior, ok := f.accepted[opsIdentityKey(i)]
	if !ok {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: i}
	}
	return store.AcceptedOperation{Operation: prior.Operation, Replayed: true}, nil
}

func (f *fakeOpsStore) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.accepted {
		if a.Operation.Kind == kind {
			n++
		}
	}
	return n
}

func (f *fakeOpsStore) last(kind string) store.OperationAcceptance {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out store.OperationAcceptance
	for _, key := range f.order {
		if a := f.accepted[key]; a.Operation.Kind == kind {
			out = a
		}
	}
	return out
}

type fakeAttemptRegistry struct {
	mu      sync.Mutex
	tokens  map[string]*store.AccessToken
	revoked map[string]bool
}

func (r *fakeAttemptRegistry) RecordAccessToken(_ context.Context, token *store.AccessToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens == nil {
		r.tokens, r.revoked = map[string]*store.AccessToken{}, map[string]bool{}
	}
	copy := *token
	r.tokens[token.JTI] = &copy
	return nil
}

func (r *fakeAttemptRegistry) RevokeAccessToken(_ context.Context, jti string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tokens[jti]; !ok {
		return nil, store.ErrIdentityNotFound
	}
	r.revoked[jti] = true
	return nil, nil
}

type fakeOpsBackend struct {
	ops      *fakeOpsStore
	registry *fakeAttemptRegistry
	profile  executor.GrantProfile
	attempts []store.FleetExecutorAttemptFact
	devices  map[string]store.FleetExecutorDeviceFact
	converge bool
}

func (b *fakeOpsBackend) OperationStore() store.OperationStore { return b.ops }
func (b *fakeOpsBackend) Actor(_ http.ResponseWriter, _ *http.Request, p AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	return store.OperationActor{Issuer: "norn://managed-token", Subject: p.TokenID}, store.AcceptanceAuditContext{Source: "test"}, true
}
func (b *fakeOpsBackend) GrantProfile() executor.GrantProfile { return b.profile }
func (b *fakeOpsBackend) AttemptCredentials() FleetAttemptCredentialRegistry {
	return b.registry
}
func (b *fakeOpsBackend) FleetExecutorAttempts(_ context.Context, planID string) ([]store.FleetExecutorAttemptFact, error) {
	var out []store.FleetExecutorAttemptFact
	for _, a := range b.attempts {
		if a.PlanID == planID {
			out = append(out, a)
		}
	}
	return out, nil
}
func (b *fakeOpsBackend) FleetExecutorAttemptByID(_ context.Context, id string) (*store.FleetExecutorAttemptFact, error) {
	for i := range b.attempts {
		if b.attempts[i].ID == id {
			return &b.attempts[i], nil
		}
	}
	return nil, nil
}
func (b *fakeOpsBackend) FleetExecutorDevice(_ context.Context, id string) (store.FleetExecutorDeviceFact, error) {
	return b.devices[id], nil
}
func (b *fakeOpsBackend) FleetExecutorPlanIsConverge(context.Context, string) (bool, error) {
	return b.converge, nil
}
func (b *fakeOpsBackend) FleetExecutorLedgerRows(_ context.Context, kind, planID string) ([]store.FleetExecutorLedgerRow, error) {
	b.ops.mu.Lock()
	defer b.ops.mu.Unlock()
	var rows []store.FleetExecutorLedgerRow
	for _, key := range b.ops.order {
		if a := b.ops.accepted[key]; a.Operation.Kind == kind && a.Operation.Ref == planID {
			rows = append(rows, store.FleetExecutorLedgerRow{ID: a.Operation.ID, Payload: a.Operation.Payload, Metadata: a.Operation.Metadata, StartedAt: a.Operation.StartedAt})
		}
	}
	return rows, nil
}

type opsFixture struct {
	t       *testing.T
	router  http.Handler
	routes  *FleetExecutorOpsRoutes
	backend *fakeOpsBackend
	planID  string
	attempt store.FleetExecutorAttemptFact
	secret  string
}

const (
	opsDevice  = "dev-1"
	opsDroplet = "424242"
)

func newOpsFixture(t *testing.T, profile executor.GrantProfile, phase string) *opsFixture {
	t.Helper()
	planID := uuid.NewString()
	attempt := store.FleetExecutorAttemptFact{
		ID: uuid.NewString(), PlanID: planID, RunnerAttemptID: executor.RunnerAttemptID(opsDroplet, strings.Repeat("a", 32), planID),
		Attempt: 1, Status: "running", CurrentPhase: phase, PrincipalSubject: executor.PrincipalSubject(opsDevice), Executor: true,
		Metadata:  executor.AttemptMetadata{Lane: executor.LaneMetadataValue, DropletID: opsDroplet, MachineID: "machine-1", BootID: "boot-1", InvocationID: strings.Repeat("a", 32)},
		StartedAt: time.Now().Add(-time.Hour), HeartbeatExpiresAt: time.Now().Add(time.Minute),
	}
	backend := &fakeOpsBackend{
		ops: &fakeOpsStore{}, registry: &fakeAttemptRegistry{}, profile: profile, attempts: []store.FleetExecutorAttemptFact{attempt}, converge: true,
		devices: map[string]store.FleetExecutorDeviceFact{opsDevice: {Exists: true, Platform: executor.Platform}},
	}
	routes := NewFleetExecutorOpsRoutes(backend, opsTestSecret)
	router := chi.NewRouter()
	router.Post("/api/v1/fleet/plans/{planID}/credential-grants", routes.Grant)
	router.Post("/api/v1/fleet/plans/{planID}/preflight-completions", routes.PreflightComplete)
	router.Post("/api/v1/fleet/executor/stop-attestations", routes.StopAttestation)
	router.Post("/api/v1/fleet/plans/{planID}/provider-evidence", routes.ProviderEvidence)
	return &opsFixture{t: t, router: router, routes: routes, backend: backend, planID: planID, attempt: attempt, secret: opsTestSecret}
}

func opsExecutorPrincipal() *AccessPrincipal {
	p := executorPrincipal()
	return &p
}

func (f *opsFixture) grant(principal *AccessPrincipal, key string, body interface{}) *httptest.ResponseRecorder {
	return fleetTargetDo(f.router, http.MethodPost, "/api/v1/fleet/plans/"+f.planID+"/credential-grants", principal, key, body)
}

func (f *opsFixture) attest(principal *AccessPrincipal, key string, body interface{}) *httptest.ResponseRecorder {
	return fleetTargetDo(f.router, http.MethodPost, "/api/v1/fleet/executor/stop-attestations", principal, key, body)
}

func (f *opsFixture) evidence(principal *AccessPrincipal, key string, body interface{}) *httptest.ResponseRecorder {
	return fleetTargetDo(f.router, http.MethodPost, "/api/v1/fleet/plans/"+f.planID+"/provider-evidence", principal, key, body)
}

func (f *opsFixture) grantBody(phase string, types ...string) map[string]interface{} {
	body := map[string]interface{}{"phase": phase, "types": types}
	if phase != executor.GrantPhasePlan {
		body["attemptId"] = f.attempt.ID
	}
	return body
}

type grantResponseBody struct {
	GrantID      string    `json:"grantId"`
	ExpiresAt    time.Time `json:"expiresAt"`
	AttemptToken string    `json:"attemptToken"`
}

func decodeGrant(t *testing.T, rec *httptest.ResponseRecorder) grantResponseBody {
	t.Helper()
	var out grantResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func requireProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status || (code != "" && decodeProblemCode(rec) != code) {
		t.Fatalf("want %d %q, got %d %q: %s", status, code, rec.Code, decodeProblemCode(rec), rec.Body.String())
	}
}

// --- grants ---

func TestFleetExecutorGrantRequiresLiveAttemptAndCurrentPhase(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "infrastructure_applied")
	principal := opsExecutorPrincipal()

	rec := f.grant(principal, "k1", f.grantBody("infrastructure_applied", executor.CredDOWrite, executor.CredSpacesRW, executor.CredNornAttempt))
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	first := decodeGrant(t, rec)
	if first.GrantID == "" || first.AttemptToken == "" || time.Until(first.ExpiresAt) < time.Hour+50*time.Minute || time.Until(first.ExpiresAt) > executor.GrantTTL {
		t.Fatalf("grant response: %+v", first)
	}
	op := f.backend.ops.last(executor.KindCredentialGrant)
	var payload executor.CredentialGrantPayload
	if !decodeLedgerValueMap(op.Operation.Payload, &payload) || op.Operation.Ref != f.planID || payload.GrantID != first.GrantID || payload.AttemptID != f.attempt.ID ||
		payload.Phase != "infrastructure_applied" || strings.Join(payload.Types, ",") != "do-write,norn-attempt,spaces-rw" ||
		payload.DeviceID != opsDevice || payload.TokenID != principal.TokenID || !payload.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("ledger payload: %+v", op.Operation.Payload)
	}

	// The attempt-bound credential: not the device token, scoped to one attempt.
	claims, err := verifyToken(opsTestSecret, first.AttemptToken, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != ScopeFleetAttempt || claims.Sub != "fleet-attempt:"+f.attempt.ID || claims.Did != "" ||
		!claims.Managed || claims.Exp != first.ExpiresAt.Unix() {
		t.Fatalf("claims: %+v", claims)
	}
	record := f.backend.registry.tokens[claims.Jti]
	if record == nil || record.DeviceID != opsDevice || record.Subject != claims.Sub || !record.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("token record: %+v", record)
	}
	attemptPrincipal := AccessPrincipal{Subject: claims.Sub, TokenID: claims.Jti, Scopes: claims.Scopes, ExpiresAt: time.Unix(claims.Exp, 0), Source: AccessPrincipalSourceManagedToken}
	if IsFleetExecutorPrincipal(attemptPrincipal) || attemptPrincipal.Allows(ScopeFleetExecute) {
		t.Fatal("the attempt credential must not be an executor principal")
	}

	// A lost response replays the same grant and the same credential.
	replay := f.grant(principal, "k1", f.grantBody("infrastructure_applied", executor.CredNornAttempt, executor.CredSpacesRW, executor.CredDOWrite))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if again := decodeGrant(t, replay); again.GrantID != first.GrantID || again.AttemptToken != first.AttemptToken || !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("replay differs: %+v vs %+v", again, first)
	}
	if f.backend.ops.count(executor.KindCredentialGrant) != 1 || len(f.backend.registry.tokens) != 1 {
		t.Fatal("a replay records nothing new")
	}
	// The same key for different intent is a conflict, not a second grant.
	requireProblem(t, f.grant(principal, "k1", f.grantBody("infrastructure_applied", executor.CredNornAttempt)), http.StatusConflict, "")

	// The wrong phase, in either direction.
	requireProblem(t, f.grant(principal, "k2", f.grantBody("nodes_configured", executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantPhaseMismatch)
	requireProblem(t, f.grant(principal, "k3", f.grantBody(executor.GrantPhaseRecover, executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)

	// Another device's attempt, an unknown attempt and an expired lease.
	other := *principal
	other.DeviceID, other.TokenID = "dev-2", "jti-2"
	f.backend.devices["dev-2"] = store.FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}
	requireProblem(t, f.grant(&other, "k4", f.grantBody("infrastructure_applied", executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)
	unknown := f.grantBody("infrastructure_applied", executor.CredNornAttempt)
	unknown["attemptId"] = uuid.NewString()
	requireProblem(t, f.grant(principal, "k5", unknown), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-time.Second)
	requireProblem(t, f.grant(principal, "k6", f.grantBody("infrastructure_applied", executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)
	// A replay after attempt lease expiry cannot revive its credential.
	requireProblem(t, f.grant(principal, "k1", f.grantBody("infrastructure_applied", executor.CredDOWrite, executor.CredSpacesRW, executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
}

func TestFleetExecutorGrantTypeTable(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "nodes_configured")
	principal := opsExecutorPrincipal()
	// The post-apply phases never carry provider write credentials.
	requireProblem(t, f.grant(principal, "t1", f.grantBody("nodes_configured", executor.CredDOWrite)), http.StatusForbidden, executor.CodeFleetCredentialTypeNotAllowed)
	requireProblem(t, f.grant(principal, "t2", f.grantBody("nodes_configured", executor.CredSSHNode, executor.CredDORead)), http.StatusForbidden, executor.CodeFleetCredentialTypeNotAllowed)
	requireProblem(t, f.grant(principal, "t3", f.grantBody("nodes_configured")), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	requireProblem(t, f.grant(principal, "t4", map[string]interface{}{"phase": "nodes_configured", "types": []string{"ssh-node"}}), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)
	requireProblem(t, f.grant(principal, "t5", f.grantBody("provider_applying", executor.CredNornAttempt)), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	requireProblem(t, f.grant(principal, "t6", map[string]interface{}{"phase": "nodes_configured", "attemptId": f.attempt.ID, "types": []string{"ssh-node"}, "extra": 1}), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	if f.backend.ops.count(executor.KindCredentialGrant) != 0 || len(f.backend.registry.tokens) != 0 {
		t.Fatal("refusals record nothing and mint nothing")
	}
	// The exact post-apply set is admitted, with an attempt credential.
	rec := f.grant(principal, "t7", f.grantBody("nodes_configured", executor.CredSSHNode, executor.CredBootstrapDir, executor.CredReleaseGitHub, executor.CredNornAttempt, executor.CredSpacesRead))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// A grant with no norn-attempt mints no credential.
	rec = f.grant(principal, "t8", f.grantBody("nodes_configured", executor.CredSSHNode))
	if got := decodeGrant(t, rec); rec.Code != http.StatusCreated || got.AttemptToken != "" || len(f.backend.registry.tokens) != 1 {
		t.Fatalf("%d %+v", rec.Code, got)
	}

	// plan: no attempt; do-read and spaces-rw only; needs a converge plan.
	plan := f.grant(principal, "p1", f.grantBody(executor.GrantPhasePlan, executor.CredDORead, executor.CredSpacesRW))
	if got := decodeGrant(t, plan); plan.Code != http.StatusCreated || got.AttemptToken != "" {
		t.Fatalf("plan grant: %d %s", plan.Code, plan.Body.String())
	}
	requireProblem(t, f.grant(principal, "p2", f.grantBody(executor.GrantPhasePlan, executor.CredNornAttempt)), http.StatusForbidden, executor.CodeFleetCredentialTypeNotAllowed)
	withAttempt := f.grantBody(executor.GrantPhasePlan, executor.CredDORead)
	withAttempt["attemptId"] = f.attempt.ID
	requireProblem(t, f.grant(principal, "p3", withAttempt), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	f.backend.converge = false
	requireProblem(t, f.grant(principal, "p4", f.grantBody(executor.GrantPhasePlan, executor.CredDORead)), http.StatusConflict, executor.CodeFleetPlanNotConverge)
}

func TestFleetExecutorGrantRecoverAndEtcdProfile(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	preflight := decodeGrant(t, f.grant(principal, "v-preflight", f.grantBody(executor.GrantPhasePreflight, executor.CredBootstrapDir, executor.CredSSHNode)))
	completion := map[string]interface{}{"attemptId": f.attempt.ID, "grantId": preflight.GrantID, "evidenceDigest": "sha256:" + strings.Repeat("a", 64)}
	if rec := fleetTargetDo(f.router, http.MethodPost, "/api/v1/fleet/plans/"+f.planID+"/preflight-completions", principal, "v-preflight-complete", completion); rec.Code != http.StatusCreated {
		t.Fatalf("preflight completion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.grant(principal, "v1", f.grantBody("provider_applying", executor.CredDOWrite, executor.CredSpacesRW, executor.CredNornAttempt)); rec.Code != http.StatusCreated {
		t.Fatalf("provider_applying on the canonical list: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, f.grant(principal, "v2", f.grantBody("provider_applying", executor.CredDORead)), http.StatusForbidden, executor.CodeFleetCredentialTypeNotAllowed)
	requireProblem(t, f.grant(principal, "v3", f.grantBody(executor.GrantPhaseRecover, executor.CredDORead)), http.StatusConflict, executor.CodeFleetCredentialGrantAttemptRequired)
	f.backend.attempts[0].RetryOf = uuid.NewString()
	rec := f.grant(principal, "v4", f.grantBody(executor.GrantPhaseRecover, executor.CredDORead, executor.CredSpacesRW, executor.CredSpacesRead,
		executor.CredSSHNode, executor.CredBootstrapDir, executor.CredReleaseGitHub, executor.CredNornAttempt))
	if rec.Code != http.StatusCreated {
		t.Fatalf("recover grant on a live recovery attempt: %d %s", rec.Code, rec.Body.String())
	}
	f.backend.attempts[0].CurrentPhase = "nodes_configured"
	requireProblem(t, f.grant(principal, "v4", f.grantBody(executor.GrantPhaseRecover, executor.CredDORead, executor.CredSpacesRW, executor.CredSpacesRead,
		executor.CredSSHNode, executor.CredBootstrapDir, executor.CredReleaseGitHub, executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
}

func TestFleetExecutorPreflightAttestationBeforeProviderGrant(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	provider := f.grantBody("provider_applying", executor.CredDOWrite, executor.CredSpacesRW, executor.CredNornAttempt)
	requireProblem(t, f.grant(principal, "provider-early", provider), http.StatusConflict, executor.CodeFleetPreflightRequired)
	request := f.grantBody(executor.GrantPhasePreflight, executor.CredBootstrapDir, executor.CredSSHNode)
	rec := f.grant(principal, "preflight-1", request)
	if rec.Code != http.StatusCreated {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	grant := decodeGrant(t, rec)
	if grant.GrantID == "" || grant.AttemptToken != "" || len(f.backend.registry.tokens) != 0 {
		t.Fatalf("preflight must not mint an attempt credential: %+v", grant)
	}
	op := f.backend.ops.last(executor.KindCredentialGrant)
	var payload executor.CredentialGrantPayload
	if !decodeLedgerValueMap(op.Operation.Payload, &payload) || payload.Phase != executor.GrantPhasePreflight || payload.AttemptID != f.attempt.ID ||
		strings.Join(payload.Types, ",") != "bootstrap-dir,ssh-node" {
		t.Fatalf("preflight ledger payload: %+v", op.Operation.Payload)
	}
	// Grant issuance is insufficient. The executor must attest completed checks.
	requireProblem(t, f.grant(principal, "provider-before-attestation", provider), http.StatusConflict, executor.CodeFleetPreflightRequired)
	if replay := f.grant(principal, "preflight-1", request); replay.Code != http.StatusOK {
		t.Fatalf("preflight replay: %d %s", replay.Code, replay.Body.String())
	}
	completion := map[string]interface{}{"attemptId": f.attempt.ID, "grantId": grant.GrantID, "evidenceDigest": "sha256:" + strings.Repeat("a", 64)}
	path := "/api/v1/fleet/plans/" + f.planID + "/preflight-completions"
	rec = fleetTargetDo(f.router, http.MethodPost, path, principal, "complete-1", completion)
	if rec.Code != http.StatusCreated {
		t.Fatalf("preflight completion: %d %s", rec.Code, rec.Body.String())
	}
	if replay := fleetTargetDo(f.router, http.MethodPost, path, principal, "complete-1", completion); replay.Code != http.StatusOK {
		t.Fatalf("preflight completion replay while current: %d %s", replay.Code, replay.Body.String())
	}
	if rec := f.grant(principal, "provider-1", provider); rec.Code != http.StatusCreated {
		t.Fatalf("provider after attestation: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, fleetTargetDo(f.router, http.MethodPost, path, principal, "complete-1", completion), http.StatusConflict, executor.CodeFleetPreflightRequired)
	requireProblem(t, f.grant(principal, "preflight-1", request), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
	requireProblem(t, f.grant(principal, "preflight-after-write", request), http.StatusConflict, executor.CodeFleetPreflightRequired)
	if rec := f.grant(principal, "provider-checkpoint", f.grantBody("provider_applying", executor.CredNornAttempt)); rec.Code != http.StatusCreated {
		t.Fatalf("provider checkpoint grant: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, f.grant(principal, "provider-1", provider), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
	f.backend.attempts[0].CurrentPhase = "infrastructure_applied"
	requireProblem(t, f.grant(principal, "preflight-1", request), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
}

func TestFleetExecutorPreflightGrantReplayExpires(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	base := time.Now().UTC().Truncate(time.Second)
	f.routes.now = func() time.Time { return base }
	f.backend.attempts[0].HeartbeatExpiresAt = base.Add(4 * time.Hour)
	request := f.grantBody(executor.GrantPhasePreflight, executor.CredBootstrapDir, executor.CredSSHNode)
	if rec := f.grant(principal, "expiry", request); rec.Code != http.StatusCreated {
		t.Fatalf("preflight grant: %d %s", rec.Code, rec.Body.String())
	}
	f.routes.now = func() time.Time { return base.Add(executor.GrantTTL + time.Second) }
	requireProblem(t, f.grant(principal, "expiry", request), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
}

func TestFleetExecutorPartialPreflightCannotBeAttested(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	grant := decodeGrant(t, f.grant(principal, "partial", f.grantBody(executor.GrantPhasePreflight, executor.CredSSHNode)))
	completion := map[string]interface{}{"attemptId": f.attempt.ID, "grantId": grant.GrantID, "evidenceDigest": "sha256:" + strings.Repeat("a", 64)}
	path := "/api/v1/fleet/plans/" + f.planID + "/preflight-completions"
	requireProblem(t, fleetTargetDo(f.router, http.MethodPost, path, principal, "partial-complete", completion), http.StatusConflict, executor.CodeFleetPreflightRequired)
}

func TestFleetExecutorRecoveryNeedsOwnPreflightBeforeProviderWrite(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "provider_applying")
	principal := opsExecutorPrincipal()
	source := f.attempt
	source.Status = "failed"
	recovery := f.attempt
	recovery.ID, recovery.Attempt, recovery.RetryOf = uuid.NewString(), 2, source.ID
	f.backend.attempts = []store.FleetExecutorAttemptFact{source, recovery}
	recoverBody := map[string]interface{}{"attemptId": recovery.ID, "phase": executor.GrantPhaseRecover,
		"types": []string{executor.CredDOWrite, executor.CredNornAttempt}}
	requireProblem(t, f.grant(principal, "recover-early", recoverBody), http.StatusConflict, executor.CodeFleetPreflightRequired)
	// A later-phase recovery cannot recover provider-write access without a
	// preflight bound to that recovery attempt.
	f.backend.attempts[1].CurrentPhase = "nodes_configured"
	requireProblem(t, f.grant(principal, "recover-later", recoverBody), http.StatusConflict, executor.CodeFleetPreflightRequired)
	f.backend.attempts[1].CurrentPhase = "provider_applying"
	preflightBody := map[string]interface{}{"attemptId": recovery.ID, "phase": executor.GrantPhasePreflight,
		"types": []string{executor.CredBootstrapDir, executor.CredSSHNode}}
	preflight := decodeGrant(t, f.grant(principal, "recover-preflight", preflightBody))
	completion := map[string]interface{}{"attemptId": recovery.ID, "grantId": preflight.GrantID,
		"evidenceDigest": "sha256:" + strings.Repeat("b", 64)}
	path := "/api/v1/fleet/plans/" + f.planID + "/preflight-completions"
	if rec := fleetTargetDo(f.router, http.MethodPost, path, principal, "recover-complete", completion); rec.Code != http.StatusCreated {
		t.Fatalf("recovery preflight completion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.grant(principal, "recover-write", recoverBody); rec.Code != http.StatusCreated {
		t.Fatalf("recovery provider write after own preflight: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetExecutorLegacyRecoveryNeedsOwnPreflightBeforeProviderWrite(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "infrastructure_applied")
	principal := opsExecutorPrincipal()
	// Existing PG first attempts retain their legacy provider grant contract.
	if rec := f.grant(principal, "initial-write", f.grantBody("infrastructure_applied", executor.CredDOWrite)); rec.Code != http.StatusCreated {
		t.Fatalf("legacy first-attempt provider write: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, f.grant(principal, "initial-preflight", f.grantBody(executor.GrantPhasePreflight, executor.CredBootstrapDir, executor.CredSSHNode)), http.StatusConflict, executor.CodeFleetCredentialGrantPhaseMismatch)

	source := f.attempt
	source.Status = "failed"
	recovery := f.attempt
	recovery.ID, recovery.Attempt, recovery.RetryOf = uuid.NewString(), 2, source.ID
	f.backend.attempts = []store.FleetExecutorAttemptFact{source, recovery}
	recoverBody := map[string]interface{}{"attemptId": recovery.ID, "phase": executor.GrantPhaseRecover,
		"types": []string{executor.CredDOWrite, executor.CredNornAttempt}}
	requireProblem(t, f.grant(principal, "recover-early", recoverBody), http.StatusConflict, executor.CodeFleetPreflightRequired)
	preflightBody := map[string]interface{}{"attemptId": recovery.ID, "phase": executor.GrantPhasePreflight,
		"types": []string{executor.CredBootstrapDir, executor.CredSSHNode}}
	preflight := decodeGrant(t, f.grant(principal, "recover-preflight", preflightBody))
	requireProblem(t, f.grant(principal, "recover-before-attest", recoverBody), http.StatusConflict, executor.CodeFleetPreflightRequired)
	path := "/api/v1/fleet/plans/" + f.planID + "/preflight-completions"
	completion := map[string]interface{}{"attemptId": recovery.ID, "grantId": preflight.GrantID,
		"evidenceDigest": "sha256:" + strings.Repeat("d", 64)}
	if rec := fleetTargetDo(f.router, http.MethodPost, path, principal, "recover-complete", completion); rec.Code != http.StatusCreated {
		t.Fatalf("legacy recovery preflight completion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.grant(principal, "recover-write", recoverBody); rec.Code != http.StatusCreated {
		t.Fatalf("legacy recovery provider write after attestation: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, f.grant(principal, "preflight-after-write", preflightBody), http.StatusConflict, executor.CodeFleetPreflightRequired)
}

func TestFleetExecutorLaterRecoveryCanPreflightBeforeProviderWrite(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfileV3, "nodes_configured")
	principal := opsExecutorPrincipal()
	source := f.attempt
	source.Status = "failed"
	recovery := f.attempt
	recovery.ID, recovery.Attempt, recovery.RetryOf = uuid.NewString(), 2, source.ID
	f.backend.attempts = []store.FleetExecutorAttemptFact{source, recovery}
	preflight := decodeGrant(t, f.grant(principal, "later-preflight", map[string]interface{}{
		"attemptId": recovery.ID, "phase": executor.GrantPhasePreflight,
		"types": []string{executor.CredBootstrapDir, executor.CredSSHNode},
	}))
	f.backend.attempts[1].CurrentPhase = "nodes_enrolled"
	requireProblem(t, f.grant(principal, "later-preflight", map[string]interface{}{
		"attemptId": recovery.ID, "phase": executor.GrantPhasePreflight,
		"types": []string{executor.CredBootstrapDir, executor.CredSSHNode},
	}), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
	f.backend.attempts[1].CurrentPhase = "nodes_configured"
	completion := map[string]interface{}{"attemptId": recovery.ID, "grantId": preflight.GrantID,
		"evidenceDigest": "sha256:" + strings.Repeat("c", 64)}
	path := "/api/v1/fleet/plans/" + f.planID + "/preflight-completions"
	if rec := fleetTargetDo(f.router, http.MethodPost, path, principal, "later-complete", completion); rec.Code != http.StatusCreated {
		t.Fatalf("later recovery preflight completion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.grant(principal, "later-write", map[string]interface{}{
		"attemptId": recovery.ID, "phase": executor.GrantPhaseRecover,
		"types": []string{executor.CredDOWrite, executor.CredNornAttempt},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("later recovery provider write after own preflight: %d %s", rec.Code, rec.Body.String())
	}
}

func TestFleetExecutorAttemptCredentialRevokedWithNextGrant(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "inventory_generated")
	principal := opsExecutorPrincipal()
	first := decodeGrant(t, f.grant(principal, "r1", f.grantBody("inventory_generated", executor.CredNornAttempt)))
	firstClaims, _ := verifyToken(opsTestSecret, first.AttemptToken, time.Time{})
	f.backend.attempts[0].CurrentPhase = "nodes_configured"
	second := decodeGrant(t, f.grant(principal, "r2", f.grantBody("nodes_configured", executor.CredNornAttempt)))
	secondClaims, _ := verifyToken(opsTestSecret, second.AttemptToken, time.Time{})
	if !f.backend.registry.revoked[firstClaims.Jti] || f.backend.registry.revoked[secondClaims.Jti] {
		t.Fatalf("the previous phase's credential is revoked before the next is live: %+v", f.backend.registry.revoked)
	}
	// A superseded grant cannot be replayed into a later phase.
	requireProblem(t, f.grant(principal, "r1", f.grantBody("inventory_generated", executor.CredNornAttempt)), http.StatusConflict, executor.CodeFleetCredentialGrantReplayClosed)
	// A grant without norn-attempt still ends the previous grant's credential.
	if rec := f.grant(principal, "r3", f.grantBody("nodes_configured", executor.CredSSHNode)); rec.Code != http.StatusCreated || !f.backend.registry.revoked[secondClaims.Jti] {
		t.Fatalf("the next grant revokes the previous credential: %d %+v", rec.Code, f.backend.registry.revoked)
	}
	// A recovery attempt's grant ends the dead source attempt's credential.
	third := decodeGrant(t, f.grant(principal, "r4", f.grantBody("nodes_configured", executor.CredNornAttempt)))
	thirdClaims, _ := verifyToken(opsTestSecret, third.AttemptToken, time.Time{})
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-time.Minute)
	recovery := f.attempt
	recovery.ID, recovery.Attempt, recovery.RetryOf, recovery.HeartbeatExpiresAt = uuid.NewString(), 2, f.attempt.ID, time.Now().Add(time.Minute)
	f.backend.attempts = append(f.backend.attempts, recovery)
	body := map[string]interface{}{"phase": executor.GrantPhaseRecover, "attemptId": recovery.ID, "types": []string{executor.CredNornAttempt}}
	if rec := f.grant(principal, "r5", body); rec.Code != http.StatusCreated || !f.backend.registry.revoked[thirdClaims.Jti] {
		t.Fatalf("the recovery grant revokes the source attempt's credential: %d %+v", rec.Code, f.backend.registry.revoked)
	}
}

// TestFleetExecutorAttemptCredentialRoutesMatchShapes keeps the route matcher
// E6 wires equal to the pinned executor.AttemptCredentialRoutes.
func TestFleetExecutorAttemptCredentialRoutesMatchShapes(t *testing.T) {
	attempt, plan := uuid.NewString(), uuid.NewString()
	p := AccessPrincipal{Subject: fleetAttemptSubjectPrefix + attempt, TokenID: "norn_x", Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	allowed := map[string]bool{}
	for _, route := range executor.AttemptCredentialRoutes {
		allowed[route] = true
	}
	for _, route := range executor.ExecutorRoutes {
		method, path, _ := strings.Cut(route, " ")
		concrete := strings.NewReplacer("{planID}", plan, "{attemptID}", attempt, "{name}", "prod").Replace(path)
		if got := FleetAttemptCredentialAllowsRoute(p, method, concrete); got != allowed[route] {
			t.Errorf("%s: matcher %v, AttemptCredentialRoutes %v", route, got, allowed[route])
		}
	}
	if ScopeFleetAttempt != executor.AttemptScope {
		t.Fatal("one scope name")
	}
}

// --- principals ---

func TestFleetExecutorRoutesAcceptOnlyTheirPrincipal(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "infrastructure_applied")
	grant := f.grantBody("infrastructure_applied", executor.CredNornAttempt)
	attestation := map[string]interface{}{"sourceAttemptId": f.attempt.ID}
	ci := &AccessPrincipal{Subject: "runner", TokenID: "t", DeviceID: "d", Source: AccessPrincipalSourceManagedToken, Scopes: []string{ScopeFleetOperate}, CI: &CIIdentity{RunID: "1"}, ExpiresAt: time.Now().Add(time.Hour)}
	ciExecute := *opsExecutorPrincipal()
	ciExecute.CI = &CIIdentity{RunID: "1"}
	wrongScopes := *opsExecutorPrincipal()
	wrongScopes.Scopes = []string{ScopeFleetExecute, ScopeAPIRead}
	legacy := *opsExecutorPrincipal()
	legacy.Legacy = true
	for name, principal := range map[string]*AccessPrincipal{
		"ci fleet:operate": ci, "ci with fleet:execute": &ciExecute, "admin": fleetTargetPrincipal(ScopeAdmin), "platform:operate": fleetTargetPrincipal(ScopePlatformOperate),
		"extra scope": &wrongScopes, "legacy": &legacy,
	} {
		requireProblem(t, f.grant(principal, "g-"+name, grant), http.StatusForbidden, "insufficient_scope")
		requireProblem(t, f.attest(principal, "a-"+name, attestation), http.StatusForbidden, "insufficient_scope")
	}
	requireProblem(t, f.grant(nil, "anon", grant), http.StatusUnauthorized, "")
	requireProblem(t, f.attest(nil, "anon", attestation), http.StatusUnauthorized, "")

	// The device must be live and a fleet-executor device.
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Revoked: true, Platform: executor.Platform}
	requireProblem(t, f.grant(opsExecutorPrincipal(), "revoked", grant), http.StatusForbidden, "insufficient_scope")
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Platform: "ios"}
	requireProblem(t, f.grant(opsExecutorPrincipal(), "ios", grant), http.StatusForbidden, "insufficient_scope")
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{}
	requireProblem(t, f.attest(opsExecutorPrincipal(), "unknown", attestation), http.StatusForbidden, "insufficient_scope")

	// Missing Idempotency-Key.
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}
	requireProblem(t, f.grant(opsExecutorPrincipal(), "", grant), http.StatusBadRequest, "invalid_idempotency_key")
	if f.backend.ops.count(executor.KindCredentialGrant)+f.backend.ops.count(executor.KindStopAttestation) != 0 {
		t.Fatal("refused principals record nothing")
	}
}

// --- stop attestations ---

func (f *opsFixture) attestationBody(mutate func(map[string]interface{})) map[string]interface{} {
	body := map[string]interface{}{
		"sourceAttemptId": f.attempt.ID, "dropletId": opsDroplet, "machineId": "machine-1", "bootId": "boot-2",
		"invocationId": strings.Repeat("b", 32), "phaseUidEmpty": true, "phaseProcessGroupReaped": true, "sourceCgroupEmpty": true,
		"observedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if mutate != nil {
		mutate(body)
	}
	return body
}

func TestFleetExecutorAttestationSameDropletOnly(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "inventory_generated")
	principal := opsExecutorPrincipal()
	rec := f.attest(principal, "s1", f.attestationBody(nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("attestation: %d %s", rec.Code, rec.Body.String())
	}
	op := f.backend.ops.last(executor.KindStopAttestation)
	var payload executor.StopAttestationPayload
	if !decodeLedgerValueMap(op.Operation.Payload, &payload) || op.Operation.Ref != f.planID || payload.PlanID != f.planID ||
		payload.SourceAttemptID != f.attempt.ID || payload.DropletID != opsDroplet || !payload.SourceCgroupEmpty {
		t.Fatalf("payload: %+v", op.Operation.Payload)
	}
	if op.Operation.Metadata[store.FleetExecutorAttesterMetadataKey] != executor.PrincipalSubject(opsDevice) {
		t.Fatalf("metadata: %+v", op.Operation.Metadata)
	}
	if replay := f.attest(principal, "s1", f.attestationBody(func(b map[string]interface{}) { b["observedAt"] = payload.ObservedAt.Format(time.RFC3339Nano) })); replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}

	invalid := executor.CodeFleetExecutorStopAttestationInvalid
	requireProblem(t, f.attest(principal, "c1", f.attestationBody(func(b map[string]interface{}) { b["dropletId"] = "999" })), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c2", f.attestationBody(func(b map[string]interface{}) { b["machineId"] = "cloned-machine" })), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c3", f.attestationBody(func(b map[string]interface{}) { b["invocationId"] = f.attempt.Metadata.InvocationID })), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c4", f.attestationBody(func(b map[string]interface{}) {
		b["observedAt"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	})), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c5", f.attestationBody(func(b map[string]interface{}) {
		b["bootId"], b["sourceCgroupEmpty"] = "boot-1", false
	})), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c6", f.attestationBody(func(b map[string]interface{}) { b["sourceAttemptId"] = uuid.NewString() })), http.StatusConflict, invalid)
	requireProblem(t, f.attest(principal, "c7", f.attestationBody(func(b map[string]interface{}) { delete(b, "bootId") })), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)
	// Another device, even on the same droplet, cannot attest for this device's attempt.
	other := *principal
	other.DeviceID, other.TokenID = "dev-2", "jti-2"
	f.backend.devices["dev-2"] = store.FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}
	requireProblem(t, f.attest(&other, "c8", f.attestationBody(nil)), http.StatusConflict, invalid)
	if f.backend.ops.count(executor.KindStopAttestation) != 1 {
		t.Fatal("only the valid attestation is recorded")
	}
}

// --- provider evidence ---

func (f *opsFixture) evidenceBody(mutate func(map[string]interface{})) map[string]interface{} {
	body := map[string]interface{}{
		"sourceAttemptId": f.attempt.ID,
		"providerEvidence": map[string]interface{}{
			"provider": "digitalocean", "dropletId": opsDroplet, "status": "off",
			"observedAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "reference": "do-console#1",
		},
	}
	if mutate != nil {
		mutate(body)
	}
	return body
}

func TestFleetExecutorProviderEvidenceAdminOnly(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "inventory_generated")
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-2 * time.Hour)
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Revoked: true, Platform: executor.Platform}
	admin := fleetTargetPrincipal(ScopeAdmin)

	// Only a non-CI admin: operators, the executor itself and CI are refused.
	ciAdmin := *admin
	ciAdmin.CI = &CIIdentity{RunID: "1"}
	for name, principal := range map[string]*AccessPrincipal{
		"platform:operate": fleetTargetPrincipal(ScopePlatformOperate), "api:write": fleetTargetPrincipal(ScopeAPIWrite),
		"executor": opsExecutorPrincipal(), "ci admin": &ciAdmin,
	} {
		requireProblem(t, f.evidence(principal, "e-"+name, f.evidenceBody(nil)), http.StatusForbidden, "insufficient_scope")
	}
	requireProblem(t, f.evidence(nil, "anon", f.evidenceBody(nil)), http.StatusUnauthorized, "")
	requireProblem(t, f.evidence(admin, "", f.evidenceBody(nil)), http.StatusBadRequest, "invalid_idempotency_key")
	if f.backend.ops.count(store.KindFleetExecutorProviderEvidence) != 0 {
		t.Fatal("refused callers record nothing")
	}

	body := f.evidenceBody(nil)
	rec := f.evidence(admin, "e1", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin evidence: %d %s", rec.Code, rec.Body.String())
	}
	op := f.backend.ops.last(store.KindFleetExecutorProviderEvidence)
	var payload store.FleetExecutorProviderEvidencePayload
	if !decodeLedgerValueMap(op.Operation.Payload, &payload) || op.Operation.Ref != f.planID || payload.PlanID != f.planID || payload.SourceAttemptID != f.attempt.ID {
		t.Fatalf("payload: %+v", op.Operation.Payload)
	}
	if digest, _ := op.Operation.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string); digest == "" || digest != payload.ProviderEvidence.Digest() {
		t.Fatalf("the server stores the digest of the signed evidence: %+v", op.Operation.Metadata)
	}
	if replay := f.evidence(admin, "e1", body); replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	// A caller-supplied digest is not a field of the request.
	requireProblem(t, f.evidence(admin, "e2", f.evidenceBody(func(b map[string]interface{}) { b["digest"] = "sha256:" + strings.Repeat("0", 64) })), http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest)

	// Evidence while the device is active.
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Platform: executor.Platform}
	requireProblem(t, f.evidence(admin, "e3", f.evidenceBody(nil)), http.StatusConflict, executor.CodeFleetExecutorDeviceActive)
	f.backend.devices[opsDevice] = store.FleetExecutorDeviceFact{Exists: true, Revoked: true, Platform: executor.Platform}
	// Another droplet, stale observation, a source that is not the latest attempt, an unknown source.
	requireProblem(t, f.evidence(admin, "e4", f.evidenceBody(func(b map[string]interface{}) { b["providerEvidence"].(map[string]interface{})["dropletId"] = "1" })), http.StatusBadRequest, executor.CodeFleetProviderEvidenceInvalid)
	requireProblem(t, f.evidence(admin, "e5", f.evidenceBody(func(b map[string]interface{}) {
		b["providerEvidence"].(map[string]interface{})["observedAt"] = time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano)
	})), http.StatusBadRequest, executor.CodeFleetProviderEvidenceInvalid)
	requireProblem(t, f.evidence(admin, "e6", f.evidenceBody(func(b map[string]interface{}) { b["sourceAttemptId"] = uuid.NewString() })), http.StatusConflict, lifecycle.CodeFleetTargetReleaseEvidenceMismatch)
	requireProblem(t, f.evidence(admin, "e7", f.evidenceBody(func(b map[string]interface{}) {
		b["providerEvidence"].(map[string]interface{})["status"] = "running"
	})), http.StatusBadRequest, executor.CodeFleetProviderEvidenceInvalid)
	// H5: a lease that expired minutes ago is inside the 30 minute age gate.
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-10 * time.Minute)
	requireProblem(t, f.evidence(admin, "e8", f.evidenceBody(nil)), http.StatusConflict, lifecycle.CodeFleetTargetAbandonTooSoon)
	f.backend.attempts[0].HeartbeatExpiresAt = time.Now().Add(-2 * time.Hour)
	f.backend.attempts = append(f.backend.attempts, store.FleetExecutorAttemptFact{ID: uuid.NewString(), PlanID: f.planID, Attempt: 2, Executor: true, Status: "running",
		PrincipalSubject: f.attempt.PrincipalSubject, Metadata: f.attempt.Metadata, HeartbeatExpiresAt: time.Now().Add(-2 * time.Hour)})
	requireProblem(t, f.evidence(admin, "e9", f.evidenceBody(nil)), http.StatusConflict, lifecycle.CodeFleetTargetReleaseEvidenceMismatch)
	if f.backend.ops.count(store.KindFleetExecutorProviderEvidence) != 1 {
		t.Fatal("only the first, valid evidence is recorded")
	}
}

// --- the attempt-bound credential ---

func TestFleetExecutorAttemptCredentialIsAttemptBound(t *testing.T) {
	attemptA, attemptB, plan := uuid.NewString(), uuid.NewString(), uuid.NewString()
	credential := func(attempt string) AccessPrincipal {
		return AccessPrincipal{Subject: fleetAttemptSubjectPrefix + attempt, TokenID: "norn_x", Scopes: []string{ScopeFleetAttempt}, ExpiresAt: time.Now().Add(time.Hour), Source: AccessPrincipalSourceManagedToken}
	}
	p := credential(attemptA)
	base := "/api/v1/fleet/plans/" + plan
	if id, ok := FleetAttemptCredentialAttemptID(p); !ok || id != attemptA {
		t.Fatal("the credential names its attempt")
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, base + "/attempts/" + attemptA, true},
		{http.MethodPost, base + "/attempts/" + attemptA + "/heartbeat", true},
		{http.MethodPost, base + "/attempts/" + attemptA + "/advance", true},
		{http.MethodGet, base + "/reconciliations", true},
		{http.MethodPost, base + "/reconciliations", true},
		// another attempt of the same plan
		{http.MethodGet, base + "/attempts/" + attemptB, false},
		{http.MethodPost, base + "/attempts/" + attemptB + "/advance", false},
		// no start, cancel, wrong verb, or anything else
		{http.MethodPost, base + "/attempts", false},
		{http.MethodPost, base + "/attempts/" + attemptA + "/cancel", false},
		{http.MethodPost, base + "/attempts/" + attemptA, false},
		{http.MethodGet, base + "/attempts/" + attemptA + "/advance", false},
		{http.MethodPost, "/api/v1/auth/rotate", false},
		{http.MethodPost, "/api/v1/auth/revoke", false},
		{http.MethodPost, base + "/credential-grants", false},
		{http.MethodPost, "/api/v1/fleet/executor/stop-attestations", false},
		{http.MethodGet, "/api/v1/fleet/resources/prod", false},
		{http.MethodGet, base + "/attempts/" + attemptA + "/../" + attemptB, false},
	} {
		if got := FleetAttemptCredentialAllowsRoute(p, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: got %v want %v", tc.method, tc.path, got, tc.want)
		}
	}
	if !FleetAttemptCredentialMayActOn(p, attemptA) || FleetAttemptCredentialMayActOn(p, attemptB) || FleetAttemptCredentialMayActOn(p, "") {
		t.Fatal("a checkpoint body may name only the bound attempt")
	}
	// Anything that is not exactly the credential is not it.
	expired := credential(attemptA)
	expired.ExpiresAt = time.Now().Add(-time.Second)
	withDevice := credential(attemptA)
	withDevice.DeviceID = "dev-1"
	extraScope := credential(attemptA)
	extraScope.Scopes = []string{ScopeFleetAttempt, ScopeAPIRead}
	badSubject := credential("not-a-uuid")
	ci := credential(attemptA)
	ci.CI = &CIIdentity{RunID: "1"}
	for name, bad := range map[string]AccessPrincipal{"expired": expired, "device claim": withDevice, "extra scope": extraScope, "bad subject": badSubject, "ci": ci, "executor": executorPrincipal()} {
		if _, ok := FleetAttemptCredentialAttemptID(bad); ok || FleetAttemptCredentialAllowsRoute(bad, http.MethodGet, base+"/attempts/"+attemptA) {
			t.Errorf("%s must not be the attempt credential", name)
		}
	}
	// It is not an executor principal and carries no scope any other route needs.
	if IsFleetExecutorPrincipal(p) {
		t.Fatal("not an executor principal")
	}
	for _, scope := range []string{ScopeAPIRead, ScopeAPIWrite, ScopeAdmin, ScopeFleetExecute, ScopeFleetOperate, ScopePlatformOperate} {
		if p.Allows(scope) {
			t.Errorf("must not allow %s", scope)
		}
	}
	// The scope is not in the public table, so no admin request or enrollment mints it.
	if _, err := normalizeAccessTokenScopes([]string{ScopeFleetAttempt}); err == nil {
		t.Fatal("fleet:attempt must not be a requestable scope")
	}
}

type deviceLookupFails struct{ store.AuthStore }

func (deviceLookupFails) ActiveAccessDevice(context.Context, string) (*store.AccessDevice, error) {
	return nil, store.ErrIdentityNotFound
}

// TestFleetExecutorAttemptCredentialCannotRotate drives the real /auth/rotate
// handler with the principal verification builds from a minted credential: it
// has no device claim, so rotation finds no device and issues nothing (the
// embedded nil AuthStore would panic on RotateAccessToken).
func TestFleetExecutorAttemptCredentialCannotRotate(t *testing.T) {
	f := newOpsFixture(t, executor.GrantProfilePostgresLegacyNoDrain, "inventory_generated")
	grant := decodeGrant(t, f.grant(opsExecutorPrincipal(), "rot", f.grantBody("inventory_generated", executor.CredNornAttempt)))
	claims, err := verifyToken(opsTestSecret, grant.AttemptToken, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	principal := AccessPrincipal{Subject: claims.Sub, TokenID: claims.Jti, DeviceID: claims.Did, Scopes: claims.Scopes, ExpiresAt: time.Unix(claims.Exp, 0), Source: AccessPrincipalSourceManagedToken}
	req := WithAccessPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/auth/rotate", nil), &principal)
	rec := httptest.NewRecorder()
	RotateManagedToken(&config.Config{APIToken: opsTestSecret}, deviceLookupFails{}, nil, rec, req)
	if rec.Code < 400 || strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("an attempt credential must not rotate: %d %s", rec.Code, rec.Body.String())
	}
	if FleetAttemptCredentialAllowsRoute(principal, http.MethodPost, "/api/v1/auth/rotate") {
		t.Fatal("the route matcher must refuse /auth/rotate")
	}
}

// --- providerEvidence on release and abandon-plan ---

func TestFleetTargetExecutorLaneReleaseCarriesProviderEvidence(t *testing.T) {
	router, backend, observer := newFakeTargetRouter(t)
	backend.binding = FleetTargetHolderBinding{ExecutorLane: true}
	evidence := map[string]interface{}{"provider": "digitalocean", "dropletId": opsDroplet, "status": "not_found", "observedAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}
	admin := fleetTargetPrincipal(ScopeAdmin)

	rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/"+fakeTargetID+"/fence/release", admin, "x1", map[string]interface{}{
		"mode": "terminal", "expectedGeneration": 3, "providerEvidence": evidence,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
	accepted := backend.operations.last
	if accepted.FleetTargetMutation == nil || accepted.FleetTargetMutation.Release.ProviderEvidence == nil || accepted.FleetTargetMutation.Release.ProviderEvidence.DropletID != opsDroplet {
		t.Fatalf("the evidence rides in the signed admission: %+v", accepted.FleetTargetMutation)
	}
	if accepted.FleetTargetReleaseEvidence != nil {
		t.Fatal("providerEvidence is never FleetTargetReleaseEvidence")
	}
	if digest, _ := accepted.Operation.Metadata[executor.ProviderEvidenceDigestMetadataKey].(string); digest != accepted.FleetTargetMutation.Release.ProviderEvidence.Digest() {
		t.Fatalf("digest metadata: %+v", accepted.Operation.Metadata)
	}
	rec = fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "x2", map[string]interface{}{
		"planId": fakeHolderPlanID, "providerEvidence": evidence,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("abandon-plan: %d %s", rec.Code, rec.Body.String())
	}
	if backend.operations.last.FleetTargetMutation.Release.ProviderEvidence == nil {
		t.Fatal("abandon-plan carries the evidence")
	}
	if observer.calls() != 0 {
		t.Fatalf("an executor-lane holder has no GitHub run to observe (%d observer calls)", observer.calls())
	}

	// A caller-supplied digest is refused as an unknown field; the stored one is the server's.
	bad := map[string]interface{}{"provider": "digitalocean", "dropletId": "1", "status": "off", "observedAt": evidence["observedAt"], "digest": "sha256:0"}
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", admin, "x3", map[string]interface{}{"planId": fakeHolderPlanID, "providerEvidence": bad}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown evidence field: %d", rec.Code)
	}
	// Admin only, as before.
	if rec := fleetTargetDo(router, http.MethodPost, "/api/v1/fleet/targets/abandon-plan", fleetTargetPrincipal(ScopePlatformOperate), "x4", map[string]interface{}{"planId": fakeHolderPlanID, "providerEvidence": evidence}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin: %d", rec.Code)
	}
}
