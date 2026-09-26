package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

const (
	defaultAcceptanceKeyLimit       = 512
	defaultAcceptanceCanonicalLimit = 1024 * 1024
	defaultAcceptanceResolveTimeout = 3 * time.Second
)

type acceptanceTestHooks struct {
	beforeIntent func() error
	beforeCommit func()
	afterCommit  func() error
}

// PGOperationStore is the PostgreSQL implementation of the atomic acceptance
// boundary. *DB deliberately does not implement OperationStore so new callers
// must provide signing and authority policy explicitly.
type PGOperationStore struct {
	db     *DB
	signer AcceptanceSigner
	policy AcceptancePolicy
	test   acceptanceTestHooks
}

func NewPGOperationStore(db *DB, signer AcceptanceSigner, policy AcceptancePolicy) (*PGOperationStore, error) {
	if policy.ReplayTTL < 0 {
		return nil, &AcceptanceValidationError{Reason: "replay TTL cannot be negative"}
	}
	if db == nil || db.Pool == nil {
		return nil, &AcceptanceValidationError{Reason: "PostgreSQL is unavailable"}
	}
	if signer == nil {
		return nil, &AcceptanceValidationError{Reason: "acceptance signer is required"}
	}
	if policy.MaxIdentityKeyBytes <= 0 {
		policy.MaxIdentityKeyBytes = defaultAcceptanceKeyLimit
	}
	if policy.MaxCanonicalBytes <= 0 {
		policy.MaxCanonicalBytes = defaultAcceptanceCanonicalLimit
	}
	if policy.ResolveTimeout <= 0 {
		policy.ResolveTimeout = defaultAcceptanceResolveTimeout
	}
	if policy.ExpectedAuthority != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(policy.ExpectedAuthority))
		if err != nil {
			return nil, &AcceptanceValidationError{Reason: "expected authority must be a UUID"}
		}
		policy.ExpectedAuthority = parsed.String()
	}
	return &PGOperationStore{db: db, signer: signer, policy: policy}, nil
}

func (s *PGOperationStore) Authority(ctx context.Context) (string, error) {
	if s == nil || s.db == nil || s.db.Pool == nil {
		return "", &AcceptanceAuthorityError{}
	}
	var authority string
	if err := s.db.Pool.QueryRow(ctx, `SELECT authority::text FROM control_plane_identity WHERE singleton=true`).Scan(&authority); err != nil {
		return "", &AcceptanceAuthorityError{}
	}
	if s.policy.ExpectedAuthority != "" && subtle.ConstantTimeCompare([]byte(s.policy.ExpectedAuthority), []byte(authority)) != 1 {
		return "", &AcceptanceAuthorityError{Expected: s.policy.ExpectedAuthority, Actual: authority}
	}
	return authority, nil
}

// CanonicalOperationRequestFingerprint computes the only fingerprint accepted
// by PGOperationStore. New durable IDs and incidental timestamps are excluded;
// every execution-affecting operation/deployment/region/admission field and the
// endpoint-specific Semantics object is included.
func CanonicalOperationRequestFingerprint(acceptance OperationAcceptance) (RequestFingerprint, error) {
	acceptance = normalizeFingerprintSemantics(acceptance)
	material, err := CanonicalOperationRequest(acceptance)
	if err != nil {
		return RequestFingerprint{}, &AcceptanceValidationError{Reason: "canonical request: " + err.Error()}
	}
	digest := sha256.Sum256(material)
	return RequestFingerprint{Version: OperationRequestFingerprintVersion, Digest: hex.EncodeToString(digest[:])}, nil
}

func (s *PGOperationStore) Accept(ctx context.Context, input OperationAcceptance) (AcceptedOperation, error) {
	if input.Identity.Kind == "app.function-invoke" || input.Operation.Kind == "app.function-invoke" {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "function invocation requires atomic private material acceptance"}
	}
	return s.acceptWithPrivateInvocation(ctx, input, nil)
}

func (s *PGOperationStore) acceptWithPrivateInvocation(ctx context.Context, input OperationAcceptance, private *PrivateInvocationEnvelope) (AcceptedOperation, error) {
	return s.acceptWithGuard(ctx, input, private, nil)
}

