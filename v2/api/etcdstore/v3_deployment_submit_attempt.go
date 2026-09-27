package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type deploymentSubmitAttempt struct {
	Token       effect.Token `json:"token"`
	InputDigest string       `json:"inputDigest"`
}

func (s *V3DeploymentEffectReservations) submitAttemptKey(token effect.Token) string {
	return s.operations.prefix + "/v3/effects/deploy-attempts/" + token.EffectID
}

// MarkSubmitAttempt is the last durable step before one Nomad CAS request.
// True authorizes exactly this caller to attempt the write. False means an
// earlier caller may already have written; neither caller may submit again.
func (s *V3DeploymentEffectReservations) MarkSubmitAttempt(ctx context.Context, token effect.Token) (bool, error) {
	if s == nil || s.V3CanaryEffectReservations == nil || s.operations == nil {
		return false, fmt.Errorf("deployment effect store is unavailable")
	}
	record, key, revision, err := s.loadToken(ctx, token)
	if err != nil {
		return false, err
	}
	if record.Lifecycle != effect.LifecycleReserved || record.Reservation.Supervisor != "nomad-deployment" ||
		record.Reservation.Stage != "app.deploy.nomad.submit" {
		return false, effect.ErrStaleToken
	}
	op, opRevision, err := s.operations.load(ctx, record.Reservation.OperationClaim.OperationID)
	if err != nil {
		return false, store.ErrOperationOwnershipLost
	}
	claim := record.Reservation.OperationClaim
	ownerKey := s.operations.ownerKey(claim.OperationID)
	owner, err := s.operations.kv.Get(ctx, ownerKey)
	if err != nil {
		return false, err
	}
	if op.Operation.Status != model.OperationRunning || op.Operation.LockedBy != claim.OwnerID || op.Generation != claim.Generation ||
		len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID, claim.Generation) {
		return false, store.ErrOperationOwnershipLost
	}
	app := effectApp(record.Reservation.Resource)
	if app == "" {
		return false, effect.ErrStaleToken
	}
	attempt := deploymentSubmitAttempt{Token: token, InputDigest: record.Reservation.InputDigest}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		return false, err
	}
	attemptKey := s.submitAttemptKey(token)
	gateKey := s.gateKey(app)
	txn, err := s.operations.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.operations.opKey(claim.OperationID)), "=", opRevision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(ownerKey), "=", claimOwnerValue(claim.OwnerID, claim.Generation)),
		clientv3.Compare(clientv3.ModRevision(key), "=", revision),
		clientv3.Compare(clientv3.Value(gateKey), "=", key),
		clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0),
	).Then(clientv3.OpPut(attemptKey, string(encoded))).Commit()
	if err != nil {
		return false, err
	}
	if txn.Succeeded {
		return true, nil
	}
	prior, err := s.operations.kv.Get(ctx, attemptKey)
	if err != nil {
		return false, err
	}
	if len(prior.Kvs) == 1 && string(prior.Kvs[0].Value) == string(encoded) {
		return false, nil
	}
	return false, store.ErrOperationOwnershipLost
}

// SubmitAttempted is a recovery observation, not absence proof. A missing
// marker on a reserved effect still requires claim-fenced re-entry; a present
// marker forbids automatic resubmission even when Nomad currently says 404.
func (s *V3DeploymentEffectReservations) SubmitAttempted(ctx context.Context, token effect.Token) (bool, error) {
	if s == nil || s.V3CanaryEffectReservations == nil || s.operations == nil {
		return false, fmt.Errorf("deployment effect store is unavailable")
	}
	record, key, _, err := s.loadToken(ctx, token)
	if err != nil {
		return false, err
	}
	if record.Lifecycle == effect.LifecycleReserved || record.Lifecycle == effect.LifecycleLaunched {
		gate, err := s.operations.kv.Get(ctx, s.gateKey(effectApp(record.Reservation.Resource)))
		if err != nil {
			return false, err
		}
		if len(gate.Kvs) != 1 || string(gate.Kvs[0].Value) != key {
			return false, fmt.Errorf("deployment submit attempt lost its app gate")
		}
	}
	result, err := s.operations.kv.Get(ctx, s.submitAttemptKey(token))
	if err != nil {
		return false, err
	}
	if len(result.Kvs) == 0 {
		return false, nil
	}
	var attempt deploymentSubmitAttempt
	if len(result.Kvs) != 1 || json.Unmarshal(result.Kvs[0].Value, &attempt) != nil ||
		attempt.Token != token || attempt.InputDigest != record.Reservation.InputDigest {
		return false, fmt.Errorf("deployment submit attempt does not match reserved effect")
	}
	return true, nil
}
