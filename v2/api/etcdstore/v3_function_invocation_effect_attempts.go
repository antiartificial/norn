package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// v3FunctionInvocationEffectAttempt contains only the public Nomad binding.
// In particular, it deliberately has no request body or encrypted-envelope
// fields: function request material remains in the private invocation record.
type v3FunctionInvocationEffectAttempt struct {
	OperationID     string                                     `json:"operationId"`
	Stage           store.FunctionInvocationEffectAttemptStage `json:"stage"`
	Target          string                                     `json:"target"`
	InputDigest     string                                     `json:"inputDigest"`
	ClaimGeneration int64                                      `json:"claimGeneration"`
	Attempted       bool                                       `json:"attempted"`
	CreatedAt       time.Time                                  `json:"createdAt"`
	AttemptedAt     *time.Time                                 `json:"attemptedAt,omitempty"`
}

var v3FunctionInvocationAttemptDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validV3FunctionInvocationEffectAttempt(stage store.FunctionInvocationEffectAttemptStage, target, inputDigest string) bool {
	return (stage == store.FunctionInvocationVariableAttempt || stage == store.FunctionInvocationJobAttempt) &&
		strings.TrimSpace(target) != "" && len(target) <= 512 && !strings.ContainsAny(target, "\r\n\x00") &&
		v3FunctionInvocationAttemptDigest.MatchString(inputDigest)
}

func (s *V3OperationStore) functionInvocationEffectAttemptKey(operationID string, stage store.FunctionInvocationEffectAttemptStage) string {
	sum := sha256.Sum256([]byte(operationID))
	return s.prefix + "/v3/function-invocation-effect-attempts/" + hex.EncodeToString(sum[:]) + "/" + string(stage)
}

