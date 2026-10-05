package handler

// Executor-lane credential grants, stop attestations and provider evidence
// (plan-5-7 work package E5, ADR 0010). Route wiring and the scope matcher
// belong to E6; this file defines the handlers, both backend adapters and the
// attempt-bound credential:
//
//	POST /api/v1/fleet/plans/{planID}/credential-grants       (Grant, executor)
//	POST /api/v1/fleet/executor/stop-attestations             (StopAttestation, executor)
//	POST /api/v1/fleet/plans/{planID}/provider-evidence       (ProviderEvidence, admin)
//	POST /api/v1/fleet/plans/{planID}/converge-refusals       (Refuse, executor)
//
// Each records one signed ledger operation through the backend's
// OperationStore, the same acceptance the target routes use. Every decision is
// the backend-neutral function in package store, over facts a backend reads;
// the recovery admission (E2) and the release transaction re-decide against
// fresh facts, so a route-time check is defence in depth, not the only gate.
//
// The norn-attempt credential (E1/P1 hand-off). A grant that includes
// norn-attempt must not hand the phase the executor's device token, which
// carries fleet:execute and can call /auth/rotate. Norn instead mints a
// managed token whose only scope is ScopeFleetAttempt and whose subject is
// "fleet-attempt:<attemptId>". It expires with the grant, is recorded against
// the executor device (so DELETE /devices/{id} revokes it), carries no device
// claim (so /auth/rotate cannot find a device to rotate it onto), and the
// previous grant's token for the same attempt is revoked before the next is
// minted; every new grant for the plan revokes every earlier one. The scope is deliberately absent from the public scope table
// (accessTokenScopes), so no enrollment or admin token request can ask for it.
// FleetAttemptCredentialAllowsRoute is the exact route set it may reach.

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
	"github.com/jackc/pgx/v5"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const (
	// ScopeFleetAttempt is the attempt-bound credential's only scope. It is
	// not in accessTokenScopes: only the grant route issues it.
	ScopeFleetAttempt = executor.AttemptScope

	fleetAttemptSubjectPrefix = "fleet-attempt:"

	grantMetadataTokenID  = "attemptTokenId"
	grantMetadataIssuedAt = "attemptTokenIssuedAt"
)

// FleetAttemptCredentialRegistry is the token registry the attempt-bound
// credential is recorded in and revoked from (*store.DB and
// *etcdstore.AuthStore).
type FleetAttemptCredentialRegistry interface {
	RecordAccessToken(ctx context.Context, token *store.AccessToken) error
	RevokeAccessToken(ctx context.Context, jti string) ([]string, error)
}

// FleetExecutorOpsBackend is the per-backend adapter behind the E5 routes.
type FleetExecutorOpsBackend interface {
	store.FleetExecutorOpsReader
	OperationStore() store.OperationStore
	// Actor resolves the stable operation actor for a scope-checked principal,
	// writing the problem response itself on refusal.
	Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool)
	// GrantProfile names the phase list of the backend's executor attempts.
	GrantProfile() executor.GrantProfile
	// AttemptCredentials is nil when the token registry is unavailable.
	AttemptCredentials() FleetAttemptCredentialRegistry
}

// FleetExecutorOpsRoutes serves the E5 routes for one backend.
type FleetExecutorOpsRoutes struct {
	backend FleetExecutorOpsBackend
	secret  string
	now     func() time.Time
}

// NewFleetExecutorOpsRoutes builds the routes. secret signs the
// attempt-bound credential (the control API token secret).
func NewFleetExecutorOpsRoutes(backend FleetExecutorOpsBackend, secret string) *FleetExecutorOpsRoutes {
	return &FleetExecutorOpsRoutes{backend: backend, secret: secret, now: func() time.Time { return time.Now().UTC() }}
}

// FleetExecutorOpsRoutes returns the PostgreSQL routes.
func (h *Handler) FleetExecutorOpsRoutes() *FleetExecutorOpsRoutes {
	secret := ""
	if h.cfg != nil {
		secret = h.cfg.APIToken
	}
	return NewFleetExecutorOpsRoutes(&pgFleetExecutorOpsBackend{targets: pgFleetTargetBackend{h: h}, h: h}, secret)
}

