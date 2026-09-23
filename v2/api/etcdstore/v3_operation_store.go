package etcdstore

import (
	"context"
	"crypto/sha256"
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
	prefix, authority string
	signer            store.AcceptanceSigner
}

type v3Record struct {
	Operation  model.Operation `json:"operation"`
	Generation int64           `json:"generation"`
}
type v3Acceptance struct {
	Accepted store.AcceptedOperation `json:"accepted"`
}

func NewV3OperationStore(kv clientv3.KV, prefix, authority string, signer store.AcceptanceSigner) (*V3OperationStore, error) {
	if kv == nil || strings.TrimSpace(prefix) == "" || signer == nil {
		return nil, fmt.Errorf("etcd v3 operation store requires kv, prefix, and signer")
	}
	parsed, err := uuid.Parse(authority)
	if err != nil {
		return nil, fmt.Errorf("etcd v3 operation store authority: %w", err)
	}
	return &V3OperationStore{kv: kv, prefix: strings.TrimRight(prefix, "/"), authority: parsed.String(), signer: signer}, nil
}

var _ store.OperationStore = (*V3OperationStore)(nil)
var _ store.OperationIdentityResolver = (*V3OperationStore)(nil)
var _ store.ExecutionStore = (*V3OperationStore)(nil)

func (s *V3OperationStore) opKey(id string) string { return s.prefix + "/v3/operations/" + id }
func (s *V3OperationStore) opsPrefix() string      { return s.prefix + "/v3/operations/" }
func (s *V3OperationStore) acceptanceKey(i store.OperationRequestIdentity) string {
	b, _ := json.Marshal(i)
	d := sha256.Sum256(b)
	return s.prefix + "/v3/acceptance/" + hex.EncodeToString(d[:])
}
func (s *V3OperationStore) Authority(context.Context) (string, error) { return s.authority, nil }

func (s *V3OperationStore) Accept(ctx context.Context, a store.OperationAcceptance) (store.AcceptedOperation, error) {
	if a.Identity.Authority != s.authority {
		return store.AcceptedOperation{}, &store.AcceptanceAuthorityError{Expected: s.authority, Actual: a.Identity.Authority}
	}
	if strings.TrimSpace(a.Identity.Actor.Issuer) == "" || strings.TrimSpace(a.Identity.Actor.Subject) == "" || strings.TrimSpace(a.Identity.Kind) == "" || strings.TrimSpace(a.Identity.Resource) == "" || strings.TrimSpace(a.Identity.Key) == "" || a.Operation.ID == "" || a.Operation.Kind != a.Identity.Kind || strings.TrimSpace(a.Audit.Source) == "" {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "complete signed operation identity, operation, and audit source are required"}
	}
	want, err := store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if a.Fingerprint != want {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "request fingerprint does not match accepted operation semantics"}
	}
	key := s.acceptanceKey(a.Identity)
	existing, err := s.loadAcceptance(ctx, key)
	if err == nil {
		return s.replay(ctx, existing, a.Identity, a.Fingerprint)
	}
	if !errors.Is(err, ErrNotFound) {
		return store.AcceptedOperation{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	op := a.Operation
	if op.Status == "" {
		op.Status = model.OperationQueued
	}
	if op.Status != model.OperationQueued && !op.Status.Terminal() {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "accepted operation must be queued or terminal"}
	}
	if op.Status == model.OperationQueued && (op.Attempts != 0 || op.LockGeneration != 0 || op.LockedBy != "" || op.LockedUntil != nil) {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "new queued operation cannot carry execution ownership"}
	}
	if op.Payload == nil {
		op.Payload = map[string]interface{}{}
	}
	if op.Metadata == nil {
		op.Metadata = map[string]interface{}{}
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = now
	}
	if op.NextAttemptAt.IsZero() {
		op.NextAttemptAt = op.StartedAt
	}
	if op.MaxAttempts <= 0 {
		op.MaxAttempts = 1
	}
	if op.Status.Terminal() && op.FinishedAt == nil {
		finished := now
		op.FinishedAt = &finished
	}
	request, err := json.Marshal(a)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	sig, err := s.signer.Sign(ctx, request)
	if err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: err}
	}
	if sig.Algorithm == "" || sig.KeyID == "" || sig.Value == "" {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{}
	}
	digest := sha256.Sum256(request)
	intent := store.SignedAcceptanceIntent{ID: uuid.NewString(), Schema: store.OperationAcceptanceEnvelopeSchema, RequestIdentityID: uuid.NewString(), OperationID: op.ID, AcceptedAt: now, CanonicalBytes: request, CanonicalDigest: hex.EncodeToString(digest[:]), RequestCanonicalBytes: request, Signature: sig, Fingerprint: a.Fingerprint, Audit: a.Audit}
	accepted := store.AcceptedOperation{Operation: op, Deployment: a.Deployment, Regions: a.Regions, RequestIdentityID: intent.RequestIdentityID, AcceptanceIntentID: intent.ID, Intent: intent}
	av, _ := json.Marshal(v3Acceptance{Accepted: accepted})
	ov, _ := json.Marshal(v3Record{Operation: op})
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.opKey(op.ID)), "=", 0)).Then(clientv3.OpPut(key, string(av)), clientv3.OpPut(s.opKey(op.ID), string(ov))).Commit()
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if !txn.Succeeded {
		existing, err := s.loadAcceptance(ctx, key)
		if err != nil {
			return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
		}
		return s.replay(ctx, existing, a.Identity, a.Fingerprint)
	}
	return accepted, nil
}

