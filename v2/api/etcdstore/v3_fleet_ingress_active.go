package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const activeFleetIngressSchema = "norn.fleet-active-ingress/v1"

// activeFleetIngress is the control-owned selection of a completed Fleet
// inventory. Its revision is a CAS fence for later route-intent admission.
type activeFleetIngress struct {
	SchemaVersion      string `json:"schemaVersion"`
	Cluster            string `json:"cluster"`
	Environment        string `json:"environment"`
	PlanID             string `json:"planId"`
	AttemptID          string `json:"attemptId"`
	CheckpointID       string `json:"checkpointId"`
	CheckpointRevision int64  `json:"checkpointRevision"`
	StateSerial        int64  `json:"stateSerial"`
	Digest             string `json:"digest"`
}

func (s *V3OperationStore) activeFleetIngressKey(cluster, environment string) string {
	sum := sha256.Sum256([]byte(cluster + "\x00" + environment))
	return s.prefix + "/v3/fleet-active-ingress/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) activeFleetIngressClusterEpochKey(cluster string) string {
	sum := sha256.Sum256([]byte(cluster))
	return s.prefix + "/v3/fleet-active-ingress-cluster-epoch/" + hex.EncodeToString(sum[:])
}

func (s *V3OperationStore) activeFleetIngressClusterEpoch(ctx context.Context, cluster, owner string) (clientv3.Cmp, clientv3.Op, error) {
	key := s.activeFleetIngressClusterEpochKey(cluster)
	response, err := s.kv.Get(ctx, key)
	if err != nil {
		return clientv3.Cmp{}, clientv3.Op{}, err
	}
	var revision int64
	if len(response.Kvs) == 1 {
		revision = response.Kvs[0].ModRevision
	}
	return clientv3.Compare(clientv3.ModRevision(key), "=", revision), clientv3.OpPut(key, owner), nil
}

func (s *V3OperationStore) activeFleetIngressTransition(ctx context.Context, planID string, completed fleet.RunnerAttempt) ([]clientv3.Cmp, []clientv3.Op, error) {
	plan, planRevision, err := s.load(ctx, planID)
	if err != nil || plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return nil, nil, fmt.Errorf("completed Fleet ingress plan is unavailable")
	}
	encodedPlan, err := json.Marshal(plan.Operation.Payload)
	if err != nil {
		return nil, nil, err
	}
	var typedPlan fleet.CapacityPlan
	if err := json.Unmarshal(encodedPlan, &typedPlan); err != nil || typedPlan.ID != planID {
		return nil, nil, fmt.Errorf("completed Fleet ingress plan identity is invalid")
	}
	history, revisions, err := s.listFleetReconciliations(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	var configured *fleet.ReconciliationRequest
	var checkpointID string
	for _, op := range history {
		request, err := store.FleetReconciliationRequestFromOperation(op)
		if err != nil {
			return nil, nil, err
		}
		if request.AttemptID == completed.ID && request.Phase == "nodes_configured" && request.IngressInventoryDigest != "" {
			if configured != nil {
				return nil, nil, fmt.Errorf("completed Fleet attempt has repeated ingress inventory")
			}
			copy := request
			configured, checkpointID = &copy, op.ID
		}
	}
	if configured == nil {
		return s.activeFleetIngressWithdrawals(ctx, typedPlan.Cluster, planID, completed.ID)
	}
	var identity struct {
		Cluster     string `json:"cluster"`
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal(configured.IngressInventory, &identity); err != nil || identity.Cluster == "" || identity.Environment == "" {
		return nil, nil, fmt.Errorf("completed Fleet ingress inventory identity is invalid")
	}
	if typedPlan.Cluster != identity.Cluster {
		return nil, nil, fmt.Errorf("completed Fleet ingress inventory differs from its plan cluster")
	}
	proof, err := resolveFleetIngressInventory(planID, identity.Cluster, identity.Environment, 18082, []fleet.RunnerAttempt{completed}, history, revisions)
	if err != nil || proof.CheckpointID != checkpointID {
		return nil, nil, fmt.Errorf("completed Fleet ingress inventory is unqualified: %v", err)
	}
	for _, op := range history {
		if op.ID != proof.CheckpointID {
			continue
		}
		index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(op.ID))
		if err != nil || len(index.Kvs) != 1 {
			return nil, nil, fmt.Errorf("Fleet ingress checkpoint acceptance is missing")
		}
		key := string(index.Kvs[0].Value)
		loaded, err := s.loadAcceptance(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
		if err != nil || accepted.Operation.ID != op.ID || accepted.Operation.Kind != "fleet.reconciliation" || accepted.Operation.Ref != planID {
			return nil, nil, fmt.Errorf("Fleet ingress checkpoint is not signed for this plan")
		}
		signedPayload, signedErr := json.Marshal(accepted.Operation.Payload)
		storedPayload, storedErr := json.Marshal(op.Payload)
		if signedErr != nil || storedErr != nil || !bytes.Equal(signedPayload, storedPayload) {
			return nil, nil, fmt.Errorf("Fleet ingress checkpoint differs from signed payload")
		}
	}
	key := s.activeFleetIngressKey(identity.Cluster, identity.Environment)
	current, err := s.kv.Get(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	var pointerRevision int64
	if len(current.Kvs) != 0 {
		var prior activeFleetIngress
		if len(current.Kvs) != 1 || decodeV3Record(current.Kvs[0].Value, &prior) != nil || prior.SchemaVersion != activeFleetIngressSchema || prior.Cluster != identity.Cluster || prior.Environment != identity.Environment || prior.StateSerial > proof.StateSerial || (prior.StateSerial == proof.StateSerial && prior.Digest != proof.Digest) {
			return nil, nil, fmt.Errorf("Fleet ingress active inventory has a newer or conflicting state")
		}
		pointerRevision = current.Kvs[0].ModRevision
	}
	pointer := activeFleetIngress{SchemaVersion: activeFleetIngressSchema, Cluster: identity.Cluster, Environment: identity.Environment,
		PlanID: planID, AttemptID: completed.ID, CheckpointID: proof.CheckpointID, CheckpointRevision: proof.CheckpointModRevision, StateSerial: proof.StateSerial, Digest: proof.Digest}
	encoded, err := json.Marshal(pointer)
	if err != nil {
		return nil, nil, err
	}
	epochCompare, epochPut, err := s.activeFleetIngressClusterEpoch(ctx, identity.Cluster, planID+"\x00"+completed.ID)
	if err != nil {
		return nil, nil, err
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(key), "=", pointerRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetReconciliationKey(planID, proof.CheckpointID)), "=", proof.CheckpointModRevision),
		epochCompare,
	}, []clientv3.Op{clientv3.OpPut(key, string(encoded)), epochPut}, nil
}

