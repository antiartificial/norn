package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// V3OperationStore is the deliberately narrow etcd adapter for the v3 control
// boundaries. Its records keep the fence generation private while acceptance
// records preserve the signed producer intent independently from execution.
type V3OperationStore struct {
	kv                clientv3.KV
	lease             clientv3.Lease
	prefix, authority string
	signer            store.AcceptanceSigner
	policy            store.AcceptancePolicy
}

// appOperationLockLease is renewed well before expiry. The lock's Context is
// canceled if a renewal cannot establish continued ownership, so callers do
// not keep executing after an etcd lease becomes uncertain.
const (
	appOperationLockLease           = 30 * time.Second
	appOperationLockRenewalInterval = time.Second
)

type v3Record struct {
	Operation  model.Operation `json:"operation"`
	Generation int64           `json:"generation"`
}
type v3Acceptance struct {
	Identity              store.OperationRequestIdentity `json:"identity"`
	Accepted              store.AcceptedOperation        `json:"accepted"`
	ReplayContractVersion string                         `json:"replayContractVersion,omitempty"`
	ReplayExpiresAt       *time.Time                     `json:"replayExpiresAt,omitempty"`
	ReplayExpiredAt       *time.Time                     `json:"replayExpiredAt,omitempty"`
}

type loadedV3Acceptance struct {
	record   v3Acceptance
	revision int64
}

type leasedKV interface {
	clientv3.KV
	clientv3.Lease
}

func NewV3OperationStore(kv leasedKV, prefix, authority string, signer store.AcceptanceSigner) (*V3OperationStore, error) {
	return NewV3OperationStoreWithPolicy(kv, prefix, authority, signer, store.AcceptancePolicy{})
}

// NewV3OperationStoreWithPolicy opts newly accepted etcd identities into the
// same replay contract as PostgreSQL. The historical constructor remains an
// indefinite-replay compatibility boundary.
func NewV3OperationStoreWithPolicy(kv leasedKV, prefix, authority string, signer store.AcceptanceSigner, policy store.AcceptancePolicy) (*V3OperationStore, error) {
	if kv == nil || strings.TrimSpace(prefix) == "" || signer == nil {
		return nil, fmt.Errorf("etcd v3 operation store requires kv, prefix, and signer")
	}
	if policy.ReplayTTL < 0 {
		return nil, &store.AcceptanceValidationError{Reason: "replay TTL cannot be negative"}
	}
	parsed, err := uuid.Parse(authority)
	if err != nil {
		return nil, fmt.Errorf("etcd v3 operation store authority: %w", err)
	}
	return &V3OperationStore{kv: kv, lease: kv, prefix: strings.TrimRight(prefix, "/"), authority: parsed.String(), signer: signer, policy: policy}, nil
}

var _ store.OperationStore = (*V3OperationStore)(nil)
var _ store.OperationIdentityResolver = (*V3OperationStore)(nil)
var _ store.ExecutionStore = (*V3OperationStore)(nil)
var _ store.OperationCheckpointStore = (*V3OperationStore)(nil)

// ListOperations returns a bounded, stable snapshot of accepted operations.
// It is intentionally a small read surface for the etcd Fleet runtime; callers
// must apply their own kind and result limits rather than treating etcd as the
// unrestricted historical query engine used by PostgreSQL deployments.
func (s *V3OperationStore) ListOperations(ctx context.Context, limit int) ([]model.Operation, error) {
	if limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("operation list limit must be between 1 and 100")
	}
	response, err := s.kv.Get(ctx, s.opsPrefix(), clientv3.WithPrefix(), clientv3.WithLimit(int64(limit)), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	operations := make([]model.Operation, 0, min(limit, len(response.Kvs)))
	for _, kv := range response.Kvs {
		var record v3Record
		if err := json.Unmarshal(kv.Value, &record); err != nil {
			return nil, fmt.Errorf("decode operation %q: %w", string(kv.Key), err)
		}
		operations = append(operations, record.Operation)
		if len(operations) == limit {
			break
		}
	}
	sort.SliceStable(operations, func(i, j int) bool {
		if operations[i].StartedAt.Equal(operations[j].StartedAt) {
			return operations[i].ID < operations[j].ID
		}
		return operations[i].StartedAt.After(operations[j].StartedAt)
	})
	return operations, nil
}

// ListOperationsByKind reads from the immutable kind/time index. The limit is
// applied by etcd after kind and descending acceptance-time filtering, rather
// than before filtering an arbitrary UUID-keyed operation prefix.
func (s *V3OperationStore) ListOperationsByKind(ctx context.Context, kind string, limit int) ([]model.Operation, error) {
	if strings.TrimSpace(kind) == "" || limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("operation kind and a list limit between 1 and 100 are required")
	}
	response, err := s.kv.Get(ctx, s.operationKindIndexPrefix(kind), clientv3.WithPrefix(), clientv3.WithLimit(int64(limit)), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	operations := make([]model.Operation, 0, len(response.Kvs))
	for _, item := range response.Kvs {
		id := string(item.Value)
		if id == "" {
			return nil, fmt.Errorf("operation kind index contains an empty ID")
		}
		record, _, err := s.load(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load indexed operation %q: %w", id, err)
		}
		if record.Operation.ID != id || record.Operation.Kind != kind {
			return nil, fmt.Errorf("operation kind index integrity check failed for %q", id)
		}
		operations = append(operations, record.Operation)
	}
	return operations, nil
}

// GetOperation returns a single accepted operation for the narrow
// source-validation status surface. It intentionally does not add listing or
// recovery semantics to the etcd adapter.
func (s *V3OperationStore) GetOperation(ctx context.Context, id string) (*model.Operation, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("operation ID is required")
	}
	record, _, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	operation := record.Operation
	return &operation, nil
}

