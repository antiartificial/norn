package etcdstore

// This aggregate keeps the one-time GitHub dispatch nonce private in etcd
// until a verified workflow run is bound. A lost response can therefore be
// reconciled with the exact nonce instead of issuing a second dispatch.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

var ErrFleetGitHubDispatchBound = errors.New("fleet GitHub dispatch is already bound")

const fleetGitHubDispatchOperationKind = "fleet.github.apply-dispatch"

// FleetGitHubDispatchPreparation is private server state. DispatchNonce must
// never be copied into an HTTP response, an operation payload, or a receipt.
type FleetGitHubDispatchPreparation struct {
	PlanID           string
	PlanRunID        int64
	PlanSHA256       string
	ApprovedHeadSHA  string
	FleetEnvironment string
	AllowDestructive bool
	// OperationID is the signed pre-effect receipt. It is deliberately public
	// provenance, unlike DispatchNonce.
	OperationID         string
	DispatchNonce       string
	DispatchNonceSHA256 string
}

type v3FleetGitHubDispatchPreparation struct {
	FleetGitHubDispatchPreparation
	CreatedAt time.Time `json:"createdAt"`
}

func (s *V3OperationStore) fleetGitHubDispatchPreparationKey(planID string) string {
	return s.prefix + "/v3/fleet-github-dispatch-preparations/" + planID
}

// AcceptFleetGitHubDispatch atomically stores the private dispatch nonce with
// a signed, queued immutable operation. Callers must use the returned
// preparation as the sole external-dispatch input. This is intentionally a
// separate aggregate from generic Accept: an ambiguous GitHub write must never
// be followed by a new receipt or nonce after replay expiry.
func (s *V3OperationStore) AcceptFleetGitHubDispatch(ctx context.Context, input store.OperationAcceptance, prepared FleetGitHubDispatchPreparation) (store.AcceptedOperation, FleetGitHubDispatchPreparation, error) {
	if s == nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, fmt.Errorf("etcd fleet GitHub dispatch store is unavailable")
	}
	prepared = normalizedFleetGitHubDispatchPreparation(prepared)
	if err := validateFleetGitHubDispatchAcceptance(input, prepared); err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	acceptanceKey := s.acceptanceKey(acceptance.Identity)
	if existing, loadErr := s.loadAcceptance(ctx, acceptanceKey); loadErr == nil {
		accepted, replayErr := s.replay(ctx, acceptanceKey, existing, acceptance.Identity, acceptance.Fingerprint)
		if replayErr != nil {
			return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, replayErr
		}
		stored, storedErr := s.GetFleetGitHubDispatchPreparation(ctx, prepared.PlanID)
		if storedErr != nil || !sameFleetGitHubDispatchPreparation(stored, prepared) || stored.OperationID != accepted.Operation.ID {
			return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, &store.AcceptanceConflictError{Identity: acceptance.Identity}
		}
		return accepted, stored, nil
	} else if !errors.Is(loadErr, ErrNotFound) {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, loadErr
	}
	plan, planRevision, err := s.load(ctx, prepared.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, fmt.Errorf("load fleet plan: %w", err)
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch requires a successful immutable fleet plan")
	}
	if err := validateFleetGitHubDispatchPlanBinding(plan.Operation, acceptance.Operation, prepared); err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	nonce, err := fleetGitHubDispatchNonce()
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	prepared.DispatchNonce = nonce
	digest := sha256.Sum256([]byte(nonce))
	prepared.DispatchNonceSHA256 = hex.EncodeToString(digest[:])
	prepared.OperationID = acceptance.Operation.ID
	now := time.Now().UTC().Truncate(time.Microsecond)
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, now)
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
	acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	opRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	prepRecord, err := json.Marshal(v3FleetGitHubDispatchPreparation{FleetGitHubDispatchPreparation: prepared, CreatedAt: now})
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	key := s.fleetGitHubDispatchPreparationKey(prepared.PlanID)
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(prepared.PlanID)), "=", planRevision), clientv3.Compare(clientv3.CreateRevision(acceptanceKey), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(key), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(prepared.PlanID)), "=", 0)).Then(clientv3.OpPut(acceptanceKey, string(acceptanceRecord)), clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(opRecord)), clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, now, acceptance.Operation.ID), acceptance.Operation.ID), clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), acceptanceKey), clientv3.OpPut(key, string(prepRecord))).Commit()
	if err != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, err
	}
	if txn.Succeeded {
		return accepted, prepared, nil
	}
	existing, loadErr := s.loadAcceptance(ctx, acceptanceKey)
	if loadErr != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch reservation already exists or fleet plan changed")
	}
	accepted, replayErr := s.replay(ctx, acceptanceKey, existing, acceptance.Identity, acceptance.Fingerprint)
	if replayErr != nil {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, replayErr
	}
	stored, storedErr := s.GetFleetGitHubDispatchPreparation(ctx, prepared.PlanID)
	if storedErr != nil || !sameFleetGitHubDispatchPreparation(stored, prepared) || stored.OperationID != accepted.Operation.ID {
		return store.AcceptedOperation{}, FleetGitHubDispatchPreparation{}, &store.AcceptanceConflictError{Identity: acceptance.Identity}
	}
	return accepted, stored, nil
}