// NewEtcdFleetExecutorOpsRoutes returns the etcd routes.
func NewEtcdFleetExecutorOpsRoutes(cfg *config.Config, operations *etcdstore.V3OperationStore, auth *etcdstore.AuthStore) *FleetExecutorOpsRoutes {
	secret := ""
	if cfg != nil {
		secret = cfg.APIToken
	}
	return NewFleetExecutorOpsRoutes(&etcdFleetExecutorOpsBackend{targets: etcdFleetTargetBackend{cfg: cfg, operations: operations}, operations: operations, auth: auth}, secret)
}

// --- backend adapters ---

type pgFleetExecutorOpsBackend struct {
	targets pgFleetTargetBackend
	h       *Handler
}

func (b *pgFleetExecutorOpsBackend) OperationStore() store.OperationStore {
	return b.targets.OperationStore()
}

func (b *pgFleetExecutorOpsBackend) Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	return b.targets.Actor(w, r, principal)
}

func (b *pgFleetExecutorOpsBackend) GrantProfile() executor.GrantProfile {
	return executor.GrantProfilePostgresLegacyNoDrain
}

func (b *pgFleetExecutorOpsBackend) AttemptCredentials() FleetAttemptCredentialRegistry {
	if b.h == nil || b.h.db == nil {
		return nil
	}
	return b.h.db
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorAttempts(ctx context.Context, planID string) ([]store.FleetExecutorAttemptFact, error) {
	return b.h.db.FleetExecutorAttempts(ctx, planID)
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorAttemptByID(ctx context.Context, attemptID string) (*store.FleetExecutorAttemptFact, error) {
	return b.h.db.FleetExecutorAttemptByID(ctx, attemptID)
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorDevice(ctx context.Context, deviceID string) (store.FleetExecutorDeviceFact, error) {
	return b.h.db.FleetExecutorDevice(ctx, deviceID)
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorPlanIsConverge(ctx context.Context, planID string) (bool, error) {
	return b.h.db.FleetExecutorPlanIsConverge(ctx, planID)
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorConvergePlan(ctx context.Context, planID string) (*store.FleetExecutorConvergePlanFact, error) {
	return b.h.db.FleetExecutorConvergePlan(ctx, planID)
}

func (b *pgFleetExecutorOpsBackend) FleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]store.FleetExecutorLedgerRow, error) {
	return b.h.db.FleetExecutorLedgerRows(ctx, kind, planID)
}

type etcdFleetExecutorOpsBackend struct {
	targets    etcdFleetTargetBackend
	operations *etcdstore.V3OperationStore
	auth       *etcdstore.AuthStore
}

func (b *etcdFleetExecutorOpsBackend) OperationStore() store.OperationStore {
	return b.targets.OperationStore()
}

func (b *etcdFleetExecutorOpsBackend) Actor(w http.ResponseWriter, r *http.Request, principal AccessPrincipal) (store.OperationActor, store.AcceptanceAuditContext, bool) {
	return b.targets.Actor(w, r, principal)
}

func (b *etcdFleetExecutorOpsBackend) GrantProfile() executor.GrantProfile {
	return executor.GrantProfileV3
}

func (b *etcdFleetExecutorOpsBackend) AttemptCredentials() FleetAttemptCredentialRegistry {
	if b.auth == nil {
		return nil
	}
	return b.auth
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorAttempts(ctx context.Context, planID string) ([]store.FleetExecutorAttemptFact, error) {
	return b.operations.FleetExecutorAttempts(ctx, planID)
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorAttemptByID(ctx context.Context, attemptID string) (*store.FleetExecutorAttemptFact, error) {
	return b.operations.FleetExecutorAttemptByID(ctx, attemptID)
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorDevice(ctx context.Context, deviceID string) (store.FleetExecutorDeviceFact, error) {
	return b.operations.FleetExecutorDevice(ctx, deviceID)
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorPlanIsConverge(ctx context.Context, planID string) (bool, error) {
	return b.operations.FleetExecutorPlanIsConverge(ctx, planID)
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorConvergePlan(ctx context.Context, planID string) (*store.FleetExecutorConvergePlanFact, error) {
	return b.operations.FleetExecutorConvergePlan(ctx, planID)
}

func (b *etcdFleetExecutorOpsBackend) FleetExecutorLedgerRows(ctx context.Context, kind, planID string) ([]store.FleetExecutorLedgerRow, error) {
	return b.operations.FleetExecutorLedgerRows(ctx, kind, planID)
}

// --- shared route steps ---

func writeFleetExecutorOpsError(w http.ResponseWriter, r *http.Request, err error) {
	var fenceErr *lifecycle.FenceError
	switch {
	case errors.As(err, &fenceErr):
		WriteControlProblem(w, r, FleetTargetFenceStatus(fenceErr.Code), fenceErr.Code, fenceErr.Error())
	default:
		var refusal *executor.Error
		if errors.As(err, &refusal) {
			writeExecutorError(w, r, err)
			return
		}
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_executor_facts_unavailable", "fleet executor facts could not be read")
	}
}

// requireExecutor admits only the executor principal on a live fleet-executor
// device (IsFleetExecutorPrincipal plus the E2/E5 defence-in-depth platform
// check). CI, admin and every other principal are refused.
func (s *FleetExecutorOpsRoutes) requireExecutor(w http.ResponseWriter, r *http.Request) (AccessPrincipal, bool) {
	if s == nil || s.backend == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return AccessPrincipal{}, false
	}
	principal, present := AccessPrincipalFromRequest(r)
	if !present {
		WriteControlProblem(w, r, http.StatusUnauthorized, "authenticated_principal_required", "an explicitly authenticated control principal is required")
		return AccessPrincipal{}, false
	}
	if !IsFleetExecutorPrincipal(principal) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "only the fleet executor principal may call this route")
		return AccessPrincipal{}, false
	}
	device, err := s.backend.FleetExecutorDevice(r.Context(), principal.DeviceID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "device_lookup_failed", "the executor device could not be read")
		return AccessPrincipal{}, false
	}
	if !device.Exists || device.Revoked || !isFleetExecutorPlatform(device.Platform) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "the executor lane requires a live "+FleetExecutorPlatform+" device")
		return AccessPrincipal{}, false
	}
	return principal, true
}

func requirePlanID(w http.ResponseWriter, r *http.Request) (string, bool) {
	planID := chi.URLParam(r, "planID")
	if _, err := uuid.Parse(planID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, "planID must be a UUID")
		return "", false
	}
	return planID, true
}

// ledgerIdentity resolves the actor and the request identity of one ledger
// operation. ok is false when it already wrote the problem response.
func (s *FleetExecutorOpsRoutes) ledgerIdentity(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, kind, ref, key string) (store.OperationRequestIdentity, store.AcceptanceAuditContext, bool) {
	operations := s.backend.OperationStore()
	if operations == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return store.OperationRequestIdentity{}, store.AcceptanceAuditContext{}, false
	}
	actor, audit, ok := s.backend.Actor(w, r, principal)
	if !ok {
		return store.OperationRequestIdentity{}, store.AcceptanceAuditContext{}, false
	}
	audit.Scopes = append([]string(nil), principal.Scopes...)
	authority, err := operations.Authority(r.Context())
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "control authority is unavailable")
		return store.OperationRequestIdentity{}, store.AcceptanceAuditContext{}, false
	}
	return store.OperationRequestIdentity{Authority: authority, Actor: actor, Kind: kind, Resource: ref, Key: key}, audit, true
}