func (s *V3OperationStore) opKey(id string) string { return s.prefix + "/v3/operations/" + id }
func (s *V3OperationStore) opsPrefix() string      { return s.prefix + "/v3/operations/" }
func (s *V3OperationStore) operationKindIndexPrefix(kind string) string {
	return s.prefix + "/v3/operation-index/" + base64.RawURLEncoding.EncodeToString([]byte(kind)) + "/"
}
func (s *V3OperationStore) operationKindIndexKey(kind string, acceptedAt time.Time, id string) string {
	// Complemented unsigned nanoseconds produce lexical descending time order.
	return fmt.Sprintf("%s%020d/%s", s.operationKindIndexPrefix(kind), ^uint64(acceptedAt.UnixNano()), id)
}
func (s *V3OperationStore) runningKey(id string) string { return s.prefix + "/v3/running/" + id }
func (s *V3OperationStore) runningPrefix() string       { return s.prefix + "/v3/running/" }
func (s *V3OperationStore) ownerKey(id string) string   { return s.prefix + "/v3/owners/" + id }
func (s *V3OperationStore) checkpointKey(id, stage string) string {
	return s.prefix + "/v3/checkpoints/" + id + "/" + stage
}
func (s *V3OperationStore) appLockKey(app string) string {
	digest := sha256.Sum256([]byte(app))
	return s.prefix + "/v3/app-locks/" + hex.EncodeToString(digest[:])
}
func claimOwnerValue(owner string, generation int64) string {
	return fmt.Sprintf("%d:%s", generation, owner)
}
func leaseTTL(value time.Duration) int64 {
	seconds := int64((value + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
func (s *V3OperationStore) acceptanceKey(i store.OperationRequestIdentity) string {
	b, _ := json.Marshal(i)
	d := sha256.Sum256(b)
	return s.prefix + "/v3/acceptance/" + hex.EncodeToString(d[:])
}
func (s *V3OperationStore) replayLiveKey(acceptanceKey string) string {
	return acceptanceKey + "/replay-live"
}
func (s *V3OperationStore) Authority(context.Context) (string, error) { return s.authority, nil }

func (s *V3OperationStore) Accept(ctx context.Context, a store.OperationAcceptance) (store.AcceptedOperation, error) {
	// The current adapter does not yet implement these multi-record admission
	// aggregates. Refuse them before any write instead of storing a receipt
	// whose domain state or policy was never enforced.
	if a.Deployment != nil || len(a.Regions) > 0 || a.Admission.OneActiveMutablePerApp || a.FleetReconciliation != nil || a.FleetRunnerAttempt != nil {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd operation aggregate admission is not implemented"}
	}
	// This adapter has no external-effect aggregate. Replay expiry is therefore
	// limited to the read-only operation kind it executes today; a future kind
	// must add its own authoritative hold before it can opt in.
	if s.policy.ReplayTTL > 0 && strings.TrimSpace(a.Operation.Kind) != "app.preflight" && strings.TrimSpace(a.Operation.Kind) != "fleet.capacity-plan" {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd replay expiry is implemented only for app.preflight and fleet.capacity-plan"}
	}
	var err error
	if a, err = s.normalize(a); err != nil {
		return store.AcceptedOperation{}, err
	}
	key := s.acceptanceKey(a.Identity)
	existing, err := s.loadAcceptance(ctx, key)
	if err == nil {
		return s.replay(ctx, key, existing, a.Identity, a.Fingerprint)
	}
	if !errors.Is(err, ErrNotFound) {
		return store.AcceptedOperation{}, err
	}
	acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, a, identityID, intentID, acceptedAt)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: a.Operation, Deployment: a.Deployment, Regions: a.Regions, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
	record := v3Acceptance{Identity: a.Identity, Accepted: accepted}
	var replayLease clientv3.LeaseID
	if s.policy.ReplayTTL > 0 {
		lease, err := s.lease.Grant(ctx, leaseTTL(s.policy.ReplayTTL))
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		replayLease = lease.ID
		expiresAt := acceptedAt.Add(time.Duration(lease.TTL) * time.Second)
		record.ReplayContractVersion = store.OperationReplayContractVersion
		record.ReplayExpiresAt = &expiresAt
	}
	av, err := json.Marshal(record)
	if err != nil {
		if replayLease != 0 {
			_, _ = s.lease.Revoke(context.Background(), replayLease)
		}
		return store.AcceptedOperation{}, err
	}
	ov, err := json.Marshal(v3Record{Operation: a.Operation})
	if err != nil {
		if replayLease != 0 {
			_, _ = s.lease.Revoke(context.Background(), replayLease)
		}
		return store.AcceptedOperation{}, err
	}
	puts := []clientv3.Op{clientv3.OpPut(key, string(av)), clientv3.OpPut(s.opKey(a.Operation.ID), string(ov)), clientv3.OpPut(s.operationKindIndexKey(a.Operation.Kind, acceptedAt, a.Operation.ID), a.Operation.ID)}
	if replayLease != 0 {
		puts = append(puts, clientv3.OpPut(s.replayLiveKey(key), store.OperationReplayContractVersion, clientv3.WithLease(replayLease)))
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.opKey(a.Operation.ID)), "=", 0)).Then(puts...).Commit()
	if err != nil {
		if replayLease != 0 {
			_, _ = s.lease.Revoke(context.Background(), replayLease)
		}
		return store.AcceptedOperation{}, err
	}
	if !txn.Succeeded {
		if replayLease != 0 {
			_, _ = s.lease.Revoke(context.Background(), replayLease)
		}
		existing, err := s.loadAcceptance(ctx, key)
		if err != nil {
			return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
		}
		return s.replay(ctx, key, existing, a.Identity, a.Fingerprint)
	}
	return accepted, nil
}