// PrepareFleetGitHubDispatch is retained only as an explicit fail-closed
// compatibility boundary. A nonce without a signed operation is unsafe.
func (s *V3OperationStore) PrepareFleetGitHubDispatch(context.Context, FleetGitHubDispatchPreparation) (FleetGitHubDispatchPreparation, bool, error) {
	return FleetGitHubDispatchPreparation{}, false, fmt.Errorf("unsigned Fleet GitHub dispatch preparation is no longer supported; use AcceptFleetGitHubDispatch")
}

// GetFleetGitHubDispatchPreparation returns private server state for recovery.
// Callers must keep DispatchNonce out of every public response and receipt.
func (s *V3OperationStore) GetFleetGitHubDispatchPreparation(ctx context.Context, planID string) (FleetGitHubDispatchPreparation, error) {
	response, err := s.kv.Get(ctx, s.fleetGitHubDispatchPreparationKey(strings.TrimSpace(planID)))
	if err != nil {
		return FleetGitHubDispatchPreparation{}, err
	}
	if len(response.Kvs) != 1 {
		return FleetGitHubDispatchPreparation{}, ErrNotFound
	}
	var record v3FleetGitHubDispatchPreparation
	if err := decodeV3Record(response.Kvs[0].Value, &record); err != nil {
		return FleetGitHubDispatchPreparation{}, err
	}
	if record.PlanID != strings.TrimSpace(planID) || record.OperationID == "" || uuid.Validate(record.OperationID) != nil || !validFleetGitHubDispatchPreparation(record.FleetGitHubDispatchPreparation) {
		return FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch preparation is corrupt")
	}
	digest := sha256.Sum256([]byte(record.DispatchNonce))
	if !strings.EqualFold(record.DispatchNonceSHA256, hex.EncodeToString(digest[:])) {
		return FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch preparation nonce is corrupt")
	}
	return record.FleetGitHubDispatchPreparation, nil
}