// A completed Fleet plan without ingress inventory can change ingress hosts.
// Withdraw every active pointer for its cluster in the same terminal txn so
// later app work cannot keep using stale membership.
func (s *V3OperationStore) activeFleetIngressWithdrawals(ctx context.Context, cluster, planID, attemptID string) ([]clientv3.Cmp, []clientv3.Op, error) {
	base := s.prefix + "/v3/fleet-active-ingress/"
	response, err := s.kv.Get(ctx, base, clientv3.WithPrefix(), clientv3.WithLimit(129))
	if err != nil {
		return nil, nil, err
	}
	if len(response.Kvs) > 128 {
		return nil, nil, fmt.Errorf("active Fleet ingress inventory index exceeds scan limit")
	}
	if cluster == "" {
		if len(response.Kvs) != 0 {
			return nil, nil, fmt.Errorf("Fleet plan cluster is missing while ingress inventory is active")
		}
		return nil, nil, nil
	}
	var compares []clientv3.Cmp
	var deletes []clientv3.Op
	for _, entry := range response.Kvs {
		var pointer activeFleetIngress
		if err := decodeV3Record(entry.Value, &pointer); err != nil || pointer.SchemaVersion != activeFleetIngressSchema || pointer.Cluster == "" || pointer.Environment == "" || string(entry.Key) != s.activeFleetIngressKey(pointer.Cluster, pointer.Environment) {
			return nil, nil, fmt.Errorf("active Fleet ingress inventory index is invalid")
		}
		if pointer.Cluster == cluster {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(string(entry.Key)), "=", entry.ModRevision))
			deletes = append(deletes, clientv3.OpDelete(string(entry.Key)))
		}
	}
	epochCompare, epochPut, err := s.activeFleetIngressClusterEpoch(ctx, cluster, "withdraw:"+planID+"\x00"+attemptID)
	if err != nil {
		return nil, nil, err
	}
	compares = append(compares, epochCompare)
	deletes = append(deletes, epochPut)
	return compares, deletes, nil
}

// CurrentActiveFleetIngressInventory does not let a caller choose a plan.
// It returns the server-selected inventory only while its pointer remains
// unchanged across the underlying attempt and checkpoint readback.
func (s *V3OperationStore) CurrentActiveFleetIngressInventory(ctx context.Context, cluster, environment string, port int) (*FleetIngressInventoryEvidence, error) {
	key := s.activeFleetIngressKey(cluster, environment)
	before, err := s.kv.Get(ctx, key)
	if err != nil || len(before.Kvs) != 1 {
		return nil, fmt.Errorf("active Fleet ingress inventory is unavailable")
	}
	var pointer activeFleetIngress
	if err := decodeV3Record(before.Kvs[0].Value, &pointer); err != nil || pointer.SchemaVersion != activeFleetIngressSchema || pointer.Cluster != cluster || pointer.Environment != environment {
		return nil, fmt.Errorf("active Fleet ingress inventory pointer is invalid")
	}
	epochKey := s.activeFleetIngressClusterEpochKey(cluster)
	epoch, err := s.kv.Get(ctx, epochKey)
	if err != nil || len(epoch.Kvs) != 1 || string(epoch.Kvs[0].Value) != pointer.PlanID+"\x00"+pointer.AttemptID {
		return nil, fmt.Errorf("active Fleet ingress inventory cluster generation is stale")
	}
	proof, err := s.CurrentFleetIngressInventory(ctx, pointer.PlanID, cluster, environment, port)
	if err != nil || proof.AttemptID != pointer.AttemptID || proof.CheckpointID != pointer.CheckpointID || proof.CheckpointModRevision != pointer.CheckpointRevision || proof.StateSerial != pointer.StateSerial || proof.Digest != pointer.Digest {
		return nil, fmt.Errorf("active Fleet ingress inventory differs from completed attempt")
	}
	after, err := s.kv.Get(ctx, key)
	if err != nil || len(after.Kvs) != 1 || after.Kvs[0].ModRevision != before.Kvs[0].ModRevision {
		return nil, fmt.Errorf("active Fleet ingress inventory changed during readback")
	}
	afterEpoch, err := s.kv.Get(ctx, epochKey)
	if err != nil || len(afterEpoch.Kvs) != 1 || afterEpoch.Kvs[0].ModRevision != epoch.Kvs[0].ModRevision {
		return nil, fmt.Errorf("Fleet ingress cluster generation changed during readback")
	}
	proof.ActivePointerRevision = before.Kvs[0].ModRevision
	proof.ActiveClusterEpochRevision = epoch.Kvs[0].ModRevision
	return proof, nil
}
