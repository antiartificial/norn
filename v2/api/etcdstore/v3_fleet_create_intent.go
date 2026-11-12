package etcdstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/fleetcreate"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

const (
	fleetCreateVerifierSubject = "fleet-create-verifier:work-staging"
	fleetCreateAttemptSubject  = "fleet-create-attempt:work-staging"
	fleetCreateFenceSchema     = "norn.fleet-create-cluster-fence/v1"
)

var ErrFleetCreateIntentRefused = errors.New("fresh Fleet create intent refused")

type fleetCreateFenceRecord struct {
	SchemaVersion     string             `json:"schemaVersion"`
	Intent            fleetcreate.Intent `json:"intent"`
	IntentSHA256      string             `json:"intentSha256"`
	CreatedAt         time.Time          `json:"createdAt"`
	CurrentAttemptID  string             `json:"currentAttemptId,omitempty"`
	AttemptGeneration uint64             `json:"attemptGeneration,omitempty"`
}

type fleetCreateAttemptRecord struct {
	Attempt   fleetcreate.Attempt             `json:"attempt"`
	Request   fleetcreate.AttemptStartRequest `json:"request"`
	CreatedAt time.Time                       `json:"createdAt"`
}

type fleetCreateDispatchRecord struct {
	Binding       fleetcreate.DispatchBinding `json:"binding"`
	BindingSHA256 string                      `json:"bindingSha256"`
	CreatedAt     time.Time                   `json:"createdAt"`
}

type fleetCreateAdmissionRecord struct {
	Admission       fleetcreate.Admission `json:"admission"`
	AdmissionSHA256 string                `json:"admissionSha256"`
	CreatedAt       time.Time             `json:"createdAt"`
}

type fleetCreateGrantRecord struct {
	Grant      fleetcreate.Grant `json:"grant"`
	CreatedAt  time.Time         `json:"createdAt"`
	Consumed   bool              `json:"consumed"`
	ConsumedAt *time.Time        `json:"consumedAt,omitempty"`
}

type fleetCreateRecoveryRecord struct {
	Readback  fleetcreate.RecoveryReadback `json:"readback"`
	Digest    string                       `json:"digest"`
	CreatedAt time.Time                    `json:"createdAt"`
}

type fleetCreateTerminalRecord struct {
	Receipt   fleetcreate.TerminalReceipt `json:"receipt"`
	Digest    string                      `json:"digest"`
	CreatedAt time.Time                   `json:"createdAt"`
}

func (s *V3OperationStore) fleetCreateFencePrefix() string {
	return s.prefix + "/v3/fleet-create-fences/"
}

func (s *V3OperationStore) fleetCreateFenceKey(cluster string) string {
	return s.fleetCreateFencePrefix() + fleetcreate.SHA256([]byte(cluster))
}

func (s *V3OperationStore) fleetCreateAttemptKey(intentID, attemptID string) string {
	return s.prefix + "/v3/fleet-create-attempts/" + intentID + "/" + attemptID
}

func (s *V3OperationStore) fleetCreateDispatchKey(intentID string) string {
	return s.prefix + "/v3/fleet-create-dispatches/" + intentID
}

func (s *V3OperationStore) fleetCreateAdmissionKey(intentID, attemptID string) string {
	return s.prefix + "/v3/fleet-create-admissions/" + intentID + "/" + attemptID
}

func (s *V3OperationStore) fleetCreateGrantKey(intentID, attemptID string) string {
	return s.prefix + "/v3/fleet-create-effect-grants/" + intentID + "/" + attemptID
}

func (s *V3OperationStore) fleetCreateRecoveryKey(intentID, priorAttemptID string) string {
	return s.prefix + "/v3/fleet-create-recovery-readbacks/" + intentID + "/" + priorAttemptID
}

func (s *V3OperationStore) fleetCreateTerminalKey(intentID string) string {
	return s.prefix + "/v3/fleet-create-terminal-receipts/" + intentID
}

func (s *V3OperationStore) fleetCreateGrantEventKey(intentID, attemptID string, sequence int) string {
	return fmt.Sprintf("%s/v3/fleet-create-effect-grant-events/%s/%s/%02d", s.prefix, intentID, attemptID, sequence)
}

// GetFleetCreateEffectGrant reconstructs the grant lifecycle from its mutable
// head and immutable issued/consumed events. It is read-only and fails closed
// on missing, contradictory, or substituted event bytes.
func (s *V3OperationStore) GetFleetCreateEffectGrant(ctx context.Context, intentID, attemptID string) (fleetcreate.Grant, bool, error) {
	var zero fleetcreate.Grant
	if s == nil || uuid.Validate(intentID) != nil || uuid.Validate(attemptID) != nil {
		return zero, false, ErrFleetCreateIntentRefused
	}
	grantKey := s.fleetCreateGrantKey(intentID, attemptID)
	issuedKey, consumedKey := s.fleetCreateGrantEventKey(intentID, attemptID, 1), s.fleetCreateGrantEventKey(intentID, attemptID, 2)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(grantKey), clientv3.OpGet(issuedKey), clientv3.OpGet(consumedKey)).Commit()
	if err != nil {
		return zero, false, err
	}
	if len(response.Responses) != 3 {
		return zero, false, ErrFleetCreateIntentRefused
	}
	grantRows, issuedRows, consumedRows := response.Responses[0].GetResponseRange().Kvs, response.Responses[1].GetResponseRange().Kvs, response.Responses[2].GetResponseRange().Kvs
	if len(grantRows) == 0 && len(issuedRows) == 0 && len(consumedRows) == 0 {
		return zero, false, ErrNotFound
	}
	if len(grantRows) != 1 || len(issuedRows) != 1 || len(consumedRows) > 1 ||
		(len(consumedRows) == 1 && bytes.Equal(issuedRows[0].Value, consumedRows[0].Value)) {
		return zero, false, ErrFleetCreateIntentRefused
	}
	var head, issued fleetCreateGrantRecord
	if decodeV3Record(grantRows[0].Value, &head) != nil || decodeV3Record(issuedRows[0].Value, &issued) != nil ||
		head.Grant != issued.Grant || head.CreatedAt != issued.CreatedAt || !head.Grant.MatchesRequest(fleetCreateRequestFromGrant(issued.Grant)) ||
		issued.Consumed || issued.ConsumedAt != nil || issued.CreatedAt.IsZero() {
		return zero, false, ErrFleetCreateIntentRefused
	}
	consumed := len(consumedRows) == 1
	if consumed {
		var event fleetCreateGrantRecord
		if decodeV3Record(consumedRows[0].Value, &event) != nil || event.Grant != issued.Grant || event.CreatedAt != issued.CreatedAt ||
			!event.Consumed || event.ConsumedAt == nil || event.ConsumedAt.Before(event.CreatedAt) || !head.Consumed || head.ConsumedAt == nil || *head.ConsumedAt != *event.ConsumedAt {
			return zero, false, ErrFleetCreateIntentRefused
		}
	} else if head.Consumed || head.ConsumedAt != nil || !bytes.Equal(grantRows[0].Value, issuedRows[0].Value) {
		return zero, false, ErrFleetCreateIntentRefused
	}
	return head.Grant, consumed, nil
}