// FinishFleetGitHubDispatch atomically writes the immutable runner binding and
// a signed terminal completion whose canonical result names that recovered run.
// A bound dispatch without this terminal receipt is never reported as complete.
func (s *V3OperationStore) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceSHA256 string, runID int64, workflowURL string) error {
	prepared, err := s.GetFleetGitHubDispatchPreparation(ctx, planID)
	if err != nil {
		return err
	}
	if err := s.VerifyFleetGitHubDispatchReservation(ctx, prepared); err != nil {
		return err
	}
	if nonceSHA256 != prepared.DispatchNonceSHA256 {
		return fmt.Errorf("fleet GitHub dispatch nonce does not match preparation")
	}
	if runID <= 0 || !fleetWorkflowURL(strings.TrimSpace(workflowURL)) {
		return fmt.Errorf("fleet GitHub dispatch result is invalid")
	}
	op, opRevision, err := s.load(ctx, prepared.OperationID)
	if err != nil {
		return fmt.Errorf("load fleet GitHub dispatch receipt: %w", err)
	}
	binding := FleetRunnerDispatchBinding{PlanID: prepared.PlanID, PlanSHA256: prepared.PlanSHA256, ApprovedHeadSHA: prepared.ApprovedHeadSHA, DispatchNonceSHA256: prepared.DispatchNonceSHA256, RunID: runID, WorkflowURL: strings.TrimSpace(workflowURL)}
	if op.Operation.Status == model.OperationSucceeded {
		if existing, bindErr := s.loadFleetRunnerDispatch(ctx, prepared.PlanID); bindErr == nil && existing.FleetRunnerDispatchBinding == binding {
			return s.VerifyFleetGitHubDispatchCompletion(ctx, binding)
		}
	}
	if op.Operation.Kind != fleetGitHubDispatchOperationKind || op.Operation.Ref != prepared.PlanID || op.Operation.Status != model.OperationQueued {
		return fmt.Errorf("fleet GitHub dispatch receipt is not a queued immutable reservation")
	}
	result := map[string]interface{}{"planId": prepared.PlanID, "runId": runID, "url": strings.TrimSpace(workflowURL), "planRunId": prepared.PlanRunID, "planSha256": prepared.PlanSHA256, "approvedHeadSha": prepared.ApprovedHeadSHA, "fleetEnvironment": prepared.FleetEnvironment, "allowDestructive": prepared.AllowDestructive}
	canonical, err := json.Marshal(struct {
		Schema      string                 `json:"schema"`
		OperationID string                 `json:"operationId"`
		PlanID      string                 `json:"planId"`
		Kind        string                 `json:"kind"`
		Status      model.OperationStatus  `json:"status"`
		Result      map[string]interface{} `json:"result"`
	}{"norn.fleet-github-completion/v1", op.Operation.ID, prepared.PlanID, fleetGitHubDispatchOperationKind, model.OperationSucceeded, result})
	if err != nil {
		return err
	}
	signature, err := s.signer.Sign(ctx, canonical)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	completed := op.Operation
	completed.Status, completed.Message, completed.FinishedAt = model.OperationSucceeded, "protected fleet apply dispatched", &now
	completed.UpdatedAt = now
	completed.Payload = make(map[string]interface{}, len(op.Operation.Payload)+len(result))
	for key, value := range op.Operation.Payload {
		completed.Payload[key] = value
	}
	for key, value := range result {
		completed.Payload[key] = value
	}
	if completed.Metadata == nil {
		completed.Metadata = map[string]interface{}{}
	}
	completed.Metadata["fleetGitHubCompletion"] = map[string]interface{}{"schema": "norn.fleet-github-completion/v1", "canonicalBytes": base64.StdEncoding.EncodeToString(canonical), "signingAlgorithm": signature.Algorithm, "signingKeyId": signature.KeyID, "signature": signature.Value, "result": result}
	opRecord, err := json.Marshal(v3Record{Operation: completed})
	if err != nil {
		return err
	}
	bindingRecord, err := json.Marshal(v3FleetRunnerDispatch{FleetRunnerDispatchBinding: binding, CreatedAt: now})
	if err != nil {
		return err
	}
	state, err := json.Marshal(v3FleetRunnerPlanState{Version: 1})
	if err != nil {
		return err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(prepared.PlanID)), "!=", 0), clientv3.Compare(clientv3.ModRevision(s.opKey(prepared.OperationID)), "=", opRevision), clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(prepared.PlanID)), "=", 0), clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerPlanStateKey(prepared.PlanID)), "=", 0)).Then(clientv3.OpPut(s.opKey(prepared.OperationID), string(opRecord)), clientv3.OpPut(s.fleetRunnerDispatchKey(prepared.PlanID), string(bindingRecord)), clientv3.OpPut(s.fleetRunnerPlanStateKey(prepared.PlanID), string(state))).Commit()
	if err != nil {
		return err
	}
	if txn.Succeeded {
		return nil
	}
	if existing, bindErr := s.loadFleetRunnerDispatch(ctx, prepared.PlanID); bindErr == nil && existing.FleetRunnerDispatchBinding == binding {
		finished, _, finishErr := s.load(ctx, prepared.OperationID)
		if finishErr == nil && finished.Operation.Status == model.OperationSucceeded {
			return s.VerifyFleetGitHubDispatchCompletion(ctx, binding)
		}
	}
	return fmt.Errorf("fleet GitHub dispatch binding already exists or receipt changed")
}

func (s *V3OperationStore) GetFleetRunnerDispatchBinding(ctx context.Context, planID string) (FleetRunnerDispatchBinding, error) {
	record, err := s.loadFleetRunnerDispatch(ctx, strings.TrimSpace(planID))
	if err != nil {
		return FleetRunnerDispatchBinding{}, err
	}
	return record.FleetRunnerDispatchBinding, nil
}