// acceptLedger records one completed-receipt ledger operation (Ref = ref)
// through the signed acceptance. The payload and metadata must be stable
// across retries of one request: the fingerprint covers them.
func (s *FleetExecutorOpsRoutes) acceptLedger(ctx context.Context, identity store.OperationRequestIdentity, audit store.AcceptanceAuditContext, message string, payload, metadata map[string]interface{}) (store.AcceptedOperation, error) {
	now := s.now()
	finished := now
	acceptance := store.OperationAcceptance{
		Identity: identity, Audit: audit,
		Operation: model.Operation{
			ID: uuid.NewString(), Kind: identity.Kind, Ref: identity.Resource, Status: model.OperationSucceeded,
			Risk: "records an executor assertion; no provider mutation performed", Source: "control-api", Message: message,
			Payload: payload, Metadata: metadata, StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1,
		},
	}
	fingerprint, err := store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance.Fingerprint = fingerprint
	return s.backend.OperationStore().Accept(ctx, acceptance)
}

func writeLedgerOperation(w http.ResponseWriter, accepted store.AcceptedOperation) {
	accepted.Operation.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	status := http.StatusCreated
	if accepted.Replayed {
		status = http.StatusOK
	}
	writeJSONStatus(w, status, accepted.Operation)
}

func decodeLedgerValueMap(value interface{}, target interface{}) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, target) == nil
}