func fleetCreateRequestFromGrant(grant fleetcreate.Grant) fleetcreate.Request {
	return fleetcreate.Request{SchemaVersion: fleetcreate.RequestSchema, IntentID: grant.IntentID, PlanID: grant.PlanID,
		AttemptID: grant.AttemptID, IntentSHA256: grant.IntentSHA256, AdmissionSHA256: grant.AdmissionSHA256,
		RemainderSHA256: grant.RemainderSHA256}
}

func (s *V3OperationStore) fleetCreateFenceKeyForIntent(ctx context.Context, intentID string) string {
	if s == nil || uuid.Validate(intentID) != nil {
		return ""
	}
	response, err := s.kv.Get(ctx, s.fleetCreateFencePrefix(), clientv3.WithPrefix())
	if err != nil {
		return ""
	}
	for _, row := range response.Kvs {
		var record fleetCreateFenceRecord
		if decodeV3Record(row.Value, &record) == nil && record.Intent.IntentID == intentID {
			return string(row.Key)
		}
	}
	return ""
}

// IssueFleetCreateEffectGrant durably authorizes one exact remainder after
// verifier readback. It records authority only; it does not call a provider.
func (s *V3OperationStore) IssueFleetCreateEffectGrant(ctx context.Context, request fleetcreate.Request) (fleetcreate.Grant, error) {
	var zero fleetcreate.Grant
	if s == nil || request.Validate() != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKeyForIntent(ctx, request.IntentID)
	if fenceKey == "" {
		return zero, ErrFleetCreateIntentRefused
	}
	grantKey := s.fleetCreateGrantKey(request.IntentID, request.AttemptID)
	fenceResponse, err := s.kv.Get(ctx, fenceKey)
	if err != nil {
		return zero, err
	}
	if len(fenceResponse.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKV := fenceResponse.Kvs[0]
	var fence fleetCreateFenceRecord
	if decodeV3Record(fenceKV.Value, &fence) != nil || fence.Intent.IntentID != request.IntentID ||
		fence.CurrentAttemptID != request.AttemptID || fence.AttemptGeneration == 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || intentDigest != fence.IntentSHA256 || request.IntentSHA256 != intentDigest {
		return zero, ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, request.PlanID)
	if err != nil {
		return zero, err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != request.PlanID || plan.Operation.Kind != "fleet.capacity-plan" ||
		plan.Operation.Status != model.OperationSucceeded || converge == nil || cluster != fence.Intent.Cluster ||
		converge.CommitSHA != fence.Intent.CommitSHA || plan.Operation.Ref != converge.Resource ||
		planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	keys := []string{s.fleetCreateDispatchKey(request.IntentID), s.fleetCreateAttemptKey(request.IntentID, request.AttemptID), s.fleetCreateAdmissionKey(request.IntentID, request.AttemptID), grantKey}
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(keys[0]), clientv3.OpGet(keys[1]), clientv3.OpGet(keys[2]), clientv3.OpGet(keys[3])).Commit()
	if err != nil {
		return zero, err
	}
	if len(response.Responses) != 4 {
		return zero, ErrFleetCreateIntentRefused
	}
	rows := make([]*mvccpb.KeyValue, 4)
	for i := range rows {
		values := response.Responses[i].GetResponseRange().Kvs
		if len(values) == 1 {
			rows[i] = values[0]
		}
	}
	if rows[0] == nil || rows[1] == nil || rows[2] == nil {
		return zero, ErrFleetCreateIntentRefused
	}
	issuedEventKey, consumedEventKey := s.fleetCreateGrantEventKey(request.IntentID, request.AttemptID, 1), s.fleetCreateGrantEventKey(request.IntentID, request.AttemptID, 2)
	events, err := s.kv.Txn(ctx).Then(clientv3.OpGet(issuedEventKey), clientv3.OpGet(consumedEventKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(events.Responses) != 2 {
		return zero, ErrFleetCreateIntentRefused
	}
	issuedRows, consumedRows := events.Responses[0].GetResponseRange().Kvs, events.Responses[1].GetResponseRange().Kvs
	var dispatch fleetCreateDispatchRecord
	var attempt fleetCreateAttemptRecord
	var admission fleetCreateAdmissionRecord
	if decodeV3Record(rows[0].Value, &dispatch) != nil || decodeV3Record(rows[1].Value, &attempt) != nil || decodeV3Record(rows[2].Value, &admission) != nil ||
		dispatch.BindingSHA256 != dispatch.Binding.Digest() || !dispatch.Binding.MatchesIntent(fence.Intent) || !dispatch.Binding.MatchesStart(attempt.Request) ||
		!attempt.Attempt.MatchesStart(attempt.Request) || attempt.Attempt.AttemptID != request.AttemptID ||
		admission.AdmissionSHA256 != admission.Admission.Digest() || !admission.Admission.MatchesIntent(fence.Intent) || admission.Admission.AttemptID != request.AttemptID ||
		!request.MatchesAdmission(admission.Admission) {
		return zero, ErrFleetCreateIntentRefused
	}
	if rows[3] != nil {
		var existing fleetCreateGrantRecord
		if decodeV3Record(rows[3].Value, &existing) == nil && existing.Grant.MatchesRequest(request) && len(issuedRows) == 1 && len(consumedRows) == 0 &&
			bytes.Equal(issuedRows[0].Value, rows[3].Value) && !existing.Consumed && existing.ConsumedAt == nil {
			return existing.Grant, nil
		}
		stored, consumed, readErr := s.GetFleetCreateEffectGrant(ctx, request.IntentID, request.AttemptID)
		if readErr == nil && !consumed && stored.MatchesRequest(request) {
			return stored, nil
		}
		return zero, ErrFleetCreateIntentRefused
	}
	if len(issuedRows) != 0 || len(consumedRows) != 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	grant := fleetcreate.GrantFromRequest(request)
	raw, err := json.Marshal(fleetCreateGrantRecord{Grant: grant, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceKV.ModRevision),
		clientv3.Compare(clientv3.ModRevision(keys[0]), "=", rows[0].ModRevision), clientv3.Compare(clientv3.ModRevision(keys[1]), "=", rows[1].ModRevision),
		clientv3.Compare(clientv3.ModRevision(keys[2]), "=", rows[2].ModRevision), clientv3.Compare(clientv3.ModRevision(s.opKey(request.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(grantKey), "=", 0), clientv3.Compare(clientv3.CreateRevision(issuedEventKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(consumedEventKey), "=", 0)).Then(clientv3.OpPut(grantKey, string(raw)), clientv3.OpPut(issuedEventKey, string(raw))).Commit()
	if err != nil {
		stored, consumed, readErr := s.GetFleetCreateEffectGrant(ctx, request.IntentID, request.AttemptID)
		if readErr == nil && !consumed && stored.MatchesRequest(request) {
			return stored, nil
		}
		return zero, err
	}
	if !committed.Succeeded {
		stored, consumed, readErr := s.GetFleetCreateEffectGrant(ctx, request.IntentID, request.AttemptID)
		if readErr == nil && !consumed && stored.MatchesRequest(request) {
			return stored, nil
		}
		return zero, ErrFleetCreateIntentRefused
	}
	return grant, nil
}

// ConsumeFleetCreateEffectGrant atomically records one-use consumption before
// any future provider executor may act. The returned grant is evidence only;
// this method performs no external effect.
func (s *V3OperationStore) ConsumeFleetCreateEffectGrant(ctx context.Context, request fleetcreate.Request) (fleetcreate.Grant, error) {
	var zero fleetcreate.Grant
	if s == nil || request.Validate() != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	grantKey := s.fleetCreateGrantKey(request.IntentID, request.AttemptID)
	issuedKey, consumedKey := s.fleetCreateGrantEventKey(request.IntentID, request.AttemptID, 1), s.fleetCreateGrantEventKey(request.IntentID, request.AttemptID, 2)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(grantKey), clientv3.OpGet(issuedKey), clientv3.OpGet(consumedKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(response.Responses) != 3 {
		return zero, ErrFleetCreateIntentRefused
	}
	grantRows, issuedRows, consumedRows := response.Responses[0].GetResponseRange().Kvs, response.Responses[1].GetResponseRange().Kvs, response.Responses[2].GetResponseRange().Kvs
	if len(grantRows) != 1 || len(issuedRows) != 1 || len(consumedRows) != 0 || !bytes.Equal(grantRows[0].Value, issuedRows[0].Value) {
		return zero, ErrFleetCreateIntentRefused
	}
	var record fleetCreateGrantRecord
	if decodeV3Record(grantRows[0].Value, &record) != nil || !record.Grant.MatchesRequest(request) || record.Consumed || record.ConsumedAt != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	consumedAt := time.Now().UTC().Truncate(time.Microsecond)
	record.Consumed, record.ConsumedAt = true, &consumedAt
	raw, err := json.Marshal(record)
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(grantKey), "=", grantRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(issuedKey), "=", issuedRows[0].ModRevision), clientv3.Compare(clientv3.CreateRevision(consumedKey), "=", 0)).
		Then(clientv3.OpPut(grantKey, string(raw)), clientv3.OpPut(consumedKey, string(raw))).Commit()
	if err != nil {
		// A committed consume with a lost reply is deliberately not returned as
		// a successful grant record. The caller must read status and reconcile
		// the provider/state effect before a verifier can admit a successor.
		return zero, err
	}
	if !committed.Succeeded {
		return zero, ErrFleetCreateIntentRefused
	}
	return record.Grant, nil
}

// RecordFleetCreateIntent durably claims a cluster before its first provider
// resource exists. The etcd compare-and-swap binds the signed capacity plan,
// the unregistered cluster alias, and the one-intent-per-cluster fence. It
// records no attempt and grants no provider effect; those are separate steps.
func (s *V3OperationStore) RecordFleetCreateIntent(ctx context.Context, intent fleetcreate.Intent, principal string) error {
	if s == nil || intent.Validate() != nil || principal != fleetCreateVerifierSubject {
		return ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, intent.PlanID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrFleetCreateIntentRefused
		}
		return err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != intent.PlanID || plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded ||
		converge == nil || cluster != intent.Cluster || converge.CommitSHA != intent.CommitSHA ||
		plan.Operation.Ref != converge.Resource || planDigest != "sha256:"+intent.PlanSHA256 {
		return ErrFleetCreateIntentRefused
	}
	intentDigest, err := intent.Digest()
	if err != nil {
		return ErrFleetCreateIntentRefused
	}
	key := s.fleetCreateFenceKey(intent.Cluster)
	aliasKey := s.fleetTargetAliasKey("cluster:" + intent.Cluster)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(key), clientv3.OpGet(aliasKey)).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 2 {
		return ErrFleetCreateIntentRefused
	}
	lockRows := response.Responses[0].GetResponseRange().Kvs
	aliasRows := response.Responses[1].GetResponseRange().Kvs
	if len(aliasRows) != 0 {
		return ErrFleetCreateIntentRefused
	}
	if len(lockRows) == 1 {
		var existing fleetCreateFenceRecord
		if decodeV3Record(lockRows[0].Value, &existing) != nil || existing.SchemaVersion != fleetCreateFenceSchema ||
			existing.IntentSHA256 != intentDigest || existing.Intent != intent {
			return ErrFleetCreateIntentRefused
		}
		return nil // exact idempotent replay; no new authority is created
	}
	if len(lockRows) != 0 {
		return ErrFleetCreateIntentRefused
	}
	record := fleetCreateFenceRecord{SchemaVersion: fleetCreateFenceSchema, Intent: intent,
		IntentSHA256: intentDigest, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(intent.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(aliasKey), "=", 0),
	).Then(clientv3.OpPut(key, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !committed.Succeeded {
		return ErrFleetCreateIntentRefused
	}
	return nil
}

func (s *V3OperationStore) GetFleetCreateIntent(ctx context.Context, cluster string) (fleetcreate.Intent, error) {
	var zero fleetcreate.Intent
	if s == nil || cluster == "" {
		return zero, ErrFleetCreateIntentRefused
	}
	response, err := s.kv.Get(ctx, s.fleetCreateFenceKey(cluster))
	if err != nil {
		return zero, err
	}
	if len(response.Kvs) == 0 {
		return zero, ErrNotFound
	}
	if len(response.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	var record fleetCreateFenceRecord
	if decodeV3Record(response.Kvs[0].Value, &record) != nil || record.SchemaVersion != fleetCreateFenceSchema ||
		record.Intent.Validate() != nil || record.Intent.Cluster != cluster {
		return zero, fmt.Errorf("fresh Fleet create fence is corrupt")
	}
	digest, err := record.Intent.Digest()
	if err != nil || digest != record.IntentSHA256 {
		return zero, fmt.Errorf("fresh Fleet create intent digest mismatch")
	}
	return record.Intent, nil
}

// RecordFleetCreateDispatch binds the verifier's exact protected workflow
// readback to an immutable pre-registration intent before a runner can start.
// The verifier must establish that the GitHub run was dispatched for this
// intent; Norn persists the binding and later compares the runner request.
func (s *V3OperationStore) RecordFleetCreateDispatch(ctx context.Context, binding fleetcreate.DispatchBinding, principal string) error {
	if s == nil || binding.Validate() != nil || principal != fleetCreateVerifierSubject {
		return ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(binding.Cluster)
	fenceResponse, err := s.kv.Get(ctx, fenceKey)
	if err != nil {
		return err
	}
	if len(fenceResponse.Kvs) != 1 {
		return ErrFleetCreateIntentRefused
	}
	fenceKV := fenceResponse.Kvs[0]
	var fence fleetCreateFenceRecord
	if decodeV3Record(fenceKV.Value, &fence) != nil || fence.SchemaVersion != fleetCreateFenceSchema ||
		!binding.MatchesIntent(fence.Intent) || fence.IntentSHA256 != binding.IntentSHA256 {
		return ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, binding.PlanID)
	if err != nil {
		return err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != binding.PlanID || plan.Operation.Kind != "fleet.capacity-plan" ||
		plan.Operation.Status != model.OperationSucceeded || converge == nil || cluster != binding.Cluster ||
		converge.CommitSHA != binding.CommitSHA || planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return ErrFleetCreateIntentRefused
	}
	key := s.fleetCreateDispatchKey(binding.IntentID)
	digest := binding.Digest()
	if digest == "" {
		return ErrFleetCreateIntentRefused
	}
	existing, err := s.kv.Get(ctx, key)
	if err != nil {
		return err
	}
	if len(existing.Kvs) == 1 {
		var record fleetCreateDispatchRecord
		if decodeV3Record(existing.Kvs[0].Value, &record) != nil || record.Binding != binding || record.BindingSHA256 != digest {
			return ErrFleetCreateIntentRefused
		}
		return nil
	}
	if len(existing.Kvs) != 0 {
		return ErrFleetCreateIntentRefused
	}
	if fence.CurrentAttemptID != "" || fence.AttemptGeneration != 0 {
		return ErrFleetCreateIntentRefused
	}
	raw, err := json.Marshal(fleetCreateDispatchRecord{Binding: binding, BindingSHA256: digest,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceKV.ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(binding.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
	).Then(clientv3.OpPut(key, string(raw))).Commit()
	if err != nil {
		return err
	}
	if committed.Succeeded {
		return nil
	}
	stored, readErr := s.GetFleetCreateDispatch(ctx, binding.IntentID)
	if readErr == nil && stored == binding {
		return nil
	}
	return ErrFleetCreateIntentRefused
}

func (s *V3OperationStore) GetFleetCreateDispatch(ctx context.Context, intentID string) (fleetcreate.DispatchBinding, error) {
	var zero fleetcreate.DispatchBinding
	if s == nil || uuid.Validate(intentID) != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	response, err := s.kv.Get(ctx, s.fleetCreateDispatchKey(intentID))
	if err != nil {
		return zero, err
	}
	if len(response.Kvs) == 0 {
		return zero, ErrNotFound
	}
	if len(response.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	var record fleetCreateDispatchRecord
	if decodeV3Record(response.Kvs[0].Value, &record) != nil || record.Binding.Validate() != nil || record.Binding.IntentID != intentID || record.BindingSHA256 != record.Binding.Digest() {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceResponse, err := s.kv.Get(ctx, s.fleetCreateFenceKey(record.Binding.Cluster))
	if err != nil {
		return zero, err
	}
	if len(fenceResponse.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	if decodeV3Record(fenceResponse.Kvs[0].Value, &fence) != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, digestErr := fence.Intent.Digest()
	if digestErr != nil ||
		intentDigest != fence.IntentSHA256 || !record.Binding.MatchesIntent(fence.Intent) {
		return zero, ErrFleetCreateIntentRefused
	}
	return record.Binding, nil
}

// StartFleetCreateAttempt records the first protected writer for a durable
// pre-registration intent. This only creates Norn attempt lineage; provider
// effects still require verifier admission and a separate one-use grant.
func (s *V3OperationStore) StartFleetCreateAttempt(ctx context.Context, request fleetcreate.AttemptStartRequest, principal string) (fleetcreate.Attempt, error) {
	var zero fleetcreate.Attempt
	if s == nil || request.Validate() != nil || principal != fleetCreateAttemptSubject {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(request.Cluster)
	fenceResponse, err := s.kv.Get(ctx, fenceKey)
	if err != nil {
		return zero, err
	}
	if len(fenceResponse.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKV := fenceResponse.Kvs[0]
	var fence fleetCreateFenceRecord
	if decodeV3Record(fenceKV.Value, &fence) != nil || fence.SchemaVersion != fleetCreateFenceSchema ||
		fence.Intent.Validate() != nil || fence.Intent.Cluster != request.Cluster ||
		fence.Intent.IntentID != request.IntentID || fence.Intent.PlanID != request.PlanID {
		return zero, ErrFleetCreateIntentRefused
	}
	dispatchKey := s.fleetCreateDispatchKey(request.IntentID)
	dispatchResponse, err := s.kv.Get(ctx, dispatchKey)
	if err != nil {
		return zero, err
	}
	if len(dispatchResponse.Kvs) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	dispatchKV := dispatchResponse.Kvs[0]
	var dispatch fleetCreateDispatchRecord
	if decodeV3Record(dispatchKV.Value, &dispatch) != nil || dispatch.BindingSHA256 != dispatch.Binding.Digest() ||
		!dispatch.Binding.MatchesIntent(fence.Intent) || !dispatch.Binding.MatchesStart(request) {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || intentDigest != fence.IntentSHA256 || intentDigest != request.IntentSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, request.PlanID)
	if err != nil {
		return zero, err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != request.PlanID || plan.Operation.Kind != "fleet.capacity-plan" ||
		plan.Operation.Status != model.OperationSucceeded || converge == nil || cluster != request.Cluster ||
		converge.CommitSHA != fence.Intent.CommitSHA || plan.Operation.Ref != converge.Resource ||
		planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	attemptKey := s.fleetCreateAttemptKey(request.IntentID, request.AttemptID)
	attempt := fleetcreate.AttemptFromStart(request)
	if fence.CurrentAttemptID != "" {
		if fence.CurrentAttemptID != request.AttemptID || fence.AttemptGeneration != 1 {
			return zero, ErrFleetCreateIntentRefused
		}
		attemptResponse, err := s.kv.Get(ctx, attemptKey)
		if err != nil {
			return zero, err
		}
		if len(attemptResponse.Kvs) != 1 {
			return zero, ErrFleetCreateIntentRefused
		}
		var stored fleetCreateAttemptRecord
		if decodeV3Record(attemptResponse.Kvs[0].Value, &stored) != nil || stored.Request != request ||
			!stored.Attempt.MatchesStart(stored.Request) || !stored.Attempt.MatchesStart(request) {
			return zero, ErrFleetCreateIntentRefused
		}
		return stored.Attempt, nil
	}
	if fence.AttemptGeneration != 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	attemptResponse, err := s.kv.Get(ctx, attemptKey)
	if err != nil {
		return zero, err
	}
	if len(attemptResponse.Kvs) != 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	fence.CurrentAttemptID, fence.AttemptGeneration = request.AttemptID, 1
	fenceRaw, err := json.Marshal(fence)
	if err != nil {
		return zero, err
	}
	attemptRaw, err := json.Marshal(fleetCreateAttemptRecord{Attempt: attempt, Request: request, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceKV.ModRevision),
		clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", dispatchKV.ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(request.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0),
	).Then(clientv3.OpPut(fenceKey, string(fenceRaw)), clientv3.OpPut(attemptKey, string(attemptRaw))).Commit()
	if err != nil {
		return zero, err
	}
	if !committed.Succeeded {
		return zero, ErrFleetCreateIntentRefused
	}
	return attempt, nil
}

// RecordFleetCreateRecoveryReadback records the verifier's exact observation
// for the current attempt only after its one-use grant was consumed. The record
// is immutable and authorizes no effect by itself.
func (s *V3OperationStore) RecordFleetCreateRecoveryReadback(ctx context.Context, readback fleetcreate.RecoveryReadback, principal string) error {
	if s == nil || principal != fleetCreateVerifierSubject || readback.Validate() != nil {
		return ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(readback.Cluster)
	attemptKey := s.fleetCreateAttemptKey(readback.IntentID, readback.PriorAttemptID)
	grantKey := s.fleetCreateGrantKey(readback.IntentID, readback.PriorAttemptID)
	recoveryKey := s.fleetCreateRecoveryKey(readback.IntentID, readback.PriorAttemptID)
	dispatchKey := s.fleetCreateDispatchKey(readback.IntentID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey), clientv3.OpGet(grantKey),
		clientv3.OpGet(s.fleetCreateGrantEventKey(readback.IntentID, readback.PriorAttemptID, 1)),
		clientv3.OpGet(s.fleetCreateGrantEventKey(readback.IntentID, readback.PriorAttemptID, 2)),
		clientv3.OpGet(recoveryKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 7 {
		return ErrFleetCreateIntentRefused
	}
	rows := make([][]*mvccpb.KeyValue, 7)
	for i := range rows {
		rows[i] = response.Responses[i].GetResponseRange().Kvs
	}
	if len(rows[0]) != 1 || len(rows[1]) != 1 || len(rows[2]) != 1 || len(rows[3]) != 1 || len(rows[4]) != 1 || len(rows[6]) != 1 {
		return ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	var attempt fleetCreateAttemptRecord
	var grant, issued, consumed fleetCreateGrantRecord
	var dispatch fleetCreateDispatchRecord
	if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &attempt) != nil ||
		decodeV3Record(rows[2][0].Value, &grant) != nil || decodeV3Record(rows[3][0].Value, &issued) != nil ||
		decodeV3Record(rows[4][0].Value, &consumed) != nil || decodeV3Record(rows[6][0].Value, &dispatch) != nil {
		return ErrFleetCreateIntentRefused
	}
	digest, err := fence.Intent.Digest()
	if err != nil || fence.IntentSHA256 != digest || !readback.MatchesIntent(fence.Intent) ||
		fence.AttemptGeneration < attempt.Attempt.Generation ||
		attempt.Attempt.IntentID != readback.IntentID || !attempt.Attempt.MatchesStart(attempt.Request) ||
		grant.Grant.IntentID != readback.IntentID || grant.Grant.AttemptID != readback.PriorAttemptID ||
		!grant.Grant.MatchesRequest(fleetCreateRequestFromGrant(grant.Grant)) || !grant.Consumed || grant.ConsumedAt == nil ||
		!bytes.Equal(rows[3][0].Value, func() []byte {
			raw, _ := json.Marshal(fleetCreateGrantRecord{Grant: issued.Grant, CreatedAt: issued.CreatedAt})
			return raw
		}()) ||
		!consumed.Consumed || consumed.ConsumedAt == nil || consumed.Grant != grant.Grant || !bytes.Equal(rows[2][0].Value, rows[4][0].Value) ||
		dispatch.BindingSHA256 != dispatch.Binding.Digest() || !dispatch.Binding.MatchesIntent(fence.Intent) || !dispatch.Binding.MatchesStart(attempt.Request) ||
		readback.ExpectedNodeCount != 9 || readback.PriorGrantSHA256 != grant.Grant.Digest() {
		return ErrFleetCreateIntentRefused
	}
	if len(rows[5]) == 1 {
		var existing fleetCreateRecoveryRecord
		if decodeV3Record(rows[5][0].Value, &existing) == nil && existing.Readback == readback && existing.Digest == readback.Digest() {
			return nil
		}
		return ErrFleetCreateIntentRefused
	}
	if len(rows[5]) != 0 {
		return ErrFleetCreateIntentRefused
	}
	if fence.CurrentAttemptID != readback.PriorAttemptID || fence.AttemptGeneration != attempt.Attempt.Generation {
		return ErrFleetCreateIntentRefused
	}
	record := fleetCreateRecoveryRecord{Readback: readback, Digest: readback.Digest(), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(fenceKey), "=", rows[0][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", rows[1][0].ModRevision), clientv3.Compare(clientv3.ModRevision(grantKey), "=", rows[2][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", rows[6][0].ModRevision), clientv3.Compare(clientv3.CreateRevision(recoveryKey), "=", 0)).
		Then(clientv3.OpPut(recoveryKey, string(raw))).Commit()
	if err != nil {
		stored, readErr := s.kv.Get(ctx, recoveryKey)
		if readErr == nil && len(stored.Kvs) == 1 {
			var existing fleetCreateRecoveryRecord
			if decodeV3Record(stored.Kvs[0].Value, &existing) == nil && existing.Readback == readback && existing.Digest == readback.Digest() {
				return nil
			}
		}
		return err
	}
	if !committed.Succeeded {
		return ErrFleetCreateIntentRefused
	}
	return nil
}

// StartFleetCreateRecoveryAttempt advances the same intent only after exact
// verifier recovery readback and durable consumption of the previous grant.
func (s *V3OperationStore) StartFleetCreateRecoveryAttempt(ctx context.Context, request fleetcreate.AttemptStartRequest, priorAttemptID, principal string) (fleetcreate.Attempt, error) {
	var zero fleetcreate.Attempt
	if s == nil || request.Validate() != nil || principal != fleetCreateAttemptSubject || uuid.Validate(priorAttemptID) != nil || priorAttemptID == request.AttemptID {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(request.Cluster)
	attemptKey := s.fleetCreateAttemptKey(request.IntentID, request.AttemptID)
	priorAttemptKey := s.fleetCreateAttemptKey(request.IntentID, priorAttemptID)
	grantKey := s.fleetCreateGrantKey(request.IntentID, priorAttemptID)
	recoveryKey := s.fleetCreateRecoveryKey(request.IntentID, priorAttemptID)
	dispatchKey := s.fleetCreateDispatchKey(request.IntentID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(priorAttemptKey), clientv3.OpGet(grantKey),
		clientv3.OpGet(s.fleetCreateGrantEventKey(request.IntentID, priorAttemptID, 1)),
		clientv3.OpGet(s.fleetCreateGrantEventKey(request.IntentID, priorAttemptID, 2)), clientv3.OpGet(recoveryKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(response.Responses) != 7 {
		return zero, ErrFleetCreateIntentRefused
	}
	rows := make([][]*mvccpb.KeyValue, 7)
	for i := range rows {
		rows[i] = response.Responses[i].GetResponseRange().Kvs
	}
	if len(rows[0]) != 1 || len(rows[1]) != 1 || len(rows[2]) != 1 || len(rows[3]) != 1 || len(rows[4]) != 1 || len(rows[5]) != 1 || len(rows[6]) != 1 {
		return zero, ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	var prior fleetCreateAttemptRecord
	var grant, issued, consumed fleetCreateGrantRecord
	var recovery fleetCreateRecoveryRecord
	var dispatch fleetCreateDispatchRecord
	if decodeV3Record(rows[0][0].Value, &fence) != nil || decodeV3Record(rows[1][0].Value, &prior) != nil ||
		decodeV3Record(rows[2][0].Value, &grant) != nil || decodeV3Record(rows[3][0].Value, &issued) != nil ||
		decodeV3Record(rows[4][0].Value, &consumed) != nil || decodeV3Record(rows[5][0].Value, &recovery) != nil ||
		decodeV3Record(rows[6][0].Value, &dispatch) != nil {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || fence.IntentSHA256 != intentDigest || fence.Intent.Cluster != request.Cluster || fence.Intent.IntentID != request.IntentID ||
		(fence.CurrentAttemptID != priorAttemptID && fence.CurrentAttemptID != request.AttemptID) ||
		fence.AttemptGeneration < prior.Attempt.Generation || fence.AttemptGeneration > prior.Attempt.Generation+1 ||
		prior.Attempt.AttemptID != priorAttemptID || !prior.Attempt.MatchesStart(prior.Request) ||
		!grant.Consumed || grant.ConsumedAt == nil || grant.Grant.AttemptID != priorAttemptID ||
		!bytes.Equal(rows[3][0].Value, func() []byte {
			raw, _ := json.Marshal(fleetCreateGrantRecord{Grant: issued.Grant, CreatedAt: issued.CreatedAt})
			return raw
		}()) ||
		!consumed.Consumed || consumed.Grant != grant.Grant || !bytes.Equal(rows[2][0].Value, rows[4][0].Value) || recovery.Readback.Digest() != recovery.Digest ||
		recovery.Readback.PriorAttemptID != priorAttemptID || !recovery.Readback.MatchesIntent(fence.Intent) ||
		recovery.Readback.PriorGrantSHA256 != grant.Grant.Digest() || !dispatch.Binding.MatchesStart(request) ||
		request.IntentSHA256 != intentDigest || request.PlanID != fence.Intent.PlanID {
		return zero, ErrFleetCreateIntentRefused
	}
	plan, planRevision, err := s.load(ctx, request.PlanID)
	if err != nil {
		return zero, err
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != request.PlanID || plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded ||
		converge == nil || cluster != request.Cluster || converge.CommitSHA != fence.Intent.CommitSHA || planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	if fence.CurrentAttemptID == request.AttemptID && fence.AttemptGeneration == prior.Attempt.Generation+1 {
		stored, readErr := s.kv.Get(ctx, attemptKey)
		if readErr != nil {
			return zero, readErr
		}
		if len(stored.Kvs) != 1 {
			return zero, ErrFleetCreateIntentRefused
		}
		var record fleetCreateAttemptRecord
		if decodeV3Record(stored.Kvs[0].Value, &record) != nil || record.Request != request ||
			record.Attempt != fleetcreate.AttemptForGeneration(request, fence.AttemptGeneration) || !record.Attempt.MatchesStart(request) {
			return zero, ErrFleetCreateIntentRefused
		}
		return record.Attempt, nil
	}
	if fence.CurrentAttemptID != priorAttemptID || fence.AttemptGeneration != prior.Attempt.Generation {
		return zero, ErrFleetCreateIntentRefused
	}
	if fence.AttemptGeneration >= 9 {
		return zero, ErrFleetCreateIntentRefused
	}
	if existing, getErr := s.kv.Get(ctx, attemptKey); getErr != nil {
		return zero, getErr
	} else if len(existing.Kvs) != 0 {
		return zero, ErrFleetCreateIntentRefused
	}
	priorFence := rows[0][0]
	fence.CurrentAttemptID, fence.AttemptGeneration = request.AttemptID, fence.AttemptGeneration+1
	fenceRaw, err := json.Marshal(fence)
	if err != nil {
		return zero, err
	}
	attempt := fleetcreate.AttemptForGeneration(request, fence.AttemptGeneration)
	attemptRaw, err := json.Marshal(fleetCreateAttemptRecord{Attempt: attempt, Request: request, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return zero, err
	}
	committed, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(fenceKey), "=", priorFence.ModRevision),
		clientv3.Compare(clientv3.ModRevision(priorAttemptKey), "=", rows[1][0].ModRevision), clientv3.Compare(clientv3.ModRevision(grantKey), "=", rows[2][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(recoveryKey), "=", rows[5][0].ModRevision), clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", rows[6][0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(s.opKey(request.PlanID)), "=", planRevision), clientv3.Compare(clientv3.CreateRevision(attemptKey), "=", 0)).
		Then(clientv3.OpPut(fenceKey, string(fenceRaw)), clientv3.OpPut(attemptKey, string(attemptRaw))).Commit()
	if err != nil {
		fenceReadback, fenceErr := s.kv.Get(ctx, fenceKey)
		attemptReadback, attemptErr := s.kv.Get(ctx, attemptKey)
		if fenceErr == nil && attemptErr == nil && len(fenceReadback.Kvs) == 1 && len(attemptReadback.Kvs) == 1 {
			var storedFence fleetCreateFenceRecord
			var storedAttempt fleetCreateAttemptRecord
			if decodeV3Record(fenceReadback.Kvs[0].Value, &storedFence) == nil && decodeV3Record(attemptReadback.Kvs[0].Value, &storedAttempt) == nil &&
				storedFence.CurrentAttemptID == request.AttemptID && storedFence.AttemptGeneration == prior.Attempt.Generation+1 &&
				storedAttempt.Request == request && storedAttempt.Attempt == attempt {
				return attempt, nil
			}
		}
		return zero, err
	}
	if !committed.Succeeded {
		return zero, ErrFleetCreateIntentRefused
	}
	return attempt, nil
}

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

func (s *V3OperationStore) GetFleetCreateAttempt(ctx context.Context, cluster, intentID, attemptID string) (fleetcreate.Attempt, error) {
	var zero fleetcreate.Attempt
	if s == nil {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceResponse, err := s.kv.Get(ctx, s.fleetCreateFenceKey(cluster))
	if err != nil {
		return zero, err
	}
	if len(fenceResponse.Kvs) != 1 {
		return zero, ErrNotFound
	}
	var fence fleetCreateFenceRecord
	if decodeV3Record(fenceResponse.Kvs[0].Value, &fence) != nil || fence.SchemaVersion != fleetCreateFenceSchema ||
		fence.Intent.Validate() != nil || fence.Intent.Cluster != cluster || fence.Intent.IntentID != intentID {
		return zero, ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || intentDigest != fence.IntentSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	response, err := s.kv.Get(ctx, s.fleetCreateAttemptKey(intentID, attemptID))
	if err != nil {
		return zero, err
	}
	if len(response.Kvs) != 1 {
		return zero, ErrNotFound
	}
	var record fleetCreateAttemptRecord
	if decodeV3Record(response.Kvs[0].Value, &record) != nil || record.Attempt.SchemaVersion != fleetcreate.AttemptSchema ||
		!record.Attempt.MatchesStart(record.Request) ||
		record.Attempt.Generation > fence.AttemptGeneration ||
		record.Request.Cluster != cluster || record.Request.IntentID != intentID || record.Request.PlanID != fence.Intent.PlanID ||
		record.Attempt.IntentID != intentID || record.Attempt.PlanID != fence.Intent.PlanID ||
		record.Attempt.AttemptID != attemptID || record.Attempt.IntentSHA256 != fence.IntentSHA256 {
		return zero, ErrFleetCreateIntentRefused
	}
	dispatch, err := s.GetFleetCreateDispatch(ctx, intentID)
	if err != nil || !dispatch.MatchesStart(record.Request) {
		return zero, ErrFleetCreateIntentRefused
	}
	return record.Attempt, nil
}

// RecordFleetCreateAdmission stores exact verifier readback for the current
// admitted attempt. This is an opaque assertion from Norn's perspective and
// does not authorize a provider effect; the later one-use grant performs that
// separate compare-and-swap.
func (s *V3OperationStore) RecordFleetCreateAdmission(ctx context.Context, admission fleetcreate.Admission, principal string) error {
	if s == nil || admission.Validate() != nil || principal != fleetCreateVerifierSubject {
		return ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(admission.Cluster)
	attemptKey := s.fleetCreateAttemptKey(admission.IntentID, admission.AttemptID)
	dispatchKey := s.fleetCreateDispatchKey(admission.IntentID)
	admissionKey := s.fleetCreateAdmissionKey(admission.IntentID, admission.AttemptID)
	planKey := s.opKey(admission.PlanID)
	first, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey),
		clientv3.OpGet(admissionKey), clientv3.OpGet(planKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return err
	}
	if len(first.Responses) != 5 {
		return ErrFleetCreateIntentRefused
	}
	fenceRows := first.Responses[0].GetResponseRange().Kvs
	attemptRows := first.Responses[1].GetResponseRange().Kvs
	admissionRows := first.Responses[2].GetResponseRange().Kvs
	planRows := first.Responses[3].GetResponseRange().Kvs
	dispatchRows := first.Responses[4].GetResponseRange().Kvs
	if len(fenceRows) != 1 || len(attemptRows) != 1 || len(planRows) != 1 || len(dispatchRows) != 1 {
		return ErrFleetCreateIntentRefused
	}
	var fence fleetCreateFenceRecord
	var attempt fleetCreateAttemptRecord
	var dispatch fleetCreateDispatchRecord
	var plan v3Record
	if decodeV3Record(fenceRows[0].Value, &fence) != nil || decodeV3Record(attemptRows[0].Value, &attempt) != nil ||
		decodeV3Record(dispatchRows[0].Value, &dispatch) != nil || decodeV3Record(planRows[0].Value, &plan) != nil || fence.SchemaVersion != fleetCreateFenceSchema ||
		fence.Intent.Validate() != nil || attempt.Attempt.SchemaVersion != fleetcreate.AttemptSchema ||
		!attempt.Attempt.MatchesStart(attempt.Request) {
		return ErrFleetCreateIntentRefused
	}
	intentDigest, err := fence.Intent.Digest()
	if err != nil || intentDigest != fence.IntentSHA256 || admission.IntentID != fence.Intent.IntentID ||
		admission.PlanID != fence.Intent.PlanID || admission.Cluster != fence.Intent.Cluster ||
		fence.CurrentAttemptID != admission.AttemptID || fence.AttemptGeneration != attempt.Attempt.Generation ||
		attempt.Attempt.IntentID != admission.IntentID || attempt.Attempt.PlanID != admission.PlanID ||
		attempt.Attempt.AttemptID != admission.AttemptID || attempt.Attempt.IntentSHA256 != intentDigest ||
		attempt.Request.Cluster != admission.Cluster || attempt.Request.IntentID != admission.IntentID ||
		dispatch.BindingSHA256 != dispatch.Binding.Digest() || !dispatch.Binding.MatchesIntent(fence.Intent) ||
		!dispatch.Binding.MatchesStart(attempt.Request) ||
		!admission.MatchesIntent(fence.Intent) {
		return ErrFleetCreateIntentRefused
	}
	converge, planDigest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.ID != admission.PlanID || plan.Operation.Kind != "fleet.capacity-plan" ||
		plan.Operation.Status != model.OperationSucceeded || converge == nil || cluster != admission.Cluster ||
		converge.CommitSHA != fence.Intent.CommitSHA || plan.Operation.Ref != converge.Resource ||
		planDigest != "sha256:"+fence.Intent.PlanSHA256 {
		return ErrFleetCreateIntentRefused
	}
	if len(admissionRows) == 1 {
		var existing fleetCreateAdmissionRecord
		if decodeV3Record(admissionRows[0].Value, &existing) != nil || existing.Admission != admission ||
			existing.AdmissionSHA256 != admission.Digest() {
			return ErrFleetCreateIntentRefused
		}
		return nil // exact immutable replay
	}
	if len(admissionRows) != 0 {
		return ErrFleetCreateIntentRefused
	}
	record := fleetCreateAdmissionRecord{Admission: admission, AdmissionSHA256: admission.Digest(),
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	committed, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", dispatchRows[0].ModRevision),
		clientv3.Compare(clientv3.ModRevision(planKey), "=", planRows[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(admissionKey), "=", 0),
	).Then(clientv3.OpPut(admissionKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !committed.Succeeded {
		stored, readErr := s.GetFleetCreateAdmission(ctx, admission.Cluster, admission.IntentID, admission.AttemptID)
		if readErr == nil && stored == admission {
			return nil
		}
		return ErrFleetCreateIntentRefused
	}
	return nil
}

func (s *V3OperationStore) GetFleetCreateAdmission(ctx context.Context, cluster, intentID, attemptID string) (fleetcreate.Admission, error) {
	var zero fleetcreate.Admission
	if s == nil {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceKey := s.fleetCreateFenceKey(cluster)
	attemptKey := s.fleetCreateAttemptKey(intentID, attemptID)
	dispatchKey := s.fleetCreateDispatchKey(intentID)
	admissionKey := s.fleetCreateAdmissionKey(intentID, attemptID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(fenceKey), clientv3.OpGet(attemptKey),
		clientv3.OpGet(admissionKey), clientv3.OpGet(dispatchKey)).Commit()
	if err != nil {
		return zero, err
	}
	if len(response.Responses) != 4 {
		return zero, ErrFleetCreateIntentRefused
	}
	fenceRows := response.Responses[0].GetResponseRange().Kvs
	attemptRows := response.Responses[1].GetResponseRange().Kvs
	admissionRows := response.Responses[2].GetResponseRange().Kvs
	dispatchRows := response.Responses[3].GetResponseRange().Kvs
	if len(fenceRows) != 1 || len(attemptRows) != 1 || len(admissionRows) != 1 || len(dispatchRows) != 1 {
		return zero, ErrNotFound
	}
	var fence fleetCreateFenceRecord
	var attempt fleetCreateAttemptRecord
	var dispatch fleetCreateDispatchRecord
	var record fleetCreateAdmissionRecord
	if decodeV3Record(fenceRows[0].Value, &fence) != nil || decodeV3Record(attemptRows[0].Value, &attempt) != nil ||
		decodeV3Record(admissionRows[0].Value, &record) != nil || decodeV3Record(dispatchRows[0].Value, &dispatch) != nil ||
		fence.SchemaVersion != fleetCreateFenceSchema ||
		fence.Intent.Validate() != nil || attempt.Attempt.SchemaVersion != fleetcreate.AttemptSchema ||
		!attempt.Attempt.MatchesStart(attempt.Request) || record.Admission.Validate() != nil ||
		record.AdmissionSHA256 != record.Admission.Digest() {
		return zero, ErrFleetCreateIntentRefused
	}
	admission := record.Admission
	intentDigest, err := fence.Intent.Digest()
	if err != nil || intentDigest != fence.IntentSHA256 || fence.Intent.Cluster != cluster ||
		fence.Intent.IntentID != intentID || fence.Intent.PlanID != admission.PlanID ||
		fence.AttemptGeneration < attempt.Attempt.Generation ||
		attempt.Attempt.IntentID != intentID || attempt.Attempt.PlanID != fence.Intent.PlanID ||
		attempt.Attempt.AttemptID != attemptID || attempt.Attempt.IntentSHA256 != intentDigest ||
		attempt.Request.Cluster != cluster || attempt.Request.IntentID != intentID ||
		dispatch.BindingSHA256 != dispatch.Binding.Digest() || !dispatch.Binding.MatchesIntent(fence.Intent) ||
		!dispatch.Binding.MatchesStart(attempt.Request) ||
		!admission.MatchesIntent(fence.Intent) ||
		admission.AttemptID != attemptID || attempt.Request.Cluster != cluster {
		return zero, ErrFleetCreateIntentRefused
	}
	return admission, nil
}