// VerifyFleetGitHubDispatchReservation re-proves the signed pre-effect
// acceptance before a recovered private nonce can be sent to GitHub.
func (s *V3OperationStore) VerifyFleetGitHubDispatchReservation(ctx context.Context, prepared FleetGitHubDispatchPreparation) error {
	if prepared.OperationID == "" {
		return fmt.Errorf("fleet GitHub dispatch reservation is unsigned")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(prepared.OperationID))
	if err != nil || len(index.Kvs) != 1 {
		return fmt.Errorf("load fleet GitHub dispatch acceptance: %w", err)
	}
	loaded, err := s.loadAcceptance(ctx, string(index.Kvs[0].Value))
	if err != nil {
		return err
	}
	accepted, err := s.replay(ctx, string(index.Kvs[0].Value), loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return err
	}
	if accepted.Operation.ID != prepared.OperationID || accepted.Operation.Kind != fleetGitHubDispatchOperationKind || accepted.Operation.Ref != prepared.PlanID {
		return fmt.Errorf("fleet GitHub dispatch acceptance differs from preparation")
	}
	plan, _, err := s.load(ctx, prepared.PlanID)
	if err != nil {
		return err
	}
	return validateFleetGitHubDispatchPlanBinding(plan.Operation, accepted.Operation, prepared)
}

// VerifyFleetGitHubDispatchCompletion proves that the returned dispatch has a
// signed terminal receipt for exactly the immutable runner binding.
func (s *V3OperationStore) VerifyFleetGitHubDispatchCompletion(ctx context.Context, binding FleetRunnerDispatchBinding) error {
	prepared, err := s.GetFleetGitHubDispatchPreparation(ctx, binding.PlanID)
	if err != nil {
		return err
	}
	if err := s.VerifyFleetGitHubDispatchReservation(ctx, prepared); err != nil {
		return err
	}
	op, _, err := s.load(ctx, prepared.OperationID)
	if err != nil {
		return err
	}
	if op.Operation.Status != model.OperationSucceeded || op.Operation.Kind != fleetGitHubDispatchOperationKind || op.Operation.Ref != binding.PlanID {
		return fmt.Errorf("fleet GitHub dispatch terminal receipt is invalid")
	}
	completion, ok := op.Operation.Metadata["fleetGitHubCompletion"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("fleet GitHub dispatch completion is absent")
	}
	encoded, _ := completion["canonicalBytes"].(string)
	canonical, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode fleet GitHub dispatch completion: %w", err)
	}
	signature := store.AcceptanceSignature{Algorithm: stringValue(completion["signingAlgorithm"]), KeyID: stringValue(completion["signingKeyId"]), Value: stringValue(completion["signature"])}
	if err := s.signer.Verify(ctx, signature, canonical); err != nil {
		return fmt.Errorf("verify fleet GitHub dispatch completion: %w", err)
	}
	var signed struct {
		Schema, OperationID, PlanID, Kind string
		Status                            model.OperationStatus
		Result                            struct {
			PlanSHA256, ApprovedHeadSHA, URL, FleetEnvironment string
			RunID, PlanRunID                                   int64
			AllowDestructive                                   bool
		}
	}
	if err := json.Unmarshal(canonical, &signed); err != nil || signed.Schema != "norn.fleet-github-completion/v1" || signed.OperationID != op.Operation.ID || signed.PlanID != binding.PlanID || signed.Kind != fleetGitHubDispatchOperationKind || signed.Status != model.OperationSucceeded || signed.Result.PlanSHA256 != binding.PlanSHA256 || signed.Result.ApprovedHeadSHA != binding.ApprovedHeadSHA || signed.Result.RunID != binding.RunID || signed.Result.PlanRunID != prepared.PlanRunID || signed.Result.FleetEnvironment != prepared.FleetEnvironment || signed.Result.AllowDestructive != prepared.AllowDestructive || strings.TrimSpace(signed.Result.URL) != binding.WorkflowURL {
		return fmt.Errorf("fleet GitHub dispatch completion does not bind runner result")
	}
	var resultEnvelope struct {
		Result map[string]interface{} `json:"result"`
	}
	if err := decodeV3Record(canonical, &resultEnvelope); err != nil || len(resultEnvelope.Result) == 0 {
		return fmt.Errorf("fleet GitHub dispatch completion result is invalid")
	}
	for key, value := range resultEnvelope.Result {
		if !reflect.DeepEqual(op.Operation.Payload[key], value) {
			return fmt.Errorf("fleet GitHub dispatch operation payload differs from signed completion")
		}
	}
	return nil
}

