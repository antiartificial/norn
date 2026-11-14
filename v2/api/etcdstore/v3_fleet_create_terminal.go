package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/fleetcreate"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// RecordFleetCreateTerminalReceipt records complete verifier readback for the
// current attempt only after its exact one-use grant was consumed. It remains
// an opaque verifier assertion; it does not perform provider or state effects.
func (s *V3OperationStore) RecordFleetCreateTerminalReceipt(ctx context.Context, receipt fleetcreate.TerminalReceipt, principal string) error {
	if s == nil || principal != fleetCreateVerifierSubject || receipt.Validate() != nil {
		return ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(receipt.Cluster)
	attemptKey := s.fleetCreateAttemptKey(receipt.IntentID, receipt.AttemptID)
	admissionKey := s.fleetCreateAdmissionKey(receipt.IntentID, receipt.AttemptID)
	grantKey := s.fleetCreateGrantKey(receipt.IntentID, receipt.AttemptID)
	issuedKey := s.fleetCreateGrantEventKey(receipt.IntentID, receipt.AttemptID, 1)
	consumedKey := s.fleetCreateGrantEventKey(receipt.IntentID, receipt.AttemptID, 2)
	planKey := s.opKey(receipt.PlanID)
	terminalKey := s.fleetCreateTerminalKey(receipt.IntentID)
	sourceRevisions := make([]int64, 7)
	read := func() (fleetCreateTerminalRecord, int64, error) {
		response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey), clientv3.OpGet(admissionKey),
			clientv3.OpGet(grantKey), clientv3.OpGet(issuedKey), clientv3.OpGet(consumedKey), clientv3.OpGet(planKey), clientv3.OpGet(terminalKey)).Commit()
		if err != nil {
			return fleetCreateTerminalRecord{}, 0, err
		}
		if len(response.Responses) != 8 {
			return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
		}
		rows := make([][]*mvccpb.KeyValue, 8)
		for i := range rows {
			rows[i] = response.Responses[i].GetResponseRange().Kvs
		}
		for _, index := range []int{0, 1, 2, 3, 4, 5, 6} {
			if len(rows[index]) != 1 {
				return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
			}
			sourceRevisions[index] = rows[index][0].ModRevision
		}
		var fence fleetCreateFenceRecord
		var attempt fleetCreateAttemptRecord
		var admission fleetCreateAdmissionRecord
		var grant, issued, consumed fleetCreateGrantRecord
		var plan v3Record
		if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &attempt) != nil ||
			decodeV3Record(rows[2][0].Value, &admission) != nil || decodeV3Record(rows[3][0].Value, &grant) != nil ||
			decodeV3Record(rows[4][0].Value, &issued) != nil || decodeV3Record(rows[5][0].Value, &consumed) != nil ||
			decodeV3Record(rows[6][0].Value, &plan) != nil {
			return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
		}
		intentDigest, err := fence.Intent.Digest()
		if err != nil || fence.SchemaVersion != fleetCreateFenceSchema || fence.IntentSHA256 != intentDigest ||
			!receipt.MatchesIntent(fence.Intent) || fence.CurrentAttemptID != receipt.AttemptID ||
			fence.AttemptGeneration != attempt.Attempt.Generation || attempt.Attempt.IntentID != receipt.IntentID ||
			!attempt.Attempt.MatchesStart(attempt.Request) || admission.AdmissionSHA256 != admission.Admission.Digest() ||
			!admission.Admission.MatchesIntent(fence.Intent) || admission.Admission.AttemptID != receipt.AttemptID ||
			!grant.Grant.MatchesRequest(fleetCreateRequestFromGrant(grant.Grant)) || grant.Grant.IntentID != receipt.IntentID ||
			grant.Grant.AttemptID != receipt.AttemptID || !grant.Consumed || grant.ConsumedAt == nil ||
			!issued.Grant.MatchesRequest(fleetCreateRequestFromGrant(issued.Grant)) || issued.Grant != grant.Grant ||
			!consumed.Consumed || consumed.Grant != grant.Grant || !bytes.Equal(rows[4][0].Value, func() []byte {
			raw, _ := json.Marshal(fleetCreateGrantRecord{Grant: issued.Grant, CreatedAt: issued.CreatedAt})
			return raw
		}()) || !bytes.Equal(rows[3][0].Value, rows[5][0].Value) ||
			receipt.AdmissionSHA256 != admission.AdmissionSHA256 || receipt.GrantSHA256 != grant.Grant.Digest() {
			return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
		}
		converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
		if plan.Operation.ID != receipt.PlanID || plan.Operation.Kind != "fleet.capacity-plan" ||
			plan.Operation.Status != model.OperationSucceeded || converge == nil || cluster != receipt.Cluster ||
			converge.CommitSHA != fence.Intent.CommitSHA || planDigest != "sha256:"+fence.Intent.PlanSHA256 {
			return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
		}
		if len(rows[7]) == 1 {
			var existing fleetCreateTerminalRecord
			if decodeV3Record(rows[7][0].Value, &existing) != nil || existing.Receipt != receipt || existing.Digest != receipt.Digest() {
				return fleetCreateTerminalRecord{}, 0, ErrFleetCreateIntentRefused
			}
			return existing, rows[7][0].ModRevision, nil
		}
		return fleetCreateTerminalRecord{}, 0, nil
	}
	if existing, _, err := read(); err != nil {
		return err
	} else if existing.Digest != "" {
		return nil
	}
	record := fleetCreateTerminalRecord{Receipt: receipt, Digest: receipt.Digest(), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	keys := []string{fenceKey, attemptKey, admissionKey, grantKey, issuedKey, consumedKey, planKey}
	compares := make([]clientv3.Cmp, 0, len(keys)+1)
	for i, key := range keys {
		if sourceRevisions[i] == 0 {
			return ErrFleetCreateIntentRefused
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", sourceRevisions[i]))
	}
	compares = append(compares, clientv3.Compare(clientv3.CreateRevision(terminalKey), "=", 0))
	committed, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(terminalKey, string(raw))).Commit()
	if err != nil {
		stored, _, readErr := read()
		if readErr == nil && stored.Receipt == receipt && stored.Digest == receipt.Digest() {
			return nil
		}
		return err
	}
	if !committed.Succeeded {
		stored, _, readErr := read()
		if readErr == nil && stored.Receipt == receipt && stored.Digest == receipt.Digest() {
			return nil
		}
		return ErrFleetCreateIntentRefused
	}
	return nil
}