// RecordFunctionInvocationEffectStage durably binds the public Nomad target
// before its remote call. Equal retries return the original record; a changed
// target or digest is a conflict and never replaces earlier provenance.
func (s *V3OperationStore) RecordFunctionInvocationEffectStage(ctx context.Context, claim store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, inputDigest string) (store.FunctionInvocationEffectAttempt, error) {
	if s == nil || s.kv == nil || !validV3FunctionInvocationClaim(claim) || !validV3FunctionInvocationEffectAttempt(stage, target, inputDigest) {
		return store.FunctionInvocationEffectAttempt{}, fmt.Errorf("function invocation effect attempt is invalid")
	}
	key := s.functionInvocationEffectAttemptKey(claim.OperationID(), stage)
	for attempt := 0; attempt < 4; attempt++ {
		revision, ownerRevision, err := s.liveV3FunctionInvocationClaim(ctx, claim)
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		existing, effectRevision, err := s.loadV3FunctionInvocationEffectAttempt(ctx, key, claim.OperationID(), stage)
		if err == nil {
			if err := s.confirmV3FunctionInvocationClaim(ctx, claim, revision, ownerRevision, key, effectRevision); err != nil {
				return store.FunctionInvocationEffectAttempt{}, err
			}
			if existing.Target != target || existing.InputDigest != inputDigest {
				return existing, store.ErrFunctionInvocationEffectConflict
			}
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		candidate := store.FunctionInvocationEffectAttempt{OperationID: claim.OperationID(), Stage: stage, Target: target, InputDigest: inputDigest, ClaimGeneration: claim.Generation(), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
		encoded, err := json.Marshal(v3FunctionInvocationEffectAttempt{OperationID: candidate.OperationID, Stage: candidate.Stage, Target: candidate.Target, InputDigest: candidate.InputDigest, ClaimGeneration: candidate.ClaimGeneration, CreatedAt: candidate.CreatedAt})
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", revision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", ownerRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		).Then(clientv3.OpPut(key, string(encoded))).Commit()
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		if txn.Succeeded {
			return candidate, nil
		}
	}
	return store.FunctionInvocationEffectAttempt{}, store.ErrOperationOwnershipLost
}

// MarkFunctionInvocationEffectAttempt advances recorded to attempted in the
// same etcd compare-and-swap transaction that verifies the live claim. Only
// the transaction winner receives MarkedNow and may make the remote call.
func (s *V3OperationStore) MarkFunctionInvocationEffectAttempt(ctx context.Context, claim store.OperationClaim, stage store.FunctionInvocationEffectAttemptStage, target, inputDigest string) (store.FunctionInvocationEffectAttempt, error) {
	if s == nil || s.kv == nil || !validV3FunctionInvocationClaim(claim) || !validV3FunctionInvocationEffectAttempt(stage, target, inputDigest) {
		return store.FunctionInvocationEffectAttempt{}, fmt.Errorf("function invocation effect attempt is invalid")
	}
	key := s.functionInvocationEffectAttemptKey(claim.OperationID(), stage)
	for attempt := 0; attempt < 4; attempt++ {
		revision, ownerRevision, err := s.liveV3FunctionInvocationClaim(ctx, claim)
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		existing, effectRevision, err := s.loadV3FunctionInvocationEffectAttempt(ctx, key, claim.OperationID(), stage)
		if errors.Is(err, ErrNotFound) {
			return store.FunctionInvocationEffectAttempt{}, store.ErrFunctionInvocationEffectMissing
		}
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		if existing.Target != target || existing.InputDigest != inputDigest {
			return existing, store.ErrFunctionInvocationEffectConflict
		}
		if existing.Attempted {
			if err := s.confirmV3FunctionInvocationClaim(ctx, claim, revision, ownerRevision, key, effectRevision); err != nil {
				return store.FunctionInvocationEffectAttempt{}, err
			}
			return existing, nil
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		candidate := existing
		candidate.Attempted, candidate.MarkedNow, candidate.ClaimGeneration, candidate.AttemptedAt = true, true, claim.Generation(), &now
		encoded, err := json.Marshal(v3FunctionInvocationEffectAttempt{OperationID: candidate.OperationID, Stage: candidate.Stage, Target: candidate.Target, InputDigest: candidate.InputDigest, ClaimGeneration: candidate.ClaimGeneration, Attempted: true, CreatedAt: candidate.CreatedAt, AttemptedAt: candidate.AttemptedAt})
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", revision),
			clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", ownerRevision),
			clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
			clientv3.Compare(clientv3.ModRevision(key), "=", effectRevision),
		).Then(clientv3.OpPut(key, string(encoded))).Commit()
		if err != nil {
			return store.FunctionInvocationEffectAttempt{}, err
		}
		if txn.Succeeded {
			return candidate, nil
		}
	}
	return store.FunctionInvocationEffectAttempt{}, store.ErrOperationOwnershipLost
}

// LoadFunctionInvocationEffectAttempt is the recovery read and exposes only
// the recorded public binding.
func (s *V3OperationStore) LoadFunctionInvocationEffectAttempt(ctx context.Context, operationID string, stage store.FunctionInvocationEffectAttemptStage) (*store.FunctionInvocationEffectAttempt, error) {
	if s == nil || s.kv == nil || strings.TrimSpace(operationID) == "" || (stage != store.FunctionInvocationVariableAttempt && stage != store.FunctionInvocationJobAttempt) {
		return nil, fmt.Errorf("function invocation effect attempt lookup is invalid")
	}
	attempt, _, err := s.loadV3FunctionInvocationEffectAttempt(ctx, s.functionInvocationEffectAttemptKey(operationID, stage), operationID, stage)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func validV3FunctionInvocationClaim(claim store.OperationClaim) bool {
	return claim.OperationID() != "" && claim.OwnerID() != "" && claim.Generation() > 0
}

func (s *V3OperationStore) liveV3FunctionInvocationClaim(ctx context.Context, claim store.OperationClaim) (int64, int64, error) {
	loaded, revision, err := s.load(ctx, claim.OperationID())
	if err != nil {
		return 0, 0, store.ErrOperationOwnershipLost
	}
	owner, err := s.kv.Get(ctx, s.ownerKey(claim.OperationID()))
	if err != nil {
		return 0, 0, err
	}
	if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || loaded.Operation.Kind != store.PrivateInvocationOperationKind || loaded.Operation.Status != model.OperationRunning || loaded.Operation.LockedBy != claim.OwnerID() || loaded.Generation != claim.Generation() || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return 0, 0, store.ErrOperationOwnershipLost
	}
	return revision, owner.Kvs[0].ModRevision, nil
}

func (s *V3OperationStore) confirmV3FunctionInvocationClaim(ctx context.Context, claim store.OperationClaim, operationRevision, ownerRevision int64, effectKey string, effectRevision int64) error {
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", operationRevision),
		clientv3.Compare(clientv3.ModRevision(s.ownerKey(claim.OperationID())), "=", ownerRevision),
		clientv3.Compare(clientv3.Value(s.ownerKey(claim.OperationID())), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.ModRevision(effectKey), "=", effectRevision),
	).Then().Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return store.ErrOperationOwnershipLost
	}
	return nil
}

func (s *V3OperationStore) loadV3FunctionInvocationEffectAttempt(ctx context.Context, key, operationID string, stage store.FunctionInvocationEffectAttemptStage) (store.FunctionInvocationEffectAttempt, int64, error) {
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return store.FunctionInvocationEffectAttempt{}, 0, err
	}
	if len(response.Kvs) != 1 {
		return store.FunctionInvocationEffectAttempt{}, 0, ErrNotFound
	}
	var stored v3FunctionInvocationEffectAttempt
	if err := decodeV3Record(response.Kvs[0].Value, &stored); err != nil {
		return store.FunctionInvocationEffectAttempt{}, 0, fmt.Errorf("decode function invocation effect attempt: %w", err)
	}
	if stored.OperationID != operationID || stored.Stage != stage || !validV3FunctionInvocationEffectAttempt(stored.Stage, stored.Target, stored.InputDigest) || stored.ClaimGeneration <= 0 || stored.CreatedAt.IsZero() || (stored.Attempted && stored.AttemptedAt == nil) || (!stored.Attempted && stored.AttemptedAt != nil) {
		return store.FunctionInvocationEffectAttempt{}, 0, fmt.Errorf("function invocation effect attempt is invalid")
	}
	return store.FunctionInvocationEffectAttempt{OperationID: stored.OperationID, Stage: stored.Stage, Target: stored.Target, InputDigest: stored.InputDigest, ClaimGeneration: stored.ClaimGeneration, Attempted: stored.Attempted, CreatedAt: stored.CreatedAt, AttemptedAt: stored.AttemptedAt}, response.Kvs[0].ModRevision, nil
}