func (s *V3OperationStore) loadAcceptance(ctx context.Context, key string) (store.AcceptedOperation, error) {
	r, e := s.kv.Get(ctx, key)
	if e != nil {
		return store.AcceptedOperation{}, e
	}
	if len(r.Kvs) == 0 {
		return store.AcceptedOperation{}, ErrNotFound
	}
	var a v3Acceptance
	if e = json.Unmarshal(r.Kvs[0].Value, &a); e != nil {
		return store.AcceptedOperation{}, e
	}
	return a.Accepted, nil
}
func (s *V3OperationStore) replay(ctx context.Context, got store.AcceptedOperation, identity store.OperationRequestIdentity, fp store.RequestFingerprint) (store.AcceptedOperation, error) {
	if got.Intent.Fingerprint != fp {
		return store.AcceptedOperation{}, &store.AcceptanceConflictError{Identity: identity}
	}
	if e := s.signer.Verify(ctx, got.Intent.Signature, got.Intent.CanonicalBytes); e != nil {
		return store.AcceptedOperation{}, &store.AcceptanceSignatureError{Err: e}
	}
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
	return s.replay(ctx, got, i, fp)
}
func (s *V3OperationStore) ResolveIdentity(ctx context.Context, i store.OperationRequestIdentity) (store.AcceptedOperation, error) {
	got, e := s.loadAcceptance(ctx, s.acceptanceKey(i))
	if errors.Is(e, ErrNotFound) {
		return store.AcceptedOperation{}, &store.AcceptanceNotFoundError{Identity: i}
	}
	if e != nil {
		return store.AcceptedOperation{}, e
	}
	return s.replay(ctx, got, i, got.Intent.Fingerprint)
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
	if e = json.Unmarshal(r.Kvs[0].Value, &v); e != nil {
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
		if json.Unmarshal(kv.Value, &v) != nil {
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
		ok, e := s.write(ctx, v.Operation.ID, c.rev, v)
		if e != nil {
			return nil, store.OperationClaim{}, e
		}
		if ok {
			claim, e := store.NewOperationClaim(v.Operation.ID, owner, v.Generation)
			return &v.Operation, claim, e
		}
	}
	return nil, store.OperationClaim{}, nil
}
func (s *V3OperationStore) mutateClaim(ctx context.Context, c store.OperationClaim, f func(*model.Operation)) error {
	v, rev, e := s.load(ctx, c.OperationID())
	if e != nil {
		return store.ErrOperationOwnershipLost
	}
	if v.Operation.Status != model.OperationRunning || v.Operation.LockedBy != c.OwnerID() || v.Generation != c.Generation() || v.Operation.LockedUntil == nil || !v.Operation.LockedUntil.After(time.Now()) {
		return store.ErrOperationOwnershipLost
	}
	f(&v.Operation)
	ok, e := s.write(ctx, c.OperationID(), rev, v)
	if e != nil {
		return e
	}
	if !ok {
		return store.ErrOperationOwnershipLost
	}
	return nil
}
func (s *V3OperationStore) RenewOperationClaim(ctx context.Context, c store.OperationClaim, lease time.Duration) error {
	if lease <= 0 {
		return fmt.Errorf("operation renewal lease must be positive")
	}
	return s.mutateClaim(ctx, c, func(o *model.Operation) { until := time.Now().Add(lease); o.LockedUntil = &until })
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

// Recovery requires the same checkpoint and external-effect classification as
// the PostgreSQL adapter. Refuse worker startup until that path is implemented.
func (s *V3OperationStore) RecoverExpiredOperations(context.Context) error {
	return fmt.Errorf("etcd operation recovery is not implemented")
}
func (s *V3OperationStore) AcquireAppOperationLock(ctx context.Context, app string) (func(), bool, error) {
	// A lock without an ownership lease can survive a worker crash forever.
	// The M3 lock must be lease-backed and fenced before app mutations use it.
	return func() {}, false, fmt.Errorf("etcd app operation lock is not implemented")
}