func stringFromAny(value interface{}) string {
	text, _ := value.(string)
	return text
}

func ledgerMap(value interface{}) map[string]interface{} {
	var out map[string]interface{}
	if !decodeLedgerValueMap(value, &out) {
		return map[string]interface{}{}
	}
	return out
}

// --- credential grants ---

func grantIncludesAttemptCredential(types []string) bool {
	for _, t := range types {
		if t == executor.CredNornAttempt {
			return true
		}
	}
	return false
}

func newFleetAttemptToken(deviceID, attemptID string, issuedAt, expiresAt time.Time) *store.AccessToken {
	return &store.AccessToken{
		JTI: "norn_" + uuid.NewString(), DeviceID: deviceID, Subject: fleetAttemptSubjectPrefix + attemptID,
		Scopes: []string{ScopeFleetAttempt}, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}
}

// signFleetAttemptToken signs the record's bearer. It deliberately sets no
// device claim: /auth/rotate resolves the caller's device from it, so this
// credential cannot be rotated into a longer-lived one.
func signFleetAttemptToken(secret string, record *store.AccessToken) (string, error) {
	return signToken(secret, tokenClaims{
		Sub: record.Subject, Iss: "norn", Aud: "norn-control", Use: "access", Managed: true,
		Iat: record.IssuedAt.Unix(), Exp: record.ExpiresAt.Unix(), Jti: record.JTI, Scopes: []string{ScopeFleetAttempt},
	})
}

// FleetAttemptCredentialAttemptID returns the attempt a principal is bound to
// when it is the attempt-bound credential: a live managed token, exact scope
// ScopeFleetAttempt, no CI claim and no device claim, subject
// fleet-attempt:<uuid>.
func FleetAttemptCredentialAttemptID(p AccessPrincipal) (string, bool) {
	if p.Source != AccessPrincipalSourceManagedToken || p.Legacy || p.TokenID == "" || p.DeviceID != "" || p.CI != nil ||
		len(p.Scopes) != 1 || p.Scopes[0] != ScopeFleetAttempt || p.ExpiresAt.IsZero() || !time.Now().Before(p.ExpiresAt) {
		return "", false
	}
	attemptID, ok := strings.CutPrefix(p.Subject, fleetAttemptSubjectPrefix)
	if !ok {
		return "", false
	}
	if _, err := uuid.Parse(attemptID); err != nil {
		return "", false
	}
	return attemptID, true
}

// FleetAttemptCredentialMayActOn reports whether p is the attempt-bound
// credential for exactly attemptID. A handler that reads an attempt ID from
// the body (a checkpoint) must call it.
func FleetAttemptCredentialMayActOn(p AccessPrincipal, attemptID string) bool {
	bound, ok := FleetAttemptCredentialAttemptID(p)
	return ok && attemptID != "" && bound == attemptID
}

var fleetAttemptRouteRe = regexp.MustCompile(`^/api/v1/fleet/plans/[0-9a-fA-F-]{36}/(attempts/([0-9a-fA-F-]{36})(/heartbeat|/advance)?|reconciliations)$`)