func (s *V3OperationStore) loadAcceptance(ctx context.Context, key string) (loadedV3Acceptance, error) {
	r, e := s.kv.Get(ctx, key)
	if e != nil {
		return loadedV3Acceptance{}, e
	}
	if len(r.Kvs) == 0 {
		return loadedV3Acceptance{}, ErrNotFound
	}
	var a v3Acceptance
	if e = json.Unmarshal(r.Kvs[0].Value, &a); e != nil {
		return loadedV3Acceptance{}, e
	}
	return loadedV3Acceptance{record: a, revision: r.Kvs[0].ModRevision}, nil
}
func (s *V3OperationStore) replay(ctx context.Context, key string, loaded loadedV3Acceptance, identity store.OperationRequestIdentity, fp store.RequestFingerprint) (store.AcceptedOperation, error) {
	record, got := loaded.record, loaded.record.Accepted
	if record.Identity != identity || got.RequestIdentityID != got.Intent.RequestIdentityID || got.AcceptanceIntentID != got.Intent.ID {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: fmt.Errorf("etcd signed acceptance identity links differ")}
	}
	if got.Intent.Fingerprint != fp {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	if e := s.signer.Verify(ctx, got.Intent.Signature, got.Intent.CanonicalBytes); e != nil {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: e}
	}
	persisted, operationRevision, e := s.load(ctx, got.Operation.ID)
	if e != nil {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: fmt.Errorf("load accepted etcd operation: %w", e)}
	}
	if e := store.VerifyAcceptanceEvidence(store.AcceptanceEvidence{Identity: identity, IdentityFingerprint: got.Intent.Fingerprint, IdentityOperationID: got.Operation.ID, Intent: got.Intent, Operation: persisted.Operation}); e != nil {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: e}
	}
	if record.ReplayContractVersion != "" {
		if record.ReplayContractVersion != store.OperationReplayContractVersion || record.ReplayExpiresAt == nil {
			return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: fmt.Errorf("unsupported replay contract %q", record.ReplayContractVersion)}
		}
		if record.ReplayExpiredAt != nil {
			return store.AcceptedOperation{}, &store.AcceptanceExpiredError{Identity: identity, ExpiresAt: *record.ReplayExpiresAt}
		}
		live, err := s.kv.Get(ctx, s.replayLiveKey(key))
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		if len(live.Kvs) == 0 && replayOperationEligible(persisted.Operation) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			record.ReplayExpiredAt = &now
			value, err := json.Marshal(record)
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			txn, err := s.kv.Txn(ctx).If(
				clientv3.Compare(clientv3.ModRevision(key), "=", loaded.revision),
				clientv3.Compare(clientv3.ModRevision(s.opKey(got.Operation.ID)), "=", operationRevision),
				clientv3.Compare(clientv3.CreateRevision(s.replayLiveKey(key)), "=", 0),
			).Then(clientv3.OpPut(key, string(value))).Commit()
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			if !txn.Succeeded {
				fresh, err := s.loadAcceptance(ctx, key)
				if err != nil {
					return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
				}
				return s.replay(ctx, key, fresh, identity, fp)
			}
			return store.AcceptedOperation{}, &store.AcceptanceExpiredError{Identity: identity, ExpiresAt: *record.ReplayExpiresAt}
		}
	}
	got.Operation = persisted.Operation
	got.Replayed = true
	return got, nil
}
func (s *V3OperationStore) Resolve(ctx context.Context, i store.OperationRequestIdentity, fp store.RequestFingerprint) (store.AcceptedOperation, error) {
	if i.Authority != s.authority {
		return store.AcceptedOperation{}, &store.AcceptanceAuthorityError{Expected: s.authority, Actual: i.Authority}
	}
	got, e := s.loadAcceptance(ctx, s.acceptanceKey(i))
	if errors.Is(e, ErrNotFound) {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: i}
	}
	if e != nil {
		return store.AcceptedOperation{}, e
	}
	return s.replay(ctx, s.acceptanceKey(i), got, i, fp)
}
func (s *V3OperationStore) ResolveIdentity(ctx context.Context, i store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	if i.Authority != s.authority {
		return store.AcceptedOperation{}, &store.AcceptanceAuthorityError{Expected: s.authority, Actual: i.Authority}
	}
	got, e := s.loadAcceptance(ctx, s.acceptanceKey(i))
	if errors.Is(e, ErrNotFound) {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: i}
	}
	if e != nil {
		return store.AcceptedOperation{}, e
	}
	return s.replay(ctx, s.acceptanceKey(i), got, i, got.record.Accepted.Intent.Fingerprint)
}

func replayOperationEligible(operation model.Operation) bool {
	if !operation.Status.Terminal() {
		return false
	}
	return operation.Metadata["manualRecoveryRequired"] != true && operation.Metadata["externalEffectRecoveryPending"] != true
}