func (s *PGOperationStore) acceptWithGuard(ctx context.Context, input OperationAcceptance, private *PrivateInvocationEnvelope, guard func(context.Context, pgx.Tx) error) (AcceptedOperation, error) {
	if s == nil || s.signer == nil || s.db == nil || s.db.Pool == nil {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "operation acceptance store is unavailable"}
	}
	acceptance, err := s.normalize(ctx, input)
	if err != nil {
		return AcceptedOperation{}, err
	}

	requestIdentityID := uuid.NewString()
	intentID := uuid.NewString()
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	requestCanonical, err := canonicalRequestMaterial(acceptance)
	if err != nil {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "canonical request: " + err.Error()}
	}

	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AcceptedOperation{}, err
	}
	defer tx.Rollback(context.Background())
	var legacyCollision bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM operations
		WHERE NOT acceptance_required AND metadata->>'idempotencyKey'=$1
	)`, acceptance.Identity.Key).Scan(&legacyCollision); err != nil {
		return AcceptedOperation{}, err
	}
	if legacyCollision {
		return AcceptedOperation{}, &LegacyReplayAmbiguousError{Kind: acceptance.Identity.Kind, Resource: acceptance.Identity.Resource}
	}

	inserted, err := insertRequestIdentity(ctx, tx, requestIdentityID, acceptance, acceptedAt, s.policy.ReplayTTL)
	if err != nil {
		return AcceptedOperation{}, err
	}
	if !inserted {
		_ = tx.Rollback(context.Background())
		return s.resolveFresh(acceptance.Identity, acceptance.Fingerprint)
	}
	if guard != nil {
		if err := guard(ctx, tx); err != nil {
			return AcceptedOperation{}, err
		}
	}
	if acceptance.Admission.OneActiveMutablePerApp {
		if err := enforceActiveAppAdmission(ctx, tx, acceptance.Operation.App); err != nil {
			return AcceptedOperation{}, err
		}
	}
	if acceptance.FleetReconciliation != nil {
		if err := enforceFleetReconciliationAdmission(ctx, tx, acceptance); err != nil {
			return AcceptedOperation{}, err
		}
	}
	if acceptance.FleetRunnerAttempt != nil {
		attempt, err := acceptFleetRunnerAttempt(ctx, tx, acceptance)
		if err != nil {
			return AcceptedOperation{}, err
		}
		acceptance.acceptedFleetRunnerAttempt = attempt
	}
	if err := insertAcceptedDomain(ctx, tx, acceptance); err != nil {
		return AcceptedOperation{}, err
	}
	if private != nil {
		if err := insertPrivateInvocation(ctx, tx, acceptance, *private); err != nil {
			return AcceptedOperation{}, err
		}
	}
	envelope := newAcceptanceEnvelope(acceptance, requestIdentityID, intentID, acceptedAt)
	canonical, signature, err := s.signEnvelope(ctx, &envelope)
	if err != nil {
		return AcceptedOperation{}, err
	}
	if len(canonical) > s.policy.MaxCanonicalBytes {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "signed acceptance envelope exceeds configured limit"}
	}
	if s.test.beforeIntent != nil {
		if err := s.test.beforeIntent(); err != nil {
			return AcceptedOperation{}, err
		}
	}
	if err := insertAcceptanceIntent(ctx, tx, intentID, requestIdentityID, acceptance, acceptedAt, requestCanonical, canonical, signature); err != nil {
		return AcceptedOperation{}, err
	}
	if err := reserveAcceptedEvidence(ctx, tx, acceptance, intentID, requestCanonical, canonical, signature); err != nil {
		return AcceptedOperation{}, err
	}
	if s.test.beforeCommit != nil {
		s.test.beforeCommit()
	}
	if err := tx.Commit(ctx); err != nil {
		resolved, resolveErr := s.resolveFresh(acceptance.Identity, acceptance.Fingerprint)
		if resolveErr == nil {
			return resolved, nil
		}
		return AcceptedOperation{}, &AcceptanceIndeterminateError{Err: errors.Join(err, resolveErr)}
	}
	if s.test.afterCommit != nil {
		if err := s.test.afterCommit(); err != nil {
			resolved, resolveErr := s.resolveFresh(acceptance.Identity, acceptance.Fingerprint)
			if resolveErr == nil {
				return resolved, nil
			}
			return AcceptedOperation{}, &AcceptanceIndeterminateError{Err: errors.Join(err, resolveErr)}
		}
	}
	result := acceptedResult(acceptance, requestIdentityID, intentID, acceptedAt, requestCanonical, canonical, signature, false)
	return result, nil
}

func (s *PGOperationStore) Resolve(ctx context.Context, identity OperationRequestIdentity, fingerprint RequestFingerprint) (AcceptedOperation, error) {
	if s == nil || s.signer == nil || s.db == nil || s.db.Pool == nil {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "operation acceptance store is unavailable"}
	}
	authority, err := s.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	identity, err = normalizeIdentity(identity, authority, s.policy.MaxIdentityKeyBytes)
	if err != nil {
		return AcceptedOperation{}, err
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return AcceptedOperation{}, err
	}

	record, err := s.loadAcceptance(ctx, identity)
	if errors.Is(err, pgx.ErrNoRows) {
		if expired, expiryErr := s.expiredIdentity(ctx, identity, fingerprint); expiryErr != nil || expired {
			return AcceptedOperation{}, expiryErr
		}
		return AcceptedOperation{}, &AcceptanceNotFoundError{Identity: identity}
	}
	if err != nil {
		return AcceptedOperation{}, err
	}
	if !sameFingerprint(record.intent.Fingerprint, fingerprint) {
		return AcceptedOperation{}, &AcceptanceConflictError{Identity: identity}
	}
	if err := s.verifyLoadedAcceptance(ctx, identity, record); err != nil {
		return AcceptedOperation{}, err
	}
	if err := s.expireReplayIfEligible(ctx, identity, record.intent.RequestIdentityID); err != nil {
		return AcceptedOperation{}, err
	}
	result := AcceptedOperation{
		Operation: record.operation, Deployment: record.deployment, Regions: record.regions,
		RequestIdentityID: record.intent.RequestIdentityID, AcceptanceIntentID: record.intent.ID,
		Replayed: true, Intent: record.intent, FleetRunnerAttempt: record.fleetRunnerAttempt,
	}
	return result, nil
}

// expiredIdentity preserves the replay namespace after archive-backed
// payload retirement. The tombstone retains only the immutable request
// fingerprint, enough to distinguish a replay from a collision without
// reintroducing the signed payload into the hot store.
func (s *PGOperationStore) expiredIdentity(ctx context.Context, identity OperationRequestIdentity, fingerprint RequestFingerprint) (bool, error) {
	var stored RequestFingerprint
	var expiresAt time.Time
	err := s.db.Pool.QueryRow(ctx, `SELECT fingerprint_version, fingerprint_digest, replay_expires_at
		FROM operation_request_identities
		WHERE authority=$1::uuid AND actor_issuer=$2 AND actor_subject=$3 AND kind=$4 AND resource=$5 AND request_key=$6
		  AND replay_contract_version=$7 AND replay_expired_at IS NOT NULL`,
		identity.Authority, identity.Actor.Issuer, identity.Actor.Subject, identity.Kind, identity.Resource, identity.Key,
		OperationReplayContractVersion).Scan(&stored.Version, &stored.Digest, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !sameFingerprint(stored, fingerprint) {
		return true, &AcceptanceConflictError{Identity: identity}
	}
	return true, &AcceptanceExpiredError{Identity: identity, ExpiresAt: expiresAt}
}

func (s *PGOperationStore) ResolveIdentity(ctx context.Context, identity OperationRequestIdentity) (AcceptedOperation, error) {
	authority, err := s.Authority(ctx)
	if err != nil {
		return AcceptedOperation{}, err
	}
	identity, err = normalizeIdentity(identity, authority, s.policy.MaxIdentityKeyBytes)
	if err != nil {
		return AcceptedOperation{}, err
	}
	var fingerprint RequestFingerprint
	err = s.db.Pool.QueryRow(ctx, `SELECT fingerprint_version,fingerprint_digest FROM operation_request_identities WHERE authority=$1::uuid AND actor_issuer=$2 AND actor_subject=$3 AND kind=$4 AND resource=$5 AND request_key=$6`, identity.Authority, identity.Actor.Issuer, identity.Actor.Subject, identity.Kind, identity.Resource, identity.Key).Scan(&fingerprint.Version, &fingerprint.Digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptedOperation{}, &AcceptanceNotFoundError{Identity: identity}
	}
	if err != nil {
		return AcceptedOperation{}, err
	}
	return s.Resolve(ctx, identity, fingerprint)
}

// VerifyAcceptedOperation loads signed acceptance by its durable operation
// identity without consuming or extending the original replay window. Recovery
// callers must still check the operation and deployment's current state. A
// retired or missing hot acceptance fails closed until archived evidence can
// be verified through the recovery path.
func (s *PGOperationStore) VerifyAcceptedOperation(ctx context.Context, operationID string) (AcceptedOperation, error) {
	if s == nil || s.signer == nil || s.db == nil || s.db.Pool == nil || operationID == "" {
		return AcceptedOperation{}, &AcceptanceValidationError{Reason: "operation acceptance store is unavailable"}
	}
	var identity OperationRequestIdentity
	err := s.db.Pool.QueryRow(ctx, `SELECT authority::text,actor_issuer,actor_subject,kind,resource,request_key
		FROM operation_request_identities WHERE operation_id=$1`, operationID).Scan(
		&identity.Authority, &identity.Actor.Issuer, &identity.Actor.Subject, &identity.Kind, &identity.Resource, &identity.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptedOperation{}, &AcceptanceNotFoundError{Identity: identity}
	}
	if err != nil {
		return AcceptedOperation{}, err
	}
	record, err := s.loadAcceptance(ctx, identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptedOperation{}, &AcceptanceNotFoundError{Identity: identity}
	}
	if err != nil {
		return AcceptedOperation{}, err
	}
	if record.operation.ID != operationID {
		return AcceptedOperation{}, &AcceptanceSignatureError{Err: fmt.Errorf("acceptance operation identity changed")}
	}
	if err := s.verifyLoadedAcceptance(ctx, identity, record); err != nil {
		return AcceptedOperation{}, err
	}
	return AcceptedOperation{
		Operation: record.operation, Deployment: record.deployment, Regions: record.regions,
		RequestIdentityID: record.intent.RequestIdentityID, AcceptanceIntentID: record.intent.ID,
		Intent: record.intent, FleetRunnerAttempt: record.fleetRunnerAttempt,
	}, nil
}

func (s *PGOperationStore) normalize(ctx context.Context, input OperationAcceptance) (OperationAcceptance, error) {
	authority, err := s.Authority(ctx)
	if err != nil {
		return OperationAcceptance{}, err
	}
	input.Identity, err = normalizeIdentity(input.Identity, authority, s.policy.MaxIdentityKeyBytes)
	if err != nil {
		return OperationAcceptance{}, err
	}
	if err := NormalizeAcceptanceAudit(&input.Audit); err != nil {
		return OperationAcceptance{}, err
	}
	if err := normalizeOperationDomain(&input); err != nil {
		return OperationAcceptance{}, err
	}
	if err := normalizeFleetReconciliationAcceptance(&input); err != nil {
		return OperationAcceptance{}, err
	}
	if err := normalizeFleetRunnerAttemptAcceptance(&input); err != nil {
		return OperationAcceptance{}, err
	}
	want, err := CanonicalOperationRequestFingerprint(input)
	if err != nil {
		return OperationAcceptance{}, err
	}
	if err := validateFingerprint(input.Fingerprint); err != nil {
		return OperationAcceptance{}, err
	}
	if !sameFingerprint(want, input.Fingerprint) {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "request fingerprint does not match accepted operation semantics"}
	}
	requestBytes, err := CanonicalOperationRequest(input)
	if err != nil {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "canonical request: " + err.Error()}
	}
	if len(requestBytes) > s.policy.MaxCanonicalBytes {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "canonical request exceeds configured limit"}
	}
	return input, nil
}

// NormalizeAcceptanceAudit applies the bounded, canonical audit semantics
// shared by every durable control backend before request fingerprinting.
func NormalizeAcceptanceAudit(audit *AcceptanceAuditContext) error {
	if audit == nil {
		return &AcceptanceValidationError{Reason: "audit context is required"}
	}
	audit.Source = strings.TrimSpace(audit.Source)
	if audit.Source == "" {
		return &AcceptanceValidationError{Reason: "audit source is required"}
	}
	if len(audit.Source) > 256 || len(audit.RequestID) > 512 || len(audit.RequestReceiptID) > 512 || len(audit.CredentialID) > 512 || len(audit.DeviceID) > 512 {
		return &AcceptanceValidationError{Reason: "audit evidence exceeds configured bounds"}
	}
	audit.Scopes = sortedUniqueStrings(audit.Scopes)
	if len(audit.Scopes) > 256 {
		return &AcceptanceValidationError{Reason: "too many audit scopes"}
	}
	return nil
}

// CanonicalOperationRequest returns the exact fingerprinted request bytes.
// Backends must enforce their admission size limit before persisting them.
func CanonicalOperationRequest(acceptance OperationAcceptance) ([]byte, error) {
	return canonicalRequestMaterial(acceptance)
}

func normalizeIdentity(identity OperationRequestIdentity, authority string, keyLimit int) (OperationRequestIdentity, error) {
	identity.Authority = strings.TrimSpace(identity.Authority)
	identity.Actor.Issuer = strings.TrimSpace(identity.Actor.Issuer)
	identity.Actor.Subject = strings.TrimSpace(identity.Actor.Subject)
	identity.Kind = strings.TrimSpace(identity.Kind)
	identity.Resource = strings.TrimSpace(identity.Resource)
	identity.Key = strings.TrimSpace(identity.Key)
	if identity.Authority == "" || identity.Actor.Issuer == "" || identity.Actor.Subject == "" || identity.Kind == "" || identity.Resource == "" || identity.Key == "" {
		return OperationRequestIdentity{}, &AcceptanceValidationError{Reason: "authority, actor issuer/subject, kind, resource, and key are required"}
	}
	parsed, err := uuid.Parse(identity.Authority)
	if err != nil || parsed.String() != authority {
		return OperationRequestIdentity{}, &AcceptanceAuthorityError{Expected: authority, Actual: identity.Authority}
	}
	identity.Authority = parsed.String()
	if len(identity.Key) > keyLimit || len(identity.Actor.Issuer) > 512 || len(identity.Actor.Subject) > 512 || len(identity.Kind) > 256 || len(identity.Resource) > 1024 {
		return OperationRequestIdentity{}, &AcceptanceValidationError{Reason: "request identity exceeds configured bounds"}
	}
	return identity, nil
}

func validateFingerprint(fingerprint RequestFingerprint) error {
	if fingerprint.Version != OperationRequestFingerprintVersion {
		return &AcceptanceValidationError{Reason: "unsupported request fingerprint version"}
	}
	if len(fingerprint.Digest) != 64 || strings.ToLower(fingerprint.Digest) != fingerprint.Digest {
		return &AcceptanceValidationError{Reason: "request fingerprint must be a lowercase SHA-256 digest"}
	}
	if _, err := hex.DecodeString(fingerprint.Digest); err != nil {
		return &AcceptanceValidationError{Reason: "request fingerprint must be a lowercase SHA-256 digest"}
	}
	return nil
}

func sameFingerprint(a, b RequestFingerprint) bool {
	return a.Version == b.Version && subtle.ConstantTimeCompare([]byte(a.Digest), []byte(b.Digest)) == 1
}

func normalizeOperationDomain(acceptance *OperationAcceptance) error {
	op := &acceptance.Operation
	op.ID = strings.TrimSpace(op.ID)
	op.Kind = strings.TrimSpace(op.Kind)
	if op.ID == "" || op.Kind == "" || op.Kind != acceptance.Identity.Kind {
		return &AcceptanceValidationError{Reason: "operation ID and identity-matching kind are required"}
	}
	if op.Status == "" {
		op.Status = model.OperationQueued
	}
	if op.Status != model.OperationQueued && !op.Status.Terminal() {
		return &AcceptanceValidationError{Reason: "accepted operation must be queued or terminal"}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if op.StartedAt.IsZero() {
		op.StartedAt = now
	} else {
		op.StartedAt = op.StartedAt.UTC().Truncate(time.Microsecond)
	}
	if op.MaxAttempts <= 0 {
		op.MaxAttempts = 1
	}
	if op.NextAttemptAt.IsZero() {
		op.NextAttemptAt = op.StartedAt
	} else {
		op.NextAttemptAt = op.NextAttemptAt.UTC().Truncate(time.Microsecond)
	}
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	if _, exists := op.Metadata["idempotencyKey"]; exists {
		return &AcceptanceValidationError{Reason: "new acceptance cannot use the legacy global idempotency key"}
	}
	if op.Status.Terminal() {
		if op.FinishedAt == nil {
			finished := op.StartedAt
			op.FinishedAt = &finished
		} else {
			finished := op.FinishedAt.UTC().Truncate(time.Microsecond)
			op.FinishedAt = &finished
		}
	} else if op.FinishedAt != nil || op.Attempts != 0 || op.LockedBy != "" || op.LockGeneration != 0 || op.LockedUntil != nil {
		return &AcceptanceValidationError{Reason: "new queued operation cannot carry execution ownership or terminal state"}
	}

	if acceptance.Deployment == nil {
		if len(acceptance.Regions) != 0 {
			return &AcceptanceValidationError{Reason: "regions require a deployment"}
		}
		return nil
	}
	d := acceptance.Deployment
	d.ID, d.App, d.SagaID = strings.TrimSpace(d.ID), strings.TrimSpace(d.App), strings.TrimSpace(d.SagaID)
	if d.ID == "" || d.App == "" || d.SagaID == "" || d.App != op.App || d.SagaID != op.SagaID {
		return &AcceptanceValidationError{Reason: "deployment ID/app/saga must match the operation"}
	}
	if value, _ := op.Payload["deploymentId"].(string); value != d.ID {
		return &AcceptanceValidationError{Reason: "operation payload deploymentId must match the deployment"}
	}
	if d.StartedAt.IsZero() {
		d.StartedAt = op.StartedAt
	} else {
		d.StartedAt = d.StartedAt.UTC().Truncate(time.Microsecond)
	}
	if d.Status == "" {
		d.Status = model.StatusQueued
	}
	seen := map[string]bool{}
	for index := range acceptance.Regions {
		region := &acceptance.Regions[index]
		region.Name, region.NomadRegion = strings.TrimSpace(region.Name), strings.TrimSpace(region.NomadRegion)
		if region.Name == "" || region.NomadRegion == "" || seen[region.Name] {
			return &AcceptanceValidationError{Reason: "deployment regions require unique names and Nomad regions"}
		}
		seen[region.Name] = true
		region.Datacenters = sortedUniqueStrings(region.Datacenters)
	}
	sort.Slice(acceptance.Regions, func(i, j int) bool { return acceptance.Regions[i].Name < acceptance.Regions[j].Name })
	return nil
}

type requestMaterial struct {
	Schema    string `json:"schema"`
	Authority string `json:"authority"`
	Kind      string `json:"kind"`
	Resource  string `json:"resource"`
	Operation struct {
		Kind, App, Ref, Risk, Source string
		Status                       string
		Payload, Metadata            map[string]interface{}
		MaxAttempts                  int
	} `json:"operation"`
	Deployment *struct {
		App, CommitSHA, ImageTag, Environment, Status, SourceKind, SourceRef string
		SourceDirty                                                          bool
		SourceChanges                                                        []string
	} `json:"deployment,omitempty"`
	Regions             []canonicalRegion             `json:"regions,omitempty"`
	Admission           OperationAdmissionPolicy      `json:"admission,omitempty"`
	FleetReconciliation *FleetReconciliationAdmission `json:"fleetReconciliation,omitempty"`
	FleetRunnerAttempt  *FleetRunnerAttemptAdmission  `json:"fleetRunnerAttempt,omitempty"`
	Semantics           map[string]interface{}        `json:"semantics,omitempty"`
}

type canonicalRegion struct {
	Name          string   `json:"name"`
	NomadRegion   string   `json:"nomadRegion"`
	Datacenters   []string `json:"datacenters,omitempty"`
	TrafficWeight int      `json:"trafficWeight"`
}

func canonicalRequestMaterial(acceptance OperationAcceptance) ([]byte, error) {
	acceptance = normalizeFingerprintSemantics(acceptance)
	var fleetRunnerAttempt *FleetRunnerAttemptAdmission
	if acceptance.FleetRunnerAttempt != nil {
		copy := *acceptance.FleetRunnerAttempt
		copy.AttemptID = ""
		copy.ExpectedPredecessorID = ""
		fleetRunnerAttempt = &copy
	}
	material := requestMaterial{Schema: OperationRequestFingerprintVersion, Authority: acceptance.Identity.Authority, Kind: acceptance.Identity.Kind, Resource: acceptance.Identity.Resource, Admission: acceptance.Admission, FleetReconciliation: acceptance.FleetReconciliation, FleetRunnerAttempt: fleetRunnerAttempt, Semantics: acceptance.Semantics}
	material.Operation.Kind, material.Operation.App, material.Operation.Ref = acceptance.Operation.Kind, acceptance.Operation.App, acceptance.Operation.Ref
	material.Operation.Risk, material.Operation.Source, material.Operation.Status, material.Operation.MaxAttempts = acceptance.Operation.Risk, acceptance.Operation.Source, string(acceptance.Operation.Status), acceptance.Operation.MaxAttempts
	material.Operation.Payload = semanticOperationMap(acceptance.Operation.Payload, acceptance)
	material.Operation.Metadata = semanticOperationMap(acceptance.Operation.Metadata, acceptance)
	if acceptance.Deployment != nil {
		d := acceptance.Deployment
		material.Deployment = &struct {
			App, CommitSHA, ImageTag, Environment, Status, SourceKind, SourceRef string
			SourceDirty                                                          bool
			SourceChanges                                                        []string
		}{App: d.App, CommitSHA: d.CommitSHA, ImageTag: d.ImageTag, Environment: d.Environment, Status: string(d.Status), SourceKind: d.SourceKind, SourceRef: d.SourceRef, SourceDirty: d.SourceDirty, SourceChanges: append([]string(nil), d.SourceChanges...)}
		sort.Strings(material.Deployment.SourceChanges)
	}
	for _, region := range acceptance.Regions {
		material.Regions = append(material.Regions, canonicalRegion{Name: region.Name, NomadRegion: region.NomadRegion, Datacenters: append([]string(nil), region.Datacenters...), TrafficWeight: region.TrafficWeight})
	}
	return json.Marshal(material)
}

func normalizeFingerprintSemantics(acceptance OperationAcceptance) OperationAcceptance {
	if acceptance.Operation.Status == "" {
		acceptance.Operation.Status = model.OperationQueued
	}
	if acceptance.Operation.MaxAttempts <= 0 {
		acceptance.Operation.MaxAttempts = 1
	}
	if acceptance.Operation.Payload == nil {
		acceptance.Operation.Payload = map[string]interface{}{}
	}
	if acceptance.Operation.Metadata == nil {
		acceptance.Operation.Metadata = map[string]interface{}{}
	}
	if acceptance.Deployment != nil {
		deployment := *acceptance.Deployment
		if deployment.Status == "" {
			deployment.Status = model.StatusQueued
		}
		acceptance.Deployment = &deployment
	}
	acceptance.Regions = append([]model.ResolvedRegion(nil), acceptance.Regions...)
	for index := range acceptance.Regions {
		acceptance.Regions[index].Datacenters = sortedUniqueStrings(acceptance.Regions[index].Datacenters)
	}
	sort.Slice(acceptance.Regions, func(i, j int) bool { return acceptance.Regions[i].Name < acceptance.Regions[j].Name })
	return acceptance
}

func semanticOperationMap(input map[string]interface{}, acceptance OperationAcceptance) map[string]interface{} {
	if input == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		text, _ := value.(string)
		generated := (key == "operationId" && text == acceptance.Operation.ID) ||
			(key == "sagaId" && text != "" && text == acceptance.Operation.SagaID) ||
			(key == "deploymentId" && acceptance.Deployment != nil && text == acceptance.Deployment.ID) ||
			(key == "attemptId" && acceptance.FleetRunnerAttempt != nil && text == acceptance.FleetRunnerAttempt.AttemptID) ||
			key == "requestId" || key == "requestReceiptId" || key == "tokenId" || key == "credentialId" || key == "deviceId" ||
			key == "startedAt" || key == "updatedAt" || key == "finishedAt"
		if generated {
			continue
		}
		out[key] = value
	}
	return out
}

type acceptanceEnvelope struct {
	Schema             string                      `json:"schema"`
	IntentID           string                      `json:"intentId"`
	RequestIdentityID  string                      `json:"requestIdentityId"`
	Authority          string                      `json:"authority"`
	Actor              OperationActor              `json:"actor"`
	Kind               string                      `json:"kind"`
	Resource           string                      `json:"resource"`
	RequestKey         string                      `json:"requestKey"`
	Fingerprint        RequestFingerprint          `json:"fingerprint"`
	OperationID        string                      `json:"operationId"`
	SagaID             string                      `json:"sagaId,omitempty"`
	DeploymentID       string                      `json:"deploymentId,omitempty"`
	AcceptedAt         string                      `json:"acceptedAt"`
	Audit              AcceptanceAuditContext      `json:"audit"`
	SigningAlgorithm   string                      `json:"signingAlgorithm"`
	SigningKeyID       string                      `json:"signingKeyId"`
	FleetRunnerAttempt *fleetRunnerAttemptEnvelope `json:"fleetRunnerAttempt,omitempty"`
}

type fleetRunnerAttemptEnvelope struct {
	ID                      string `json:"id"`
	PlanID                  string `json:"planId"`
	Attempt                 int    `json:"attempt"`
	RunnerAttemptID         string `json:"runnerAttemptId"`
	CommitSHA               string `json:"commitSha"`
	PlanSHA256              string `json:"planSha256"`
	WorkflowURL             string `json:"workflowUrl"`
	RootAttemptID           string `json:"rootAttemptId"`
	RetryOf                 string `json:"retryOf,omitempty"`
	HeartbeatTimeoutSeconds int    `json:"heartbeatTimeoutSeconds"`
}

func newAcceptanceEnvelope(a OperationAcceptance, identityID, intentID string, acceptedAt time.Time) acceptanceEnvelope {
	deploymentID := ""
	if a.Deployment != nil {
		deploymentID = a.Deployment.ID
	}
	envelope := acceptanceEnvelope{Schema: OperationAcceptanceEnvelopeSchema, IntentID: intentID, RequestIdentityID: identityID,
		Authority: a.Identity.Authority, Actor: a.Identity.Actor, Kind: a.Identity.Kind, Resource: a.Identity.Resource, RequestKey: a.Identity.Key,
		Fingerprint: a.Fingerprint, OperationID: a.Operation.ID, SagaID: a.Operation.SagaID, DeploymentID: deploymentID,
		AcceptedAt: acceptedAt.Format(time.RFC3339Nano), Audit: a.Audit}
	if attempt := a.acceptedFleetRunnerAttempt; attempt != nil {
		envelope.FleetRunnerAttempt = &fleetRunnerAttemptEnvelope{ID: attempt.ID, PlanID: attempt.PlanID, Attempt: attempt.Attempt, RunnerAttemptID: attempt.RunnerAttemptID, CommitSHA: attempt.CommitSHA, PlanSHA256: attempt.PlanSHA256, WorkflowURL: attempt.WorkflowURL, RootAttemptID: attempt.RootAttemptID, RetryOf: attempt.RetryOf, HeartbeatTimeoutSeconds: attempt.HeartbeatTimeoutSeconds}
	}
	return envelope
}

func (s *PGOperationStore) signEnvelope(ctx context.Context, envelope *acceptanceEnvelope) ([]byte, AcceptanceSignature, error) {
	return signAcceptanceEnvelope(ctx, s.signer, envelope)
}

// SealOperationAcceptance produces the same signed envelope and canonical
// request evidence used by the PostgreSQL acceptance boundary. Alternate
// control stores use it while preparing their atomic domain admission: the
// supplied acceptance must therefore be the normalized, persisted shape,
// including generated IDs and timestamps.
func SealOperationAcceptance(ctx context.Context, signer AcceptanceSigner, acceptance OperationAcceptance, requestIdentityID, intentID string, acceptedAt time.Time) (SignedAcceptanceIntent, error) {
	if signer == nil {
		return SignedAcceptanceIntent{}, &AcceptanceValidationError{Reason: "acceptance signer is required"}
	}
	requestCanonical, err := CanonicalOperationRequest(acceptance)
	if err != nil {
		return SignedAcceptanceIntent{}, &AcceptanceValidationError{Reason: "canonical request: " + err.Error()}
	}
	envelope := newAcceptanceEnvelope(acceptance, requestIdentityID, intentID, acceptedAt)
	canonical, signature, err := signAcceptanceEnvelope(ctx, signer, &envelope)
	if err != nil {
		return SignedAcceptanceIntent{}, err
	}
	digest := sha256.Sum256(canonical)
	intent := SignedAcceptanceIntent{
		ID: intentID, Schema: OperationAcceptanceEnvelopeSchema, RequestIdentityID: requestIdentityID,
		OperationID: acceptance.Operation.ID, AcceptedAt: acceptedAt, CanonicalBytes: canonical,
		CanonicalDigest: hex.EncodeToString(digest[:]), RequestCanonicalBytes: requestCanonical,
		Signature: signature, Fingerprint: acceptance.Fingerprint, Audit: acceptance.Audit,
	}
	if acceptance.Deployment != nil {
		intent.DeploymentID = acceptance.Deployment.ID
	}
	return intent, nil
}

func signAcceptanceEnvelope(ctx context.Context, signer AcceptanceSigner, envelope *acceptanceEnvelope) ([]byte, AcceptanceSignature, error) {
	probe, err := json.Marshal(envelope)
	if err != nil {
		return nil, AcceptanceSignature{}, &AcceptanceValidationError{Reason: "encode acceptance envelope: " + err.Error()}
	}
	metadata, err := signer.Sign(ctx, probe)
	if err != nil {
		return nil, AcceptanceSignature{}, &AcceptanceSignatureError{Err: err}
	}
	if metadata.Algorithm == "" || metadata.KeyID == "" {
		return nil, AcceptanceSignature{}, &AcceptanceSignatureError{Err: fmt.Errorf("signer omitted algorithm or key ID")}
	}
	envelope.SigningAlgorithm, envelope.SigningKeyID = metadata.Algorithm, metadata.KeyID
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return nil, AcceptanceSignature{}, &AcceptanceValidationError{Reason: "encode acceptance envelope: " + err.Error()}
	}
	signature, err := signer.Sign(ctx, canonical)
	if err != nil {
		return nil, AcceptanceSignature{}, &AcceptanceSignatureError{Err: err}
	}
	if signature.Algorithm != metadata.Algorithm || signature.KeyID != metadata.KeyID || signature.Value == "" {
		return nil, AcceptanceSignature{}, &AcceptanceSignatureError{Err: fmt.Errorf("signer identity changed while sealing envelope")}
	}
	return canonical, signature, nil
}

func insertRequestIdentity(ctx context.Context, tx pgx.Tx, id string, a OperationAcceptance, now time.Time, replayTTL time.Duration) (bool, error) {
	replayContract := ""
	var replayInterval interface{}
	if replayTTL > 0 {
		replayContract = OperationReplayContractVersion
		replayInterval = replayTTL.String()
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO operation_request_identities
		(id,authority,actor_issuer,actor_subject,kind,resource,request_key,fingerprint_version,fingerprint_digest,operation_id,created_at,replay_contract_version,replay_expires_at)
		VALUES($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $13::text IS NULL THEN NULL ELSE clock_timestamp()+$13::interval END)
		ON CONFLICT (authority,actor_issuer,actor_subject,kind,resource,request_key) DO NOTHING
	`, id, a.Identity.Authority, a.Identity.Actor.Issuer, a.Identity.Actor.Subject, a.Identity.Kind, a.Identity.Resource, a.Identity.Key,
		a.Fingerprint.Version, a.Fingerprint.Digest, a.Operation.ID, now, replayContract, replayInterval)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func enforceActiveAppAdmission(ctx context.Context, tx pgx.Tx, app string) error {
	if strings.TrimSpace(app) == "" {
		return &AcceptanceValidationError{Reason: "active-app admission requires an app"}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:accept-app:"+app); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE app=$1 AND status IN ('queued','running'))`, app).Scan(&active); err != nil {
		return err
	}
	if active {
		return &AcceptanceAdmissionError{App: app}
	}
	return nil
}