// FleetAttemptCredentialAllowsRoute is the exact route set the attempt-bound
// credential may reach, for E6's scope matcher: GET the bound attempt, its
// heartbeat and advance, and the plan's reconciliations (checkpoints; the
// handler binds the body's attemptId with FleetAttemptCredentialMayActOn).
// Attempt start, cancel, every other plan route, /auth/rotate and every
// other API are refused.
func FleetAttemptCredentialAllowsRoute(p AccessPrincipal, method, path string) bool {
	bound, ok := FleetAttemptCredentialAttemptID(p)
	if !ok {
		return false
	}
	match := fleetAttemptRouteRe.FindStringSubmatch(path)
	if match == nil {
		return false
	}
	if match[2] == "" { // reconciliations
		return method == http.MethodGet || method == http.MethodPost
	}
	if !strings.EqualFold(match[2], bound) {
		return false
	}
	switch match[3] {
	case "":
		return method == http.MethodGet
	default:
		return method == http.MethodPost
	}
}

// revokeSupersededAttemptTokens revokes the attempt credential of every
// earlier grant for planID, whatever its attempt or types: one phase runs at a
// time, so a new grant ends the previous one, and a recovery attempt's grant
// ends the dead source attempt's credential too.
func (s *FleetExecutorOpsRoutes) revokeSupersededAttemptTokens(ctx context.Context, registry FleetAttemptCredentialRegistry, planID string) error {
	rows, err := s.backend.FleetExecutorLedgerRows(ctx, executor.KindCredentialGrant, planID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		jti, _ := row.Metadata[grantMetadataTokenID].(string)
		if jti == "" {
			continue
		}
		if _, err := registry.RevokeAccessToken(ctx, jti); err != nil && !errors.Is(err, store.ErrIdentityNotFound) && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

// Grant is POST plans/{planID}/credential-grants (executor principal,
// Idempotency-Key). It requires a live attempt owned by the calling device
// whose currentPhase equals the requested phase (plan: no attempt; recover:
// the latest recovery attempt), and types exactly within the GrantTypes
// table of the attempt's backend. It records fleet.credential-grant and, for
// norn-attempt, mints the attempt-bound credential (see the file comment).
func (s *FleetExecutorOpsRoutes) Grant(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	principal, ok := s.requireExecutor(w, r)
	if !ok {
		return
	}
	planID, ok := requirePlanID(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request executor.CredentialGrantRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
		return
	}
	request = request.Normalize()
	identity, audit, ok := s.ledgerIdentity(w, r, principal, executor.KindCredentialGrant, planID, key)
	if !ok {
		return
	}
	ctx := r.Context()

	// A replay of an accepted grant answers from the ledger before any fact is
	// re-read: the attempt may have moved on since the lost response.
	if resolver, can := s.backend.OperationStore().(store.OperationIdentityResolver); can {
		prior, err := resolver.ResolveIdentity(ctx, identity)
		switch {
		case err == nil:
			s.replayGrant(w, r, identity, request, prior)
			return
		case !errors.Is(err, store.ErrAcceptanceNotFound):
			writeOperationAcceptanceError(w, r, err)
			return
		}
	}

	attempts, err := s.backend.FleetExecutorAttempts(ctx, planID)
	if err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	isConverge := false
	if request.Phase == executor.GrantPhasePlan {
		if isConverge, err = s.backend.FleetExecutorPlanIsConverge(ctx, planID); err != nil {
			writeFleetExecutorOpsError(w, r, err)
			return
		}
	}
	now := s.now().Truncate(time.Second)
	request, attempt, err := store.DecideFleetExecutorGrant(request, s.backend.GrantProfile(), attempts, isConverge, executor.PrincipalSubject(principal.DeviceID), now)
	if err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}

	grant := executor.CredentialGrantPayload{
		GrantID: uuid.NewString(), PlanID: planID, Phase: request.Phase, Types: request.Types,
		DeviceID: principal.DeviceID, TokenID: principal.TokenID, ExpiresAt: now.Add(executor.GrantTTL),
	}
	metadata := map[string]interface{}{"grantId": grant.GrantID}
	var token *store.AccessToken
	var registry FleetAttemptCredentialRegistry
	if attempt != nil {
		grant.AttemptID = attempt.ID
	}
	registry = s.backend.AttemptCredentials()
	if grantIncludesAttemptCredential(request.Types) && (registry == nil || s.secret == "" || attempt == nil) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "token_registry_unavailable", "the attempt-bound credential cannot be issued")
		return
	}
	// The previous grant's credential dies before this grant exists (with or
	// without norn-attempt); a failure aborts the grant with nothing created.
	// Without a registry no credential can have been minted on this backend.
	if registry != nil {
		if err := s.revokeSupersededAttemptTokens(ctx, registry, planID); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "token_revoke_failed", "the previous attempt credential could not be revoked")
			return
		}
	}
	if grantIncludesAttemptCredential(request.Types) {
		token = newFleetAttemptToken(principal.DeviceID, attempt.ID, now, grant.ExpiresAt)
		if err := registry.RecordAccessToken(ctx, token); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "token_registry_unavailable", "the attempt-bound credential could not be recorded")
			return
		}
		metadata[grantMetadataTokenID], metadata[grantMetadataIssuedAt] = token.JTI, now.Format(time.RFC3339)
	}
	accepted, err := s.acceptLedger(ctx, identity, audit, "credential grant recorded", ledgerMap(grant), metadata)
	if err != nil {
		if token != nil {
			_, _ = registry.RevokeAccessToken(ctx, token.JTI)
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	s.writeGrant(w, r, accepted, grant, token)
}

func (s *FleetExecutorOpsRoutes) writeGrant(w http.ResponseWriter, r *http.Request, accepted store.AcceptedOperation, grant executor.CredentialGrantPayload, token *store.AccessToken) {
	response := executor.CredentialGrantResponse{GrantID: grant.GrantID, ExpiresAt: grant.ExpiresAt}
	if token != nil {
		bearer, err := signFleetAttemptToken(s.secret, token)
		if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "failed to sign the attempt-bound credential")
			return
		}
		response.AttemptToken = bearer
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	status := http.StatusCreated
	if accepted.Replayed {
		status = http.StatusOK
	}
	writeJSONStatus(w, status, response)
}

