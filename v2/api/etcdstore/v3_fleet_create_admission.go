package etcdstore

import (
	"context"
	"encoding/json"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/fleetcreate"
	"norn/v2/api/model"
)

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