// normalize accepts only the narrow operation aggregate implemented by this
// adapter. It mirrors the PG boundary's identity, audit, request-fingerprint,
// and initial-operation checks before an etcd transaction can create records.
func (s *V3OperationStore) normalize(a store.OperationAcceptance) (store.OperationAcceptance, error) {
	a.Identity.Authority = strings.TrimSpace(a.Identity.Authority)
	a.Identity.Actor.Issuer = strings.TrimSpace(a.Identity.Actor.Issuer)
	a.Identity.Actor.Subject = strings.TrimSpace(a.Identity.Actor.Subject)
	a.Identity.Kind = strings.TrimSpace(a.Identity.Kind)
	a.Identity.Resource = strings.TrimSpace(a.Identity.Resource)
	a.Identity.Key = strings.TrimSpace(a.Identity.Key)
	if a.Identity.Authority != s.authority {
		return store.OperationAcceptance{}, &store.AcceptanceAuthorityError{Expected: s.authority, Actual: a.Identity.Authority}
	}
	if a.Identity.Actor.Issuer == "" || a.Identity.Actor.Subject == "" || a.Identity.Kind == "" || a.Identity.Resource == "" || a.Identity.Key == "" || len(a.Identity.Key) > 512 || len(a.Identity.Actor.Issuer) > 512 || len(a.Identity.Actor.Subject) > 512 || len(a.Identity.Kind) > 256 || len(a.Identity.Resource) > 1024 {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "complete bounded operation request identity is required"}
	}
	if err := store.NormalizeAcceptanceAudit(&a.Audit); err != nil {
		return store.OperationAcceptance{}, err
	}
	a.Operation.ID, a.Operation.Kind = strings.TrimSpace(a.Operation.ID), strings.TrimSpace(a.Operation.Kind)
	if a.Operation.ID == "" || a.Operation.Kind == "" || a.Operation.Kind != a.Identity.Kind {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "operation ID and identity-matching kind are required"}
	}
	if _, exists := a.Operation.Metadata["idempotencyKey"]; exists {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "new acceptance cannot use the legacy global idempotency key"}
	}
	if a.Operation.Status == "" {
		a.Operation.Status = model.OperationQueued
	}
	if a.Operation.Status != model.OperationQueued && !a.Operation.Status.Terminal() {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "accepted operation must be queued or terminal"}
	}
	if a.Operation.Status == model.OperationQueued && (a.Operation.FinishedAt != nil || a.Operation.Attempts != 0 || a.Operation.LockGeneration != 0 || a.Operation.LockedBy != "" || a.Operation.LockedUntil != nil) {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "new queued operation cannot carry execution ownership or terminal state"}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if a.Operation.StartedAt.IsZero() {
		a.Operation.StartedAt = now
	} else {
		a.Operation.StartedAt = a.Operation.StartedAt.UTC().Truncate(time.Microsecond)
	}
	if a.Operation.NextAttemptAt.IsZero() {
		a.Operation.NextAttemptAt = a.Operation.StartedAt
	} else {
		a.Operation.NextAttemptAt = a.Operation.NextAttemptAt.UTC().Truncate(time.Microsecond)
	}
	if a.Operation.MaxAttempts <= 0 {
		a.Operation.MaxAttempts = 1
	}
	if a.Operation.Payload == nil {
		a.Operation.Payload = map[string]interface{}{}
	}
	if a.Operation.Metadata == nil {
		a.Operation.Metadata = map[string]interface{}{}
	}
	if a.Operation.Status.Terminal() {
		if a.Operation.FinishedAt == nil {
			finished := a.Operation.StartedAt
			a.Operation.FinishedAt = &finished
		} else {
			finished := a.Operation.FinishedAt.UTC().Truncate(time.Microsecond)
			a.Operation.FinishedAt = &finished
		}
	}
	want, err := store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		return store.OperationAcceptance{}, err
	}
	if a.Fingerprint.Version != store.OperationRequestFingerprintVersion || len(a.Fingerprint.Digest) != 64 || strings.ToLower(a.Fingerprint.Digest) != a.Fingerprint.Digest {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "unsupported request fingerprint"}
	}
	if a.Fingerprint != want {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "request fingerprint does not match accepted operation semantics"}
	}
	request, err := store.CanonicalOperationRequest(a)
	if err != nil {
		return store.OperationAcceptance{}, err
	}
	if len(request) > 1024*1024 {
		return store.OperationAcceptance{}, &store.AcceptanceValidationError{Reason: "canonical request exceeds configured limit"}
	}
	return a, nil
}

func decodeV3Record(encoded []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func (s *V3OperationStore) load(ctx context.Context, id string) (v3Record, int64, error) {
	r, e := s.kv.Get(ctx, s.opKey(id))
	if e != nil {
		return v3Record{}, 0, e
	}
	if len(r.Kvs) == 0 {
		return v3Record{}, 0, ErrNotFound
	}
	var v v3Record
	if e = decodeV3Record(r.Kvs[0].Value, &v); e != nil {
		return v3Record{}, 0, e
	}
	return v, r.Kvs[0].ModRevision, nil
}
func (s *V3OperationStore) write(ctx context.Context, id string, rev int64, v v3Record) (bool, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return false, e
	}
	r, e := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(id)), "=", rev)).Then(clientv3.OpPut(s.opKey(id), string(b))).Commit()
	return r.Succeeded, e
}