// replayGrant answers a repeated Idempotency-Key from the ledger: the same
// grant, and the same attempt credential re-signed from its durable record.
func (s *FleetExecutorOpsRoutes) replayGrant(w http.ResponseWriter, r *http.Request, identity store.OperationRequestIdentity, request executor.CredentialGrantRequest, prior store.AcceptedOperation) {
	var grant executor.CredentialGrantPayload
	if !decodeLedgerValueMap(prior.Operation.Payload, &grant) || grant.PlanID != identity.Resource || grant.Phase != request.Phase ||
		grant.AttemptID != request.AttemptID || strings.Join(grant.Types, ",") != strings.Join(request.Types, ",") {
		writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{Identity: identity})
		return
	}
	var token *store.AccessToken
	if jti, _ := prior.Operation.Metadata[grantMetadataTokenID].(string); jti != "" {
		issued, err := time.Parse(time.RFC3339, stringFromAny(prior.Operation.Metadata[grantMetadataIssuedAt]))
		if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "token_issue_failed", "the recorded attempt credential is unreadable")
			return
		}
		token = &store.AccessToken{JTI: jti, Subject: fleetAttemptSubjectPrefix + grant.AttemptID, Scopes: []string{ScopeFleetAttempt}, IssuedAt: issued.UTC(), ExpiresAt: grant.ExpiresAt}
	}
	prior.Replayed = true
	s.writeGrant(w, r, prior, grant, token)
}

// --- stop attestations ---

