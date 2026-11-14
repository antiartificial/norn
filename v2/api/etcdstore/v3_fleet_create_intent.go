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