func stringValue(value interface{}) string { result, _ := value.(string); return result }

func normalizedFleetGitHubDispatchPreparation(input FleetGitHubDispatchPreparation) FleetGitHubDispatchPreparation {
	input.PlanID = strings.TrimSpace(input.PlanID)
	input.PlanSHA256 = strings.TrimSpace(input.PlanSHA256)
	input.ApprovedHeadSHA = strings.TrimSpace(input.ApprovedHeadSHA)
	input.FleetEnvironment = strings.TrimSpace(input.FleetEnvironment)
	input.DispatchNonce = strings.TrimSpace(input.DispatchNonce)
	input.DispatchNonceSHA256 = strings.TrimSpace(input.DispatchNonceSHA256)
	return input
}

func validFleetGitHubDispatchPreparation(input FleetGitHubDispatchPreparation) bool {
	_, planErr := uuid.Parse(input.PlanID)
	return planErr == nil && input.PlanRunID > 0 && fleetLowerHex(input.PlanSHA256, 64) && fleetLowerHex(input.ApprovedHeadSHA, 40) &&
		(input.FleetEnvironment == "staging/nyc3" || input.FleetEnvironment == "production/nyc3") &&
		(input.DispatchNonce == "" || fleetLowerHex(input.DispatchNonce, 64)) &&
		(input.OperationID == "" || uuid.Validate(input.OperationID) == nil) &&
		(input.DispatchNonceSHA256 == "" || fleetLowerHex(input.DispatchNonceSHA256, 64))
}

func sameFleetGitHubDispatchPreparation(left, right FleetGitHubDispatchPreparation) bool {
	return left.PlanID == right.PlanID && left.PlanRunID == right.PlanRunID && left.PlanSHA256 == right.PlanSHA256 &&
		left.ApprovedHeadSHA == right.ApprovedHeadSHA && left.FleetEnvironment == right.FleetEnvironment &&
		left.AllowDestructive == right.AllowDestructive
}

func validateFleetGitHubDispatchAcceptance(input store.OperationAcceptance, prepared FleetGitHubDispatchPreparation) error {
	if !validFleetGitHubDispatchPreparation(prepared) || prepared.OperationID != "" {
		return fmt.Errorf("fleet GitHub dispatch preparation is incomplete")
	}
	if input.Identity.Kind != fleetGitHubDispatchOperationKind || input.Identity.Resource != prepared.PlanID || input.Operation.Kind != fleetGitHubDispatchOperationKind || input.Operation.Ref != prepared.PlanID || input.Operation.Status != model.OperationQueued {
		return fmt.Errorf("fleet GitHub dispatch acceptance does not bind the reservation")
	}
	return nil
}

func validateFleetGitHubDispatchPlanBinding(plan model.Operation, operation model.Operation, prepared FleetGitHubDispatchPreparation) error {
	encoded, err := json.Marshal(plan.Payload)
	if err != nil {
		return err
	}
	var typed fleet.CapacityPlan
	if err := json.Unmarshal(encoded, &typed); err != nil || typed.ID != prepared.PlanID || typed.Digest == "" {
		return fmt.Errorf("fleet GitHub dispatch requires a complete immutable capacity plan payload")
	}
	encoded, err = json.Marshal(operation.Payload)
	if err != nil {
		return err
	}
	var envelope struct {
		FleetGitHub struct {
			PlanID, PlanDigest, SourceDigest              string
			PlanRunID                                     int64
			PlanSHA256, ApprovedHeadSHA, FleetEnvironment string
			AllowDestructive                              bool
		} `json:"fleetGitHub"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return fmt.Errorf("signed Fleet GitHub dispatch payload does not match immutable inputs")
	}
	payload := envelope.FleetGitHub
	if payload.PlanID != prepared.PlanID || payload.PlanDigest != typed.Digest || payload.SourceDigest != typed.SourceDigest || payload.PlanRunID != prepared.PlanRunID || payload.PlanSHA256 != prepared.PlanSHA256 || payload.ApprovedHeadSHA != prepared.ApprovedHeadSHA || payload.FleetEnvironment != prepared.FleetEnvironment || payload.AllowDestructive != prepared.AllowDestructive {
		return fmt.Errorf("signed Fleet GitHub dispatch payload does not match immutable inputs")
	}
	return nil
}

func fleetGitHubDispatchNonce() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate fleet GitHub dispatch nonce: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