// StopAttestation is POST executor/stop-attestations (executor principal,
// Idempotency-Key). The identity fields are the attester's; the plan is
// derived from the source attempt. It records
// fleet.executor.stop-attestation (Ref = plan) with metadata principalSubject
// after executor.ValidStopAttestation against the source attempt.
func (s *FleetExecutorOpsRoutes) StopAttestation(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireExecutor(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request executor.StopAttestationRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
		return
	}
	if err := request.Validate(); err != nil {
		writeExecutorError(w, r, err)
		return
	}
	source, err := s.backend.FleetExecutorAttemptByID(r.Context(), request.SourceAttemptID)
	if err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	subject := executor.PrincipalSubject(principal.DeviceID)
	if err := store.DecideFleetExecutorStopAttestation(request, source, subject, s.now()); err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	request.ObservedAt = request.ObservedAt.UTC()
	if request.TofuLock != nil {
		lock := *request.TofuLock
		lock.Created = lock.Created.UTC()
		request.TofuLock = &lock
	}
	identity, audit, ok := s.ledgerIdentity(w, r, principal, executor.KindStopAttestation, source.PlanID, key)
	if !ok {
		return
	}
	accepted, err := s.acceptLedger(r.Context(), identity, audit, "stop attestation recorded",
		ledgerMap(executor.StopAttestationPayload{PlanID: source.PlanID, StopAttestationRequest: request}),
		map[string]interface{}{store.FleetExecutorAttesterMetadataKey: subject})
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	writeLedgerOperation(w, accepted)
}

// --- provider evidence ---

// ProviderEvidence is POST plans/{planID}/provider-evidence (admin, non-CI,
// Idempotency-Key). It is non-releasing: it records the admin's assertion that
// the source attempt's droplet is off, archived or gone, so a recovery on a
// rebuilt host may proceed (E2 reads kind fleet.executor.provider-evidence).
// The server checks that the source attempt is the holder plan's latest, that
// the droplet matches, that the attempt's executor device is revoked
// (fleet_executor_device_active otherwise), the H5 30-minute age and
// ProviderEvidence.Validate, and stores the digest under
// fleetProviderEvidenceDigest. The evidence itself is an assertion, not proof.
func (s *FleetExecutorOpsRoutes) ProviderEvidence(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireTargetScope(w, r, ScopeAdmin, true)
	if !ok {
		return
	}
	if s == nil || s.backend == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_acceptance_unavailable", "durable signed operation acceptance is unavailable")
		return
	}
	planID, ok := requirePlanID(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request executor.ProviderEvidenceRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
		return
	}
	if _, err := uuid.Parse(request.SourceAttemptID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, "sourceAttemptId must be a UUID")
		return
	}
	ctx := r.Context()
	attempts, err := s.backend.FleetExecutorAttempts(ctx, planID)
	if err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	var device store.FleetExecutorDeviceFact
	for _, attempt := range attempts {
		if attempt.ID == request.SourceAttemptID {
			if device, err = s.backend.FleetExecutorDevice(ctx, store.FleetExecutorDeviceID(attempt.PrincipalSubject)); err != nil {
				writeFleetExecutorOpsError(w, r, err)
				return
			}
		}
	}
	evidence := request.ProviderEvidence
	evidence.ObservedAt = evidence.ObservedAt.UTC()
	if err := store.DecideFleetExecutorProviderEvidence(evidence, request.SourceAttemptID, attempts, device, s.now()); err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	identity, audit, ok := s.ledgerIdentity(w, r, principal, store.KindFleetExecutorProviderEvidence, planID, key)
	if !ok {
		return
	}
	accepted, err := s.acceptLedger(ctx, identity, audit, "provider evidence recorded",
		ledgerMap(store.FleetExecutorProviderEvidencePayload{PlanID: planID, SourceAttemptID: request.SourceAttemptID, ProviderEvidence: evidence}),
		map[string]interface{}{executor.ProviderEvidenceDigestMetadataKey: evidence.Digest()})
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	writeLedgerOperation(w, accepted)
}

// --- executor refusals ---

