package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type v3DesiredReplica struct {
	Authority   string    `json:"authority"`
	App         string    `json:"app"`
	Process     string    `json:"process"`
	Region      string    `json:"region"`
	Desired     int       `json:"desiredCount"`
	Revision    int64     `json:"revision"`
	OperationID string    `json:"operationId"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

var (
	_ store.DesiredReplicaReader       = (*V3OperationStore)(nil)
	_ store.FencedDesiredReplicaWriter = (*V3OperationStore)(nil)
)

func (s *V3OperationStore) desiredReplicaPrefix(app, region string) string {
	digest := sha256.Sum256([]byte(app + "\x00" + region))
	return s.prefix + "/v3/desired-replicas/" + hex.EncodeToString(digest[:]) + "/"
}

func (s *V3OperationStore) desiredReplicaKey(app, process, region string) string {
	digest := sha256.Sum256([]byte(process))
	return s.desiredReplicaPrefix(app, region) + hex.EncodeToString(digest[:])
}

// DesiredReplicaCounts reads the current per-region scale ledger and validates
// every embedded identity because the hashed key alone is not self-describing.
func (s *V3OperationStore) DesiredReplicaCounts(ctx context.Context, app, region string) (map[string]int, error) {
	if s == nil || s.kv == nil || strings.TrimSpace(app) == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("etcd desired replica store is unavailable")
	}
	response, err := s.kv.Get(ctx, s.desiredReplicaPrefix(app, region), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(response.Kvs))
	for _, item := range response.Kvs {
		var record v3DesiredReplica
		if err := decodeV3Record(item.Value, &record); err != nil {
			return nil, fmt.Errorf("decode etcd desired replica record: %w", err)
		}
		if record.Authority != s.authority || record.App != app || record.Region != region || record.Process == "" || record.Desired < 0 || record.Revision <= 0 || record.OperationID == "" ||
			string(item.Key) != s.desiredReplicaKey(app, record.Process, region) {
			return nil, fmt.Errorf("etcd desired replica record failed identity validation")
		}
		counts[record.Process] = record.Desired
	}
	return counts, nil
}

// FinishScaleClaimedOperationWithAppLock commits the desired replica intent
// and successful operation receipt atomically. A stale authority, claim owner,
// claim generation, owner lease, or app-lock fence makes the transaction lose.
func (s *V3OperationStore) FinishScaleClaimedOperationWithAppLock(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, authority, app, process, region string, count int, message string, metadata map[string]interface{}) error {
	if s == nil || s.kv == nil || lock == nil || lock.Fence() == "" || authority != s.authority ||
		app == "" || process == "" || region == "" || count < 0 {
		return store.ErrOperationOwnershipLost
	}
	current, opRevision, err := s.load(ctx, claim.OperationID())
	if err != nil || current.Operation.Kind != "app.scale" || current.Operation.App != app || current.Operation.Status != model.OperationRunning ||
		current.Operation.LockedBy != claim.OwnerID() || current.Generation != claim.Generation() {
		return store.ErrOperationOwnershipLost
	}
	acceptedCount, countOK := effectPayloadInt(current.Operation.Payload, "count")
	if process != effectPayloadString(current.Operation.Payload, "group") || region != effectPayloadString(current.Operation.Payload, "region") || !countOK || count != acceptedCount {
		return fmt.Errorf("desired replica completion does not match accepted scale operation payload")
	}
	effectID, ok := metadata["effectId"].(string)
	if !ok || effectID == "" {
		return fmt.Errorf("desired replica completion requires its exact scale effect")
	}
	effects, err := NewV3ScaleEffectReservations(s)
	if err != nil {
		return err
	}
	completed, effectKey, effectRevision, err := effects.loadScaleEffectID(ctx, effectID)
	if err != nil || completed.Lifecycle != effect.LifecycleCompleted || completed.Completion == nil || completed.Completion.Outcome != effect.OutcomeSucceeded ||
		completed.Reservation.Authority != authority || completed.Reservation.OperationClaim.OperationID != claim.OperationID() ||
		completed.Reservation.OperationClaim.Generation <= 0 || completed.Reservation.OperationClaim.Generation > claim.Generation() ||
		(completed.Reservation.OperationClaim.Generation == claim.Generation() && completed.Reservation.OperationClaim.OwnerID != claim.OwnerID()) {
		return fmt.Errorf("desired replica completion is not bound to a successful exact scale effect")
	}
	var effectInput scaleEffectInput
	if err := json.Unmarshal(completed.Reservation.LaunchPayload, &effectInput); err != nil || effectInput.App != app || effectInput.Group != process || effectInput.Region != region || effectInput.Count != count || effectInput.NomadJobID != effectPayloadString(current.Operation.Payload, "nomadJobId") {
		return fmt.Errorf("completed scale effect does not match desired replica intent")
	}
	ownerKey := s.ownerKey(claim.OperationID())
	owner, err := s.kv.Get(ctx, ownerKey)
	if err != nil || len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || string(owner.Kvs[0].Value) != claimOwnerValue(claim.OwnerID(), claim.Generation()) {
		return store.ErrOperationOwnershipLost
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if current.Operation.Metadata == nil {
		current.Operation.Metadata = map[string]interface{}{}
	}
	delete(current.Operation.Metadata, "externalEffectRecoveryPending")
	delete(current.Operation.Metadata, "effectRecoveryReason")
	delete(current.Operation.Metadata, "effectResource")
	for key, value := range metadata {
		current.Operation.Metadata[key] = value
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	current.Operation.Status = model.OperationSucceeded
	current.Operation.Message = message
	current.Operation.LockedBy = ""
	current.Operation.LockedUntil = nil
	current.Operation.UpdatedAt = now
	current.Operation.FinishedAt = &now
	opBytes, err := json.Marshal(current)
	if err != nil {
		return err
	}
	desiredKey := s.desiredReplicaKey(app, process, region)
	prior, err := s.kv.Get(ctx, desiredKey)
	if err != nil {
		return err
	}
	revision := int64(1)
	desiredRevision := int64(0)
	if len(prior.Kvs) == 1 {
		var existing v3DesiredReplica
		if err := decodeV3Record(prior.Kvs[0].Value, &existing); err != nil || existing.Authority != authority || existing.App != app || existing.Process != process || existing.Region != region || existing.Revision <= 0 {
			return fmt.Errorf("existing etcd desired replica record failed identity validation")
		}
		revision = existing.Revision + 1
		desiredRevision = prior.Kvs[0].ModRevision
	} else if len(prior.Kvs) > 1 {
		return fmt.Errorf("etcd desired replica key returned multiple values")
	}
	desiredBytes, err := json.Marshal(v3DesiredReplica{Authority: authority, App: app, Process: process, Region: region, Desired: count, Revision: revision, OperationID: claim.OperationID(), UpdatedAt: now})
	if err != nil {
		return err
	}
	comparisons := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.opKey(claim.OperationID())), "=", opRevision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(ownerKey), "=", claimOwnerValue(claim.OwnerID(), claim.Generation())),
		clientv3.Compare(clientv3.Value(s.appLockKey(app)), "=", lock.Fence()),
		clientv3.Compare(clientv3.ModRevision(effectKey), "=", effectRevision),
	}
	if desiredRevision == 0 {
		comparisons = append(comparisons, clientv3.Compare(clientv3.CreateRevision(desiredKey), "=", 0))
	} else {
		comparisons = append(comparisons, clientv3.Compare(clientv3.ModRevision(desiredKey), "=", desiredRevision))
	}
	admissionComparisons, admissionOps, err := s.releaseAppAdmission(ctx, current.Operation)
	if err != nil {
		return err
	}
	comparisons = append(comparisons, admissionComparisons...)
	ops := []clientv3.Op{clientv3.OpPut(desiredKey, string(desiredBytes)), clientv3.OpPut(s.opKey(claim.OperationID()), string(opBytes)), clientv3.OpDelete(ownerKey), clientv3.OpDelete(s.runningKey(claim.OperationID()))}
	ops = append(ops, admissionOps...)
	txn, err := s.kv.Txn(ctx).If(comparisons...).Then(ops...).Commit()
	if err != nil {
		return err
	}
	if !txn.Succeeded {
		return store.ErrOperationOwnershipLost
	}
	return nil
}