type v3Checkpoint struct {
	OperationID     string          `json:"operationId"`
	Stage           string          `json:"stage"`
	ClaimGeneration int64           `json:"claimGeneration"`
	Outputs         json.RawMessage `json:"outputs"`
	OutputsDigest   string          `json:"outputsDigest"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// RecordOperationCheckpoint writes a source/build receipt only while the
// fenced claim remains the live owner. A checkpoint is immutable: an equal
// retry returns the original record; a different output is surfaced as a
// conflict rather than replacing provenance from an earlier claim.
func (s *V3OperationStore) RecordOperationCheckpoint(ctx context.Context, claim store.OperationClaim, stage string, outputs json.RawMessage) (store.OperationCheckpoint, error) {
	if claim.OperationID() == "" || claim.OwnerID() == "" || claim.Generation() <= 0 {
		return store.OperationCheckpoint{}, fmt.Errorf("operation claim is incomplete")
	}
	if (stage != store.CheckpointSource && stage != store.CheckpointBuild) || len(outputs) < 2 || len(outputs) > 65536 || !json.Valid(outputs) {
		return store.OperationCheckpoint{}, fmt.Errorf("operation checkpoint is invalid")
	}
	key := s.checkpointKey(claim.OperationID(), stage)
	for attempt := 0; attempt < 4; attempt++ {
		record, revision, err := s.load(ctx, claim.OperationID())
		if err != nil {
			return store.OperationCheckpoint{}, store.ErrOperationOwnershipLost
		}
		owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
		if err != nil || len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) || owner.Kvs[0].Lease == 0 || record.Operation.Status != model.OperationRunning || record.Operation.LockedBy != claim.OwnerID() || record.Generation != claim.Generation() {
			return store.OperationCheckpoint{}, store.ErrOperationOwnershipLost
		}
		existing, err := s.loadCheckpoint(ctx, key)
		if err == nil {
			if existing.OutputsDigest != checkpointDigest(outputs) {
				return existing, store.ErrCheckpointConflict
			}
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return store.OperationCheckpoint{}, err
		}
		candidate := store.OperationCheckpoint{OperationID: claim.OperationID(), Stage: stage, ClaimGeneration: claim.Generation(), Outputs: bytes.Clone(outputs), OutputsDigest: checkpointDigest(outputs), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
		encoded, err := json.Marshal(v3Checkpoint{OperationID: candidate.OperationID, Stage: candidate.Stage, ClaimGeneration: candidate.ClaimGeneration, Outputs: candidate.Outputs, OutputsDigest: candidate.OutputsDigest, CreatedAt: candidate.CreatedAt})
		if err != nil {
			return store.OperationCheckpoint{}, err
		}
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", revision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", owner.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		).Then(clientv3.OpPut(key, string(encoded))).Commit()
		if err != nil {
			return store.OperationCheckpoint{}, err
		}
		if txn.Succeeded {
			return candidate, nil
		}
	}
	return store.OperationCheckpoint{}, store.ErrOperationOwnershipLost
}

// LoadOperationCheckpoint verifies the stored digest before returning any
// execution-affecting receipt. A corrupt or foreign value fails closed.
func (s *V3OperationStore) LoadOperationCheckpoint(ctx context.Context, operationID, stage string) (*store.OperationCheckpoint, error) {
	if operationID == "" || (stage != store.CheckpointSource && stage != store.CheckpointBuild) {
		return nil, fmt.Errorf("operation checkpoint stage is invalid")
	}
	checkpoint, err := s.loadCheckpoint(ctx, s.checkpointKey(operationID, stage))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if checkpoint.OperationID != operationID || checkpoint.Stage != stage {
		return nil, fmt.Errorf("operation checkpoint identity is invalid")
	}
	return &checkpoint, nil
}

func (s *V3OperationStore) loadCheckpoint(ctx context.Context, key string) (store.OperationCheckpoint, error) {
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return store.OperationCheckpoint{}, err
	}
	if len(response.Kvs) == 0 {
		return store.OperationCheckpoint{}, ErrNotFound
	}
	var stored v3Checkpoint
	if err := decodeV3Record(response.Kvs[0].Value, &stored); err != nil {
		return store.OperationCheckpoint{}, fmt.Errorf("decode etcd operation checkpoint: %w", err)
	}
	if stored.OperationID == "" || stored.Stage == "" || stored.ClaimGeneration <= 0 || len(stored.Outputs) < 2 || !json.Valid(stored.Outputs) || checkpointDigest(stored.Outputs) != stored.OutputsDigest {
		return store.OperationCheckpoint{}, fmt.Errorf("operation checkpoint failed integrity verification")
	}
	return store.OperationCheckpoint{OperationID: stored.OperationID, Stage: stored.Stage, ClaimGeneration: stored.ClaimGeneration, Outputs: bytes.Clone(stored.Outputs), OutputsDigest: stored.OutputsDigest, CreatedAt: stored.CreatedAt}, nil
}

func checkpointDigest(outputs []byte) string {
	digest := sha256.Sum256(outputs)
	return "sha256:" + hex.EncodeToString(digest[:])
}
func (s *V3OperationStore) ClaimNextOperation(ctx context.Context, owner string, lease time.Duration, kinds []string) (*model.Operation, store.OperationClaim, error) {
	if owner == "" || lease <= 0 {
		return nil, store.OperationClaim{}, fmt.Errorf("operation claim owner and lease are required")
	}
	r, e := s.kv.Get(ctx, s.opsPrefix(), clientv3.WithPrefix())
	if e != nil {
		return nil, store.OperationClaim{}, e
	}
	allowed := map[string]bool{}
	for _, k := range kinds {
		allowed[k] = true
	}
	type candidate struct {
		v   v3Record
		rev int64
	}
	var all []candidate
	now := time.Now()
	for _, kv := range r.Kvs {
		var v v3Record
		if decodeV3Record(kv.Value, &v) != nil {
			continue
		}
		if v.Operation.Status == model.OperationQueued && !v.Operation.NextAttemptAt.After(now) && v.Operation.Attempts < v.Operation.MaxAttempts && (len(allowed) == 0 || allowed[v.Operation.Kind]) {
			all = append(all, candidate{v, kv.ModRevision})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v.Operation.StartedAt.Before(all[j].v.Operation.StartedAt) })
	for _, c := range all {
		v := c.v
		v.Operation.Status = model.OperationRunning
		v.Operation.Attempts++
		v.Generation++
		v.Operation.LockedBy = owner
		until := now.Add(lease)
		v.Operation.LockedUntil = &until
		grant, e := s.lease.Grant(ctx, leaseTTL(lease))
		if e != nil {
			return nil, store.OperationClaim{}, e
		}
		encoded, e := json.Marshal(v)
		if e != nil {
			_, _ = s.lease.Revoke(ctx, grant.ID)
			return nil, store.OperationClaim{}, e
		}
		ownerValue := claimOwnerValue(owner, v.Generation)
		txn, e := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(v.Operation.ID)), "=", c.rev),
			clientv3.Compare(clientv3.CreateRevision(s.ownerKey(v.Operation.ID)), "=", 0),
		).Then(
			clientv3.OpPut(s.opKey(v.Operation.ID), string(encoded)),
			clientv3.OpPut(s.ownerKey(v.Operation.ID), ownerValue, clientv3.WithLease(grant.ID)),
			clientv3.OpPut(s.runningKey(v.Operation.ID), v.Operation.ID),
		).Commit()
		if e != nil {
			_, _ = s.lease.Revoke(ctx, grant.ID)
			return nil, store.OperationClaim{}, e
		}
		if txn.Succeeded {
			claim, e := store.NewOperationClaim(v.Operation.ID, owner, v.Generation)
			return &v.Operation, claim, e
		}
		_, _ = s.lease.Revoke(ctx, grant.ID)
	}
	return nil, store.OperationClaim{}, nil
}
func (s *V3OperationStore) mutateClaim(ctx context.Context, c store.OperationClaim, f func(*model.Operation)) error {
	return s.mutateClaimWithComparisons(ctx, c, nil, f)
}

func (s *V3OperationStore) mutateClaimWithComparisons(ctx context.Context, c store.OperationClaim, comparisonsForOperation func(*model.Operation) []clientv3.Cmp, f func(*model.Operation)) error {
	v, rev, e := s.load(ctx, c.OperationID())
	if e != nil {
		return store.ErrOperationOwnershipLost
	}
	owner, e := s.kv.Get(ctx, s.ownerKey(c.OperationID()))
	if e != nil || len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != claimOwnerValue(c.OwnerID(), c.Generation()) || owner.Kvs[0].Lease == 0 || v.Operation.Status != model.OperationRunning || v.Operation.LockedBy != c.OwnerID() || v.Generation != c.Generation() {
		return store.ErrOperationOwnershipLost
	}
	if v.Operation.Metadata == nil {
		v.Operation.Metadata = map[string]interface{}{}
	}
	f(&v.Operation)
	encoded, e := json.Marshal(v)
	if e != nil {
		return e
	}
	ops := []clientv3.Op{clientv3.OpPut(s.opKey(c.OperationID()), string(encoded))}
	if v.Operation.Status != model.OperationRunning {
		ops = append(ops, clientv3.OpDelete(s.ownerKey(c.OperationID())), clientv3.OpDelete(s.runningKey(c.OperationID())))
	}
	baseComparisons := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(c.OperationID())), "=", rev),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(c.OperationID())), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(c.OperationID())), "=", claimOwnerValue(c.OwnerID(), c.Generation())),
	}
	if comparisonsForOperation != nil {
		baseComparisons = append(baseComparisons, comparisonsForOperation(&v.Operation)...)
	}
	txn, e := s.kv.Txn(ctx).If(baseComparisons...).Then(ops...).Commit()
	if e != nil {
		return e
	}
	if !txn.Succeeded {
		return store.ErrOperationOwnershipLost
	}
	return nil
}
func (s *V3OperationStore) RenewOperationClaim(ctx context.Context, c store.OperationClaim, lease time.Duration) error {
	if lease <= 0 {
		return fmt.Errorf("operation renewal lease must be positive")
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(c.OperationID()))
	if err != nil || len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != claimOwnerValue(c.OwnerID(), c.Generation()) || owner.Kvs[0].Lease == 0 {
		return store.ErrOperationOwnershipLost
	}
	kept, err := s.lease.KeepAliveOnce(ctx, clientv3.LeaseID(owner.Kvs[0].Lease))
	if err != nil || kept == nil || kept.TTL <= 0 {
		return store.ErrOperationOwnershipLost
	}
	return s.mutateClaim(ctx, c, func(o *model.Operation) {
		until := time.Now().Add(time.Duration(kept.TTL) * time.Second)
		o.LockedUntil = &until
		o.UpdatedAt = time.Now().UTC()
	})
}
func (s *V3OperationStore) DeferClaimedOperation(ctx context.Context, c store.OperationClaim, msg string, next time.Time, m map[string]interface{}) error {
	return s.mutateClaim(ctx, c, func(o *model.Operation) {
		o.Status = model.OperationQueued
		o.Attempts--
		o.Message = msg
		o.NextAttemptAt = next
		o.LockedBy = ""
		o.LockedUntil = nil
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}
func (s *V3OperationStore) DeferClaimedOperationWithAppLock(ctx context.Context, c store.OperationClaim, lock store.AppOperationLock, msg string, next time.Time, m map[string]interface{}) error {
	return s.mutateClaimWithAppLock(ctx, c, lock, func(o *model.Operation) {
		o.Status = model.OperationQueued
		o.Attempts--
		o.Message = msg
		o.NextAttemptAt = next
		o.LockedBy = ""
		o.LockedUntil = nil
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}

// DeferOrFailCronPauseClaimedOperation preserves every claimed recovery
// attempt. A persistent ambiguous Nomad effect therefore reaches the request
// budget and becomes a manual-review receipt instead of cycling forever.
func (s *V3OperationStore) DeferOrFailCronPauseClaimedOperation(ctx context.Context, c store.OperationClaim, lock store.AppOperationLock, msg string, next time.Time, m map[string]interface{}) (bool, error) {
	terminal := false
	err := s.mutateClaimWithAppLock(ctx, c, lock, func(o *model.Operation) {
		for k, v := range m {
			o.Metadata[k] = v
		}
		if o.Attempts >= o.MaxAttempts {
			now := time.Now().UTC()
			o.Status = model.OperationFailed
			o.Message = "cron pause effect recovery retry budget exhausted; manual recovery is required: " + msg
			o.LastError = msg
			o.LockedBy = ""
			o.LockedUntil = nil
			o.FinishedAt = &now
			o.Metadata["manualRecoveryRequired"] = true
			o.Metadata["externalEffectRecoveryPending"] = true
			o.Metadata["retryBudgetExhausted"] = true
			terminal = true
			return
		}
		o.Status = model.OperationQueued
		o.Message = msg
		o.LastError = msg
		o.NextAttemptAt = next
		o.LockedBy = ""
		o.LockedUntil = nil
	})
	if err != nil {
		return false, err
	}
	return terminal, nil
}
func (s *V3OperationStore) RetryClaimedOperation(ctx context.Context, c store.OperationClaim, msg, last string, next time.Time, m map[string]interface{}) error {
	return s.mutateClaim(ctx, c, func(o *model.Operation) {
		o.Status = model.OperationQueued
		o.Message = msg
		o.LastError = last
		o.NextAttemptAt = next
		o.LockedBy = ""
		o.LockedUntil = nil
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}
func (s *V3OperationStore) RetryClaimedOperationWithAppLock(ctx context.Context, c store.OperationClaim, lock store.AppOperationLock, msg, last string, next time.Time, m map[string]interface{}) error {
	return s.mutateClaimWithAppLock(ctx, c, lock, func(o *model.Operation) {
		o.Status = model.OperationQueued
		o.Message = msg
		o.LastError = last
		o.NextAttemptAt = next
		o.LockedBy = ""
		o.LockedUntil = nil
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}
func (s *V3OperationStore) FinishClaimedOperation(ctx context.Context, c store.OperationClaim, status model.OperationStatus, msg string, m map[string]interface{}) error {
	return s.mutateClaim(ctx, c, func(o *model.Operation) {
		now := time.Now()
		o.Status = status
		o.Message = msg
		o.LockedBy = ""
		o.LockedUntil = nil
		o.FinishedAt = &now
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}

// FinishClaimedOperationWithAppLock terminalizes only while both the operation
// claim and its app-scoped lock still have the same etcd-owned fence. The
// comparisons run in the same transaction as the terminal record write, so a
// replacement holder cannot race a local Context.Err check.
func (s *V3OperationStore) FinishClaimedOperationWithAppLock(ctx context.Context, c store.OperationClaim, lock store.AppOperationLock, status model.OperationStatus, msg string, m map[string]interface{}) error {
	return s.mutateClaimWithAppLock(ctx, c, lock, func(o *model.Operation) {
		now := time.Now()
		o.Status = status
		o.Message = msg
		o.LockedBy = ""
		o.LockedUntil = nil
		o.FinishedAt = &now
		for k, v := range m {
			o.Metadata[k] = v
		}
	})
}

func (s *V3OperationStore) mutateClaimWithAppLock(ctx context.Context, c store.OperationClaim, lock store.AppOperationLock, f func(*model.Operation)) error {
	if lock == nil || lock.Fence() == "" {
		return store.ErrOperationOwnershipLost
	}
	return s.mutateClaimWithComparisons(ctx, c, func(o *model.Operation) []clientv3.Cmp {
		lockKey := o.App
		if lockKey == "" {
			lockKey = o.Kind + ":" + o.Ref
		}
		return []clientv3.Cmp{clientv3.Compare(clientv3.Value(s.appLockKey(lockKey)), "=", lock.Fence())}
	}, f)
}

// RecoverExpiredOperations never requeues an expired etcd claim. This adapter
// has no checkpoint or external-effect aggregate yet, so it cannot prove that
// a prior executor stopped before a mutable side effect. It atomically fences
// each expired owner by terminalizing the operation for manual recovery and
// explicitly records unresolved external-effect ambiguity. A concurrent renew,
// finish, or replacement claim changes the record revision and wins instead.
func (s *V3OperationStore) RecoverExpiredOperations(ctx context.Context) error {
	if s == nil || s.kv == nil || s.lease == nil {
		return fmt.Errorf("etcd operation recovery is unavailable")
	}
	// Only active operations have running-index keys. Owner keys are attached
	// to etcd server leases, so their absence is authoritative expiry and does
	// not depend on either API replica's wall clock.
	now := time.Now().UTC().Truncate(time.Microsecond)
	cursor, end := s.runningPrefix(), clientv3.GetPrefixRangeEnd(s.runningPrefix())
	for {
		response, err := s.kv.Get(ctx, cursor, clientv3.WithRange(end), clientv3.WithLimit(256))
		if err != nil {
			return err
		}
		for _, entry := range response.Kvs {
			operationID := strings.TrimPrefix(string(entry.Key), s.runningPrefix())
			if operationID == "" {
				continue
			}
			owner, err := s.kv.Get(ctx, s.ownerKey(operationID))
			if err != nil {
				return err
			}
			if len(owner.Kvs) != 0 {
				continue
			}
			opResponse, err := s.kv.Get(ctx, s.opKey(operationID))
			if err != nil {
				return err
			}
			if len(opResponse.Kvs) == 0 {
				_, err = s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(string(entry.Key)), "=", entry.ModRevision)).Then(clientv3.OpDelete(string(entry.Key))).Commit()
				if err != nil {
					return err
				}
				continue
			}
			var record v3Record
			if err := decodeV3Record(opResponse.Kvs[0].Value, &record); err != nil {
				return fmt.Errorf("decode etcd operation recovery record %q: %w", string(opResponse.Kvs[0].Key), err)
			}
			op := &record.Operation
			if op.Status != model.OperationRunning {
				_, err = s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(string(entry.Key)), "=", entry.ModRevision)).Then(clientv3.OpDelete(string(entry.Key))).Commit()
				if err != nil {
					return err
				}
				continue
			}
			if op.Metadata == nil {
				op.Metadata = map[string]interface{}{}
			}
			op.Status = model.OperationFailed
			op.Message = "operation executor lease expired; manual recovery is required before retry"
			op.LastError = "operation executor lease expired with external effect outcome unresolved"
			op.Metadata["manualRecoveryRequired"] = true
			op.Metadata["externalEffectRecoveryPending"] = true
			op.Metadata["recoveredAfterLeaseExpiry"] = true
			op.LockedBy = ""
			op.LockedUntil = nil
			op.FinishedAt = &now
			op.UpdatedAt = now
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			txn, err := s.kv.Txn(ctx).If(
				clientv3.Compare(clientv3.ModRevision(s.opKey(operationID)), "=", opResponse.Kvs[0].ModRevision),
				clientv3.Compare(clientv3.ModRevision(string(entry.Key)), "=", entry.ModRevision),
				clientv3.Compare(clientv3.CreateRevision(s.ownerKey(operationID)), "=", 0),
			).Then(clientv3.OpPut(s.opKey(operationID), string(encoded)), clientv3.OpDelete(string(entry.Key))).Commit()
			if err != nil {
				return err
			}
			if !txn.Succeeded {
				// A concurrent owner renewed, completed, or claimed the operation.
				// It owns the newer generation; leave it untouched rather than
				// retrying recovery from a stale observation.
				continue
			}
		}
		if !response.More || len(response.Kvs) == 0 {
			break
		}
		cursor = string(response.Kvs[len(response.Kvs)-1].Key) + "\x00"
	}
	return nil
}
func (s *V3OperationStore) AcquireAppOperationLock(ctx context.Context, app string) (store.AppOperationLock, bool, error) {
	if s == nil || s.kv == nil || s.lease == nil || strings.TrimSpace(app) == "" {
		return nil, false, fmt.Errorf("etcd app operation lock is unavailable")
	}
	grant, err := s.lease.Grant(ctx, leaseTTL(appOperationLockLease))
	if err != nil {
		return nil, false, err
	}
	if grant == nil {
		return nil, false, fmt.Errorf("etcd granted an expired app operation lock lease")
	}
	if grant.TTL <= 0 {
		_, _ = s.lease.Revoke(context.Background(), grant.ID)
		return nil, false, fmt.Errorf("etcd granted an expired app operation lock lease")
	}
	leaseExpiresAt := time.Now().Add(time.Duration(grant.TTL) * time.Second)
	key, owner := s.appLockKey(app), uuid.NewString()
	txn, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, owner, clientv3.WithLease(grant.ID))).
		Commit()
	if err != nil {
		_, _ = s.lease.Revoke(context.Background(), grant.ID)
		return nil, false, err
	}
	if !txn.Succeeded {
		_, _ = s.lease.Revoke(context.Background(), grant.ID)
		return nil, false, nil
	}

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	lock := store.NewFencedAppOperationLock(ctx, owner, func() {
		stopMonitor()
		// Deleting only the value we acquired prevents a delayed release from
		// deleting a successor after expiry and reacquisition.
		_, _ = s.kv.Txn(context.Background()).
			If(clientv3.Compare(clientv3.Value(key), "=", owner)).
			Then(clientv3.OpDelete(key)).
			Commit()
		_, _ = s.lease.Revoke(context.Background(), grant.ID)
	})
	go s.monitorAppOperationLock(monitorCtx, lock, key, owner, grant.ID, leaseExpiresAt)
	return lock, true, nil
}

func (s *V3OperationStore) monitorAppOperationLock(ctx context.Context, lock *store.AppOperationLockHandle, key, owner string, leaseID clientv3.LeaseID, leaseExpiresAt time.Time) {
	timer := time.NewTimer(appOperationLockRenewalInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			kept, err := boundedAppLockKeepAlive(ctx, s.lease, leaseID, time.Until(leaseExpiresAt))
			if err == nil && (kept == nil || kept.TTL <= 0) {
				err = fmt.Errorf("etcd returned an expired app lock lease")
			}
			if err == nil {
				current, getErr := s.kv.Get(ctx, key)
				if getErr != nil {
					err = getErr
				} else if len(current.Kvs) != 1 || string(current.Kvs[0].Value) != owner || current.Kvs[0].Lease != int64(leaseID) {
					err = fmt.Errorf("etcd app lock ownership no longer matches its lease")
				}
			}
			if err != nil {
				lock.Fail(fmt.Errorf("%w: %v", store.ErrAppOperationLockLost, err))
				return
			}
			leaseExpiresAt = time.Now().Add(time.Duration(kept.TTL) * time.Second)
			timer.Reset(appOperationLockRenewalInterval)
		}
	}
}

type appLockLeaseKeeper interface {
	KeepAliveOnce(context.Context, clientv3.LeaseID) (*clientv3.LeaseKeepAliveResponse, error)
}

// boundedAppLockKeepAlive never lets a transport call consume the known
// remaining lease. A blackholed endpoint therefore cancels the lock before
// etcd can hand the same app scope to another holder.
func boundedAppLockKeepAlive(ctx context.Context, lease appLockLeaseKeeper, id clientv3.LeaseID, remaining time.Duration) (*clientv3.LeaseKeepAliveResponse, error) {
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	budget := remaining / 2
	if budget > appOperationLockRenewalInterval {
		budget = appOperationLockRenewalInterval
	}
	if budget <= 0 {
		return nil, context.DeadlineExceeded
	}
	renewCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return lease.KeepAliveOnce(renewCtx, id)
}