func insertAcceptedDomain(ctx context.Context, tx pgx.Tx, a OperationAcceptance) error {
	if a.Deployment != nil {
		changes, err := json.Marshal(a.Deployment.SourceChanges)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO deployments
			(id,app,commit_sha,image_tag,spec_digest,environment,saga_id,status,source_kind,source_ref,source_dirty,source_changes,started_at,finished_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			a.Deployment.ID, a.Deployment.App, a.Deployment.CommitSHA, a.Deployment.ImageTag, a.Deployment.SpecDigest, a.Deployment.Environment, a.Deployment.SagaID,
			a.Deployment.Status, a.Deployment.SourceKind, a.Deployment.SourceRef, a.Deployment.SourceDirty, changes, a.Deployment.StartedAt, a.Deployment.FinishedAt)
		if err != nil {
			return err
		}
		for _, region := range a.Regions {
			datacenters, err := json.Marshal(region.Datacenters)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO deployment_regions
				(deployment_id,region,nomad_region,status,desired_weight,active_weight,datacenters)
				VALUES($1,$2,$3,$4,$5,0,$6)`, a.Deployment.ID, region.Name, region.NomadRegion, model.StatusQueued, region.TrafficWeight, datacenters); err != nil {
				return err
			}
		}
	}
	payload, err := json.Marshal(a.Operation.Payload)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(a.Operation.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operations
		(id,kind,app,saga_id,ref,status,risk,source,message,payload,metadata,attempts,max_attempts,next_attempt_at,started_at,updated_at,finished_at,acceptance_required)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$16,true)`,
		a.Operation.ID, a.Operation.Kind, a.Operation.App, a.Operation.SagaID, a.Operation.Ref, a.Operation.Status, a.Operation.Risk, a.Operation.Source,
		a.Operation.Message, payload, metadata, a.Operation.Attempts, a.Operation.MaxAttempts, a.Operation.NextAttemptAt, a.Operation.StartedAt, a.Operation.FinishedAt)
	return err
}

func insertAcceptanceIntent(ctx context.Context, tx pgx.Tx, intentID, identityID string, a OperationAcceptance, acceptedAt time.Time, requestCanonical, canonical []byte, signature AcceptanceSignature) error {
	scopes, err := json.Marshal(a.Audit.Scopes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	deploymentID := interface{}(nil)
	if a.Deployment != nil {
		deploymentID = a.Deployment.ID
	}
	receiptID := interface{}(nil)
	if a.Audit.RequestReceiptID != "" {
		receiptID = a.Audit.RequestReceiptID
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_acceptance_intents
		(id,schema_version,request_identity_id,operation_id,deployment_id,accepted_at,request_receipt_id,request_id,credential_id,device_id,source,scopes,
		 fingerprint_version,fingerprint_digest,request_canonical_bytes,canonical_bytes,canonical_digest,signing_algorithm,signing_key_id,signature)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		intentID, OperationAcceptanceEnvelopeSchema, identityID, a.Operation.ID, deploymentID, acceptedAt, receiptID,
		a.Audit.RequestID, a.Audit.CredentialID, a.Audit.DeviceID, a.Audit.Source, scopes,
		a.Fingerprint.Version, a.Fingerprint.Digest, requestCanonical, canonical, hex.EncodeToString(digest[:]), signature.Algorithm, signature.KeyID, signature.Value)
	return err
}

func acceptedResult(a OperationAcceptance, identityID, intentID string, acceptedAt time.Time, requestCanonical, canonical []byte, signature AcceptanceSignature, replayed bool) AcceptedOperation {
	digest := sha256.Sum256(canonical)
	intent := SignedAcceptanceIntent{ID: intentID, Schema: OperationAcceptanceEnvelopeSchema, RequestIdentityID: identityID,
		OperationID: a.Operation.ID, AcceptedAt: acceptedAt, CanonicalBytes: append([]byte(nil), canonical...), CanonicalDigest: hex.EncodeToString(digest[:]),
		RequestCanonicalBytes: append([]byte(nil), requestCanonical...), Signature: signature, Fingerprint: a.Fingerprint, Audit: a.Audit}
	if a.Deployment != nil {
		intent.DeploymentID = a.Deployment.ID
	}
	return AcceptedOperation{Operation: a.Operation, Deployment: a.Deployment, Regions: a.Regions, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Replayed: replayed, Intent: intent, FleetRunnerAttempt: a.acceptedFleetRunnerAttempt}
}

type loadedAcceptance struct {
	intent                  SignedAcceptanceIntent
	identityFingerprint     RequestFingerprint
	identityOperationID     string
	operation               model.Operation
	exactPayload            map[string]interface{}
	exactMetadata           map[string]interface{}
	deployment              *model.Deployment
	regions                 []model.ResolvedRegion
	fleetRunnerAttempt      *fleet.RunnerAttempt
	fleetRunnerLineageValid bool
}

// expireReplayIfEligible records a durable tombstone after the signed record
// has been verified. It locks the owning operation before evaluating holds, so
// a worker cannot reserve a new external effect or change terminal state
// between the hold proof and the tombstone write.
func (s *PGOperationStore) expireReplayIfEligible(ctx context.Context, identity OperationRequestIdentity, identityID string) error {
	expired, expiresAt, err := s.db.expireReplayIdentityIfEligible(ctx, identityID)
	if err != nil {
		return err
	}
	if expired {
		return &AcceptanceExpiredError{Identity: identity, ExpiresAt: *expiresAt}
	}
	return nil
}

// expireReplayIdentityIfEligible is the shared, locked replay-expiry
// transition used by replay resolution and archive-backed retirement.
func (db *DB) expireReplayIdentityIfEligible(ctx context.Context, identityID string) (bool, *time.Time, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback(context.Background())
	expired, expiresAt, err := expireReplayIdentityInTx(ctx, tx, identityID)
	if err != nil || !expired {
		return expired, expiresAt, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, err
	}
	return true, expiresAt, nil
}

// expireReplayIdentityInTx shares the same hold proof between ordinary replay
// resolution and archive-backed retirement. The latter commits this tombstone
// together with removal of the hot payload and its byte reservation.
func expireReplayIdentityInTx(ctx context.Context, tx pgx.Tx, identityID string) (bool, *time.Time, error) {

	var contract, operationID, status, kind, ref string
	var expiresAt, expiredAt *time.Time
	var recoveryHold bool
	err := tx.QueryRow(ctx, `
		SELECT ri.replay_contract_version,ri.replay_expires_at,ri.replay_expired_at,
			o.id,o.status,o.kind,o.ref,
			COALESCE(o.metadata->>'manualRecoveryRequired'='true',false) OR COALESCE(o.metadata->>'externalEffectRecoveryPending'='true',false)
		FROM operation_request_identities ri
		JOIN operations o ON o.id=ri.operation_id
		WHERE ri.id=$1
		FOR UPDATE OF ri,o
	`, identityID).Scan(&contract, &expiresAt, &expiredAt, &operationID, &status, &kind, &ref, &recoveryHold)
	if err != nil {
		return false, nil, err
	}
	if contract == "" {
		return false, nil, nil
	}
	if contract != OperationReplayContractVersion || expiresAt == nil {
		return false, nil, &AcceptanceSignatureError{Err: fmt.Errorf("unsupported replay contract %q", contract)}
	}
	if expiredAt != nil {
		return true, expiresAt, nil
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return false, nil, err
	}
	if databaseNow.Before(*expiresAt) || recoveryHold || !model.OperationStatus(status).Terminal() {
		return false, nil, nil
	}

	var effectHold bool
	// Completed effects remain a hold: completion can precede a lost terminal
	// operation write, while resolved is the explicit recovery decision that
	// proves the effect no longer needs replay protection.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_effects WHERE operation_id=$1 AND lifecycle <> 'resolved')`, operationID).Scan(&effectHold); err != nil {
		return false, nil, err
	}
	if effectHold {
		return false, nil, nil
	}

	// Fleet runner state is an external execution aggregate outside
	// operation_effects. Keep both runner and reconciliation identities live
	// while any attempt for the accepted plan remains active.
	if kind == "fleet.runner-attempt" || kind == "fleet.reconciliation" {
		var runnerHold bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fleet_runner_attempts WHERE plan_id=$1 AND status IN ('queued','running'))`, ref).Scan(&runnerHold); err != nil {
			return false, nil, err
		}
		if runnerHold {
			return false, nil, nil
		}
	}

	var deploymentID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(deployment_id,'') FROM operation_acceptance_intents WHERE request_identity_id=$1`, identityID).Scan(&deploymentID); err != nil {
		return false, nil, err
	}
	if deploymentID != "" {
		var deploymentStatus, app string
		if err := tx.QueryRow(ctx, `SELECT status,app FROM deployments WHERE id=$1 FOR UPDATE`, deploymentID).Scan(&deploymentStatus, &app); err != nil {
			return false, nil, err
		}
		if deploymentStatus != string(model.StatusDeployed) && deploymentStatus != string(model.StatusFailed) {
			return false, nil, nil
		}
		if deploymentStatus == string(model.StatusDeployed) {
			var rollbackHold bool
			if err := tx.QueryRow(ctx, `SELECT $1=ANY(ARRAY(SELECT id FROM deployments WHERE app=$2 AND status='deployed' ORDER BY started_at DESC LIMIT 2))`, deploymentID, app).Scan(&rollbackHold); err != nil {
				return false, nil, err
			}
			if rollbackHold {
				return false, nil, nil
			}
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE operation_request_identities SET replay_expired_at=clock_timestamp() WHERE id=$1`, identityID); err != nil {
		return false, nil, err
	}
	return true, expiresAt, nil
}

func (s *PGOperationStore) loadAcceptance(ctx context.Context, identity OperationRequestIdentity) (loadedAcceptance, error) {
	var record loadedAcceptance
	var scopes []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT
		ri.id,ri.fingerprint_version,ri.fingerprint_digest,ri.operation_id,
		ai.id,ai.schema_version,ai.operation_id,COALESCE(ai.deployment_id,''),ai.accepted_at,
		COALESCE(ai.request_receipt_id,''),ai.request_id,ai.credential_id,ai.device_id,ai.source,ai.scopes,
		ai.fingerprint_version,ai.fingerprint_digest,ai.request_canonical_bytes,ai.canonical_bytes,ai.canonical_digest,ai.signing_algorithm,ai.signing_key_id,ai.signature
		FROM operation_request_identities ri
		JOIN operation_acceptance_intents ai ON ai.request_identity_id=ri.id
		WHERE ri.authority=$1::uuid AND ri.actor_issuer=$2 AND ri.actor_subject=$3 AND ri.kind=$4 AND ri.resource=$5 AND ri.request_key=$6`,
		identity.Authority, identity.Actor.Issuer, identity.Actor.Subject, identity.Kind, identity.Resource, identity.Key).Scan(
		&record.intent.RequestIdentityID, &record.identityFingerprint.Version, &record.identityFingerprint.Digest, &record.identityOperationID,
		&record.intent.ID, &record.intent.Schema, &record.intent.OperationID, &record.intent.DeploymentID, &record.intent.AcceptedAt,
		&record.intent.Audit.RequestReceiptID, &record.intent.Audit.RequestID, &record.intent.Audit.CredentialID, &record.intent.Audit.DeviceID, &record.intent.Audit.Source, &scopes,
		&record.intent.Fingerprint.Version, &record.intent.Fingerprint.Digest, &record.intent.RequestCanonicalBytes, &record.intent.CanonicalBytes, &record.intent.CanonicalDigest,
		&record.intent.Signature.Algorithm, &record.intent.Signature.KeyID, &record.intent.Signature.Value)
	if err != nil {
		return loadedAcceptance{}, err
	}
	if !sameFingerprint(record.identityFingerprint, record.intent.Fingerprint) {
		return loadedAcceptance{}, &AcceptanceSignatureError{Err: fmt.Errorf("identity and intent fingerprints differ")}
	}
	if err := json.Unmarshal(scopes, &record.intent.Audit.Scopes); err != nil {
		return loadedAcceptance{}, &AcceptanceSignatureError{Err: err}
	}
	op, err := s.db.GetOperation(ctx, record.intent.OperationID)
	if err != nil {
		return loadedAcceptance{}, err
	}
	record.operation = *op
	// Verification uses exact decimal values; the returned operation keeps the
	// established float64 decoding its callers expect.
	var rawPayload, rawMetadata []byte
	if err := s.db.Pool.QueryRow(ctx, `SELECT payload,metadata FROM operations WHERE id=$1`, record.intent.OperationID).Scan(&rawPayload, &rawMetadata); err != nil {
		return loadedAcceptance{}, err
	}
	if record.exactPayload, err = DecodeExactJSONObject(rawPayload); err != nil {
		return loadedAcceptance{}, &AcceptanceSignatureError{Err: err}
	}
	if record.exactMetadata, err = DecodeExactJSONObject(rawMetadata); err != nil {
		return loadedAcceptance{}, &AcceptanceSignatureError{Err: err}
	}
	var original requestMaterial
	if err := json.Unmarshal(record.intent.RequestCanonicalBytes, &original); err != nil {
		return loadedAcceptance{}, &AcceptanceSignatureError{Err: err}
	}
	if original.FleetRunnerAttempt != nil {
		var envelope acceptanceEnvelope
		if err := json.Unmarshal(record.intent.CanonicalBytes, &envelope); err != nil || envelope.FleetRunnerAttempt == nil {
			return loadedAcceptance{}, &AcceptanceSignatureError{Err: fmt.Errorf("accepted fleet runner-attempt output is missing")}
		}
		attempt, err := s.db.GetFleetRunnerAttempt(ctx, original.FleetRunnerAttempt.PlanID, envelope.FleetRunnerAttempt.ID)
		if err != nil {
			return loadedAcceptance{}, err
		}
		record.fleetRunnerAttempt = attempt
		if attempt.Attempt == 1 {
			record.fleetRunnerLineageValid = attempt.RootAttemptID == attempt.ID && attempt.RetryOf == ""
		} else {
			var valid bool
			if err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM fleet_runner_attempts root
				JOIN fleet_runner_attempts predecessor ON predecessor.id=$3 AND predecessor.plan_id=$1 AND predecessor.attempt=$4
				WHERE root.id=$2 AND root.plan_id=$1 AND root.attempt=1
			)`, attempt.PlanID, attempt.RootAttemptID, attempt.RetryOf, attempt.Attempt-1).Scan(&valid); err != nil {
				return loadedAcceptance{}, err
			}
			record.fleetRunnerLineageValid = valid
		}
	}
	if record.intent.DeploymentID != "" {
		d, err := s.loadAcceptedDeployment(ctx, record.intent.DeploymentID)
		if err != nil {
			return loadedAcceptance{}, err
		}
		record.deployment, record.regions = d.deployment, d.regions
	}
	return record, nil
}

type acceptedDeployment struct {
	deployment *model.Deployment
	regions    []model.ResolvedRegion
}

func (s *PGOperationStore) loadAcceptedDeployment(ctx context.Context, id string) (acceptedDeployment, error) {
	var d model.Deployment
	var changes []byte
	err := s.db.Pool.QueryRow(ctx, `SELECT id,app,commit_sha,image_tag,spec_digest,environment,saga_id,status,source_kind,source_ref,source_dirty,source_changes,started_at,finished_at FROM deployments WHERE id=$1`, id).
		Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.SpecDigest, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt)
	if err != nil {
		return acceptedDeployment{}, err
	}
	if err := json.Unmarshal(changes, &d.SourceChanges); err != nil {
		return acceptedDeployment{}, err
	}
	rows, err := s.db.Pool.Query(ctx, `SELECT region,nomad_region,desired_weight,datacenters FROM deployment_regions WHERE deployment_id=$1 ORDER BY region`, id)
	if err != nil {
		return acceptedDeployment{}, err
	}
	defer rows.Close()
	regions := []model.ResolvedRegion{}
	for rows.Next() {
		var r model.ResolvedRegion
		var datacenters []byte
		if err := rows.Scan(&r.Name, &r.NomadRegion, &r.TrafficWeight, &datacenters); err != nil {
			return acceptedDeployment{}, err
		}
		if err := json.Unmarshal(datacenters, &r.Datacenters); err != nil {
			return acceptedDeployment{}, err
		}
		regions = append(regions, r)
	}
	if err := rows.Err(); err != nil {
		return acceptedDeployment{}, err
	}
	return acceptedDeployment{deployment: &d, regions: regions}, nil
}

func (s *PGOperationStore) verifyLoadedAcceptance(ctx context.Context, identity OperationRequestIdentity, record loadedAcceptance) error {
	if err := s.signer.Verify(ctx, record.intent.Signature, record.intent.CanonicalBytes); err != nil {
		return &AcceptanceSignatureError{Err: err}
	}
	operation := record.operation
	operation.Payload, operation.Metadata = record.exactPayload, record.exactMetadata
	if err := VerifyAcceptanceEvidence(AcceptanceEvidence{
		Identity: identity, IdentityFingerprint: record.identityFingerprint, IdentityOperationID: record.identityOperationID,
		Intent: record.intent, Operation: operation, Deployment: record.deployment, Regions: record.regions,
		FleetRunnerAttempt: record.fleetRunnerAttempt, FleetRunnerLineageValid: record.fleetRunnerLineageValid,
	}); err != nil {
		return &AcceptanceSignatureError{Err: err}
	}
	return nil
}

func verifyImmutableAcceptedDomain(identity OperationRequestIdentity, envelope acceptanceEnvelope, original requestMaterial, record AcceptanceEvidence) error {
	if original.Schema != OperationRequestFingerprintVersion || original.Authority != identity.Authority || original.Kind != identity.Kind || original.Resource != identity.Resource {
		return fmt.Errorf("canonical request identity differs from durable identity")
	}
	op := record.Operation
	if original.Operation.Kind != op.Kind || original.Operation.App != op.App || original.Operation.Ref != op.Ref || original.Operation.Risk != op.Risk || original.Operation.Source != op.Source || original.Operation.MaxAttempts != op.MaxAttempts {
		return fmt.Errorf("immutable operation fields differ from accepted request")
	}
	if original.Operation.Status == string(model.OperationQueued) {
		if op.Status != model.OperationQueued && op.Status != model.OperationRunning && !op.Status.Terminal() {
			return fmt.Errorf("queued acceptance has invalid lifecycle status")
		}
	} else if string(op.Status) != original.Operation.Status {
		return fmt.Errorf("completed acceptance disposition changed")
	}
	if err := verifyImmutableFleetRunnerAttempt(original.FleetRunnerAttempt, envelope.FleetRunnerAttempt, record.FleetRunnerAttempt, record.FleetRunnerLineageValid); err != nil {
		return err
	}
	currentAcceptance := OperationAcceptance{Identity: identity, Operation: op, Deployment: record.Deployment, Regions: record.Regions}
	if original.FleetRunnerAttempt != nil && envelope.FleetRunnerAttempt != nil {
		runnerAdmission := *original.FleetRunnerAttempt
		runnerAdmission.AttemptID = envelope.FleetRunnerAttempt.ID
		currentAcceptance.FleetRunnerAttempt = &runnerAdmission
	}
	if original.Operation.Status == string(model.OperationQueued) && (op.Kind == "fleet.github.pull-request" || op.Kind == "fleet.github.apply-dispatch") {
		acceptedIntent, ok := original.Operation.Payload["fleetGitHub"]
		currentIntent, currentOK := op.Payload["fleetGitHub"]
		if !ok || !currentOK || !exactJSONEqual(acceptedIntent, currentIntent) {
			return fmt.Errorf("immutable Fleet GitHub reservation payload differs from accepted request")
		}
	} else if !exactJSONEqual(original.Operation.Payload, semanticOperationMap(op.Payload, currentAcceptance)) {
		return fmt.Errorf("immutable operation payload differs from accepted request")
	}
	currentMetadata := semanticOperationMap(op.Metadata, currentAcceptance)
	if !exactJSONSubset(original.Operation.Metadata, currentMetadata) {
		return fmt.Errorf("accepted operation metadata was removed or changed")
	}
	if original.Deployment == nil {
		if record.Deployment != nil {
			return fmt.Errorf("unexpected deployment link")
		}
		return nil
	}
	if record.Deployment == nil {
		return fmt.Errorf("accepted deployment is missing")
	}
	d := record.Deployment
	if d.SagaID != record.Operation.SagaID {
		return fmt.Errorf("deployment saga differs from signed operation saga")
	}
	// CommitSHA and ImageTag are derived during an ordinary deployment lifecycle,
	// so an accepted empty artifact may become populated. The legacy branch/ref
	// pipeline also resolves source provenance during execution; pinned release
	// and rollback inputs remain immutable.
	legacyDerivedSource := original.Operation.Kind == "app.deploy" && original.Operation.Source == "pipeline" && original.Deployment.SourceKind == ""
	releaseDerivedObservation := original.Operation.Kind == "app.deploy" && original.Operation.Source == "release-control-api" && original.Deployment.SourceKind == ""
	immutableProvenanceChanged := false
	switch {
	case legacyDerivedSource:
		// The legacy branch/ref lane resolves the commit and all source
		// observations during clone.
	case releaseDerivedObservation:
		// A pinned release still records how the exact source was obtained,
		// but its SHA, source ref, cleanliness and changes remain bound.
		immutableProvenanceChanged = original.Deployment.SourceRef != d.SourceRef || original.Deployment.SourceDirty != d.SourceDirty ||
			original.Deployment.CommitSHA != d.CommitSHA || !equalStrings(original.Deployment.SourceChanges, sortedUniqueStrings(d.SourceChanges))
	default:
		immutableProvenanceChanged = original.Deployment.SourceKind != d.SourceKind || original.Deployment.SourceRef != d.SourceRef || original.Deployment.SourceDirty != d.SourceDirty ||
			original.Deployment.CommitSHA != d.CommitSHA || !equalStrings(original.Deployment.SourceChanges, sortedUniqueStrings(d.SourceChanges))
	}
	if original.Deployment.App != d.App || original.Deployment.Environment != d.Environment || immutableProvenanceChanged ||
		(original.Deployment.ImageTag != "" && original.Deployment.ImageTag != d.ImageTag) {
		return fmt.Errorf("immutable deployment fields differ from accepted request")
	}
	if len(original.Regions) != len(record.Regions) {
		return fmt.Errorf("accepted deployment regions changed")
	}
	for i, expected := range original.Regions {
		actual := record.Regions[i]
		if expected.Name != actual.Name || expected.NomadRegion != actual.NomadRegion || expected.TrafficWeight != actual.TrafficWeight || !equalStrings(expected.Datacenters, actual.Datacenters) {
			return fmt.Errorf("accepted deployment region %q changed", expected.Name)
		}
	}
	return nil
}

func jsonEqual(a, b interface{}) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(left, right) == 1
}

func (s *PGOperationStore) resolveFresh(identity OperationRequestIdentity, fingerprint RequestFingerprint) (AcceptedOperation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.policy.ResolveTimeout)
	defer cancel()
	return s.Resolve(ctx, identity, fingerprint)
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