// GetFleetCreateTerminalReceipt returns the exact immutable terminal evidence.
func (s *V3OperationStore) GetFleetCreateTerminalReceipt(ctx context.Context, intentID string) (fleetcreate.TerminalReceipt, error) {
	var zero fleetcreate.TerminalReceipt
	if s == nil || uuid.Validate(intentID) != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	response, err := s.kv.Get(ctx, s.fleetCreateTerminalKey(intentID))
	if err != nil {
		return zero, err
	}
	if len(response.Kvs) == 0 {
		return zero, ErrNotFound
	}
	var record fleetCreateTerminalRecord
	if len(response.Kvs) != 1 || decodeV3Record(response.Kvs[0].Value, &record) != nil ||
		record.Receipt.Validate() != nil || record.Receipt.IntentID != intentID || record.Digest != record.Receipt.Digest() {
		return zero, ErrFleetCreateIntentRefused
	}
	return record.Receipt, nil
}

// RegisterFleetCreateTerminalTarget transfers the exact pre-registration
// cluster fence to the ordinary registered-target fence in one etcd Txn.
func (s *V3OperationStore) RegisterFleetCreateTerminalTarget(ctx context.Context, receipt fleetcreate.TerminalReceipt) (*FleetTarget, error) {
	if s == nil || receipt.Validate() != nil {
		return nil, ErrFleetCreateIntentRefused
	}
	terminalKey := s.fleetCreateTerminalKey(receipt.IntentID)
	terminalResponse, err := s.kv.Get(ctx, terminalKey)
	if err != nil {
		return nil, err
	}
	if len(terminalResponse.Kvs) != 1 {
		return nil, ErrFleetCreateIntentRefused
	}
	var terminal fleetCreateTerminalRecord
	if decodeV3Record(terminalResponse.Kvs[0].Value, &terminal) != nil || terminal.Receipt != receipt || terminal.Digest != receipt.Digest() {
		return nil, ErrFleetCreateIntentRefused
	}
	identity := lifecycle.TargetIdentity{Provider: receipt.Provider, ProviderAccount: receipt.ProviderAccount, StateBackend: receipt.StateBackend}
	canon, targetID, err := lifecycle.CanonicalTarget(identity)
	if err != nil {
		return nil, ErrFleetCreateIntentRefused
	}
	operationID := "fleet-create-terminal:" + receipt.Digest()
	clusterAlias := lifecycle.AliasKindCluster + ":" + receipt.Cluster
	clusterNames, environments, unique, err := classifyFleetTargetAliases([]string{clusterAlias})
	if err != nil {
		return nil, err
	}
	for range fleetTargetRegisterAttempts {
		fenceKey := s.fleetCreateFenceKey(receipt.Cluster)
		fenceResponse, readErr := s.kv.Get(ctx, fenceKey)
		if readErr != nil {
			return nil, readErr
		}
		if len(fenceResponse.Kvs) == 0 {
			target, getErr := s.GetFleetTarget(ctx, targetID)
			if getErr == nil && target != nil && target.RegistrationOperationID == operationID && containsFleetTargetAlias(target.Aliases, clusterAlias) {
				return target, nil // committed handoff replay after a lost response
			}
			return nil, ErrFleetCreateIntentRefused
		}
		var fence fleetCreateFenceRecord
		if decodeV3Record(fenceResponse.Kvs[0].Value, &fence) != nil || fence.Intent.IntentID != receipt.IntentID || fence.CurrentAttemptID != receipt.AttemptID {
			return nil, ErrFleetCreateIntentRefused
		}
		intentDigest, digestErr := fence.Intent.Digest()
		if digestErr != nil || fence.IntentSHA256 != intentDigest || !receipt.MatchesIntent(fence.Intent) {
			return nil, ErrFleetCreateIntentRefused
		}
		if target, getErr := s.GetFleetTarget(ctx, targetID); getErr != nil {
			return nil, getErr
		} else if target != nil && target.RegistrationOperationID != operationID {
			return nil, ErrFleetCreateIntentRefused
		}
		target, plan, prepErr := s.prepareFleetTargetRegistration(ctx, canon, targetID, operationID, clusterNames, environments, unique, receipt.IntentID)
		if prepErr != nil {
			return nil, prepErr
		}
		if len(plan.puts) == 0 {
			continue
		}
		compares := append([]clientv3.Cmp{}, plan.compares...)
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceResponse.Kvs[0].ModRevision),
			clientv3.Compare(clientv3.ModRevision(terminalKey), "=", terminalResponse.Kvs[0].ModRevision))
		puts := append([]clientv3.Op{}, plan.puts...)
		puts = append(puts, clientv3.OpDelete(fenceKey))
		committed, commitErr := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
		if commitErr != nil {
			stored, getErr := s.GetFleetTarget(ctx, targetID)
			if getErr == nil && stored != nil && stored.RegistrationOperationID == operationID && containsFleetTargetAlias(stored.Aliases, clusterAlias) {
				return stored, nil
			}
			return nil, commitErr
		}
		if committed.Succeeded {
			return target, nil
		}
	}
	return nil, fmt.Errorf("fresh Fleet target handoff changed concurrently")
}

func containsFleetTargetAlias(aliases []string, want string) bool {
	for _, alias := range aliases {
		if alias == want {
			return true
		}
	}
	return false
}