// Refuse is POST plans/{planID}/converge-refusals (executor principal,
// Idempotency-Key). It records the signed fleet.converge.refusal operation
// (Ref = plan ID) after the plan checks below; E6 mounts the route and the
// scope matcher admits it through executor.ExecutorRoutes.
//
//   - destructive_requires_github_lane: the plan is a converge plan, commitSha
//     and planSha256 are its own. It is admitted while a recovery attempt of
//     the plan is live too (the recovery reports a destructive remainder),
//     and sets ConvergeView.refused. Idempotency-Key executor-refusal:<planID>.
//   - state_lock_foreign: additionally sourceAttemptId must be the plan's
//     latest executor-lane attempt and not succeeded, the source a recovery
//     may resume (PG sweeps a dead loop's attempt to abandoned on any read,
//     so a queued/running status is not required). It never sets
//     refused; it derives the blocker state_lock_foreign while that attempt
//     stays the latest.
//
// A replay of an accepted key answers from the ledger before any fact is
// re-read (the attempt may have moved on since the lost response); the same
// key with another body is idempotency_key_reused. The principal is not
// matched to the source attempt's device: a rebuilt host's new device reports
// the old device's attempt after the provider evidence.
func (s *FleetExecutorOpsRoutes) Refuse(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireExecutor(w, r)
	if !ok {
		return
	}
	planID, ok := requirePlanID(w, r)
	if !ok {
		return
	}
	key, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request executor.ConvergeRefusalRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, executor.CodeInvalidFleetExecutorRequest, err.Error())
		return
	}
	if err := request.Validate(); err != nil {
		writeExecutorError(w, r, err)
		return
	}
	identity, audit, ok := s.ledgerIdentity(w, r, principal, executor.KindConvergeRefusal, planID, key)
	if !ok {
		return
	}
	ctx := r.Context()
	payload := ledgerMap(executor.ConvergeRefusalPayload{PlanID: planID, ConvergeRefusalRequest: request})
	if resolver, can := s.backend.OperationStore().(store.OperationIdentityResolver); can {
		prior, err := resolver.ResolveIdentity(ctx, identity)
		switch {
		case err == nil:
			if !sameLedgerValue(prior.Operation.Payload, payload) {
				writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{Identity: identity})
				return
			}
			prior.Replayed = true
			writeLedgerOperation(w, prior)
			return
		case !errors.Is(err, store.ErrAcceptanceNotFound):
			writeOperationAcceptanceError(w, r, err)
			return
		}
	}
	planReader, can := s.backend.(store.FleetExecutorConvergePlanReader)
	if !can {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "fleet_executor_facts_unavailable", "fleet executor facts could not be read")
		return
	}
	plan, err := planReader.FleetExecutorConvergePlan(ctx, planID)
	if err != nil {
		writeFleetExecutorOpsError(w, r, err)
		return
	}
	switch {
	case plan == nil:
		writeExecutorError(w, r, &executor.Error{Code: executor.CodeFleetPlanNotConverge, Reason: "the plan is not a converge plan"})
		return
	case plan.CommitSHA != request.CommitSHA:
		writeExecutorError(w, r, &executor.Error{Code: executor.CodeFleetConvergeCommitMismatch, Reason: "commitSha does not match the plan's commit"})
		return
	case plan.PlanSHA256 != request.PlanSHA256:
		writeExecutorError(w, r, &executor.Error{Code: executor.CodeFleetConvergeRefusalRejected, Reason: "planSha256 does not match the plan"})
		return
	}
	if request.Code == executor.RefusalStateLockForeign {
		attempts, err := s.backend.FleetExecutorAttempts(ctx, planID)
		if err != nil {
			writeFleetExecutorOpsError(w, r, err)
			return
		}
		if err := store.DecideFleetExecutorStateLockRefusal(request.SourceAttemptID, attempts); err != nil {
			writeFleetExecutorOpsError(w, r, err)
			return
		}
	}
	accepted, err := s.acceptLedger(ctx, identity, audit, "converge refusal recorded", payload,
		map[string]interface{}{store.FleetExecutorAttesterMetadataKey: executor.PrincipalSubject(principal.DeviceID)})
	if err != nil {
		writeOperationAcceptanceError(w, r, err)
		return
	}
	writeLedgerOperation(w, accepted)
}

// sameLedgerValue compares two ledger payloads by their JSON encoding (map
// keys are sorted by encoding/json).
func sameLedgerValue(a, b interface{}) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}
