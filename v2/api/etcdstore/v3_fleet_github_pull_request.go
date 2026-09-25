package etcdstore

// The pull-request reservation is the pre-effect half of the Fleet GitHub
// bridge.  It gives a later HTTP adapter one immutable, signed intent per
// successful capacity plan before it can ask GitHub to create a branch or PR.
// The adapter remains responsible for proving and recording the remote result.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/githubapp"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

const fleetGitHubPullRequestOperationKind = "fleet.github.pull-request"

// FleetGitHubPullRequestReservation is the complete immutable input for a
// deterministic Fleet PR. The later GitHub call must use these values rather
// than a new copy of the capacity plan.
type FleetGitHubPullRequestReservation struct {
	PlanID       string         `json:"planId"`
	PlanDigest   string         `json:"planDigest"`
	SourceDigest string         `json:"sourceDigest"`
	Pool         string         `json:"pool"`
	Action       string         `json:"action"`
	Proposed     fleet.NodePool `json:"proposed"`
	OperationID  string         `json:"operationId"`
	CreatedAt    time.Time      `json:"createdAt"`
}

func (s *V3OperationStore) fleetGitHubPullRequestKey(planID string) string {
	return s.prefix + "/v3/fleet-github-pull-requests/" + planID
}

// AcceptFleetGitHubPullRequest atomically persists the signed queued
// operation and the plan-scoped GitHub reservation. Replay never expires:
// an expired identity could otherwise permit a second receipt after an
// ambiguous external GitHub result.
func (s *V3OperationStore) AcceptFleetGitHubPullRequest(ctx context.Context, input store.OperationAcceptance, reservation FleetGitHubPullRequestReservation) (store.AcceptedOperation, error) {
	if s == nil {
		return store.AcceptedOperation{}, fmt.Errorf("etcd fleet GitHub pull-request store is unavailable")
	}
	// This aggregate deliberately writes its acceptance record without replay
	// expiry. A normal router may use a finite default TTL for ordinary work,
	// but this identity guards an external write and must remain recoverable.
	reservation = normalizeFleetGitHubPullRequestReservation(reservation)
	if err := validateFleetGitHubPullRequestAcceptance(input, reservation); err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	acceptanceKey := s.acceptanceKey(acceptance.Identity)
	if existing, loadErr := s.loadAcceptance(ctx, acceptanceKey); loadErr == nil {
		accepted, replayErr := s.replay(ctx, acceptanceKey, existing, acceptance.Identity, acceptance.Fingerprint)
		if replayErr != nil {
			return store.AcceptedOperation{}, replayErr
		}
		if reservationErr := s.verifyFleetGitHubPullRequestReservation(ctx, reservation, accepted.Operation.ID); reservationErr != nil {
			return store.AcceptedOperation{}, reservationErr
		}
		return accepted, nil
	} else if !errors.Is(loadErr, ErrNotFound) {
		return store.AcceptedOperation{}, loadErr
	}

	plan, planRevision, err := s.load(ctx, reservation.PlanID)
	if err != nil {
		return store.AcceptedOperation{}, fmt.Errorf("load fleet plan: %w", err)
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return store.AcceptedOperation{}, fmt.Errorf("fleet GitHub pull-request requires a successful immutable fleet plan")
	}
	if err := validateFleetGitHubPullRequestPlanBinding(plan.Operation, acceptance.Operation, reservation); err != nil {
		return store.AcceptedOperation{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	reservation.OperationID, reservation.CreatedAt = acceptance.Operation.ID, now
	identityID, intentID := uuid.NewString(), uuid.NewString()
	intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, now)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
	acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	operationRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	reservationRecord, err := json.Marshal(reservation)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	reservationKey := s.fleetGitHubPullRequestKey(reservation.PlanID)
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(reservation.PlanID)), "=", planRevision),
		clientv3.Compare(clientv3.CreateRevision(acceptanceKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(reservationKey), "=", 0),
	).Then(
		clientv3.OpPut(acceptanceKey, string(acceptanceRecord)),
		clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)),
		clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, now, acceptance.Operation.ID), acceptance.Operation.ID),
		clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), acceptanceKey),
		clientv3.OpPut(reservationKey, string(reservationRecord)),
	).Commit()
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if txn.Succeeded {
		return accepted, nil
	}
	if existing, loadErr := s.loadAcceptance(ctx, acceptanceKey); loadErr == nil {
		accepted, replayErr := s.replay(ctx, acceptanceKey, existing, acceptance.Identity, acceptance.Fingerprint)
		if replayErr != nil {
			return store.AcceptedOperation{}, replayErr
		}
		if reservationErr := s.verifyFleetGitHubPullRequestReservation(ctx, reservation, accepted.Operation.ID); reservationErr != nil {
			return store.AcceptedOperation{}, reservationErr
		}
		return accepted, nil
	}
	return store.AcceptedOperation{}, fmt.Errorf("fleet GitHub pull-request reservation already exists or fleet plan changed")
}

// GetFleetGitHubPullRequestReservation returns the immutable pre-effect input
// for a later GitHub adapter. It has no completion result and exposes no
// secret material.
func (s *V3OperationStore) GetFleetGitHubPullRequestReservation(ctx context.Context, planID string) (FleetGitHubPullRequestReservation, error) {
	planID = strings.TrimSpace(planID)
	if _, err := uuid.Parse(planID); err != nil {
		return FleetGitHubPullRequestReservation{}, fmt.Errorf("fleet GitHub pull-request plan ID must be a UUID")
	}
	response, err := s.kv.Get(ctx, s.fleetGitHubPullRequestKey(planID))
	if err != nil {
		return FleetGitHubPullRequestReservation{}, err
	}
	if len(response.Kvs) != 1 {
		return FleetGitHubPullRequestReservation{}, ErrNotFound
	}
	var reservation FleetGitHubPullRequestReservation
	if err := decodeV3Record(response.Kvs[0].Value, &reservation); err != nil {
		return FleetGitHubPullRequestReservation{}, err
	}
	reservation = normalizeFleetGitHubPullRequestReservation(reservation)
	if err := validateStoredFleetGitHubPullRequestReservation(reservation); err != nil {
		return FleetGitHubPullRequestReservation{}, fmt.Errorf("fleet GitHub pull-request reservation is corrupt: %w", err)
	}
	return reservation, nil
}

func (s *V3OperationStore) verifyFleetGitHubPullRequestReservation(ctx context.Context, expected FleetGitHubPullRequestReservation, operationID string) error {
	stored, err := s.GetFleetGitHubPullRequestReservation(ctx, expected.PlanID)
	if err != nil {
		return fmt.Errorf("load Fleet GitHub pull-request reservation: %w", err)
	}
	if stored.OperationID != operationID || !sameFleetGitHubPullRequestReservation(stored, expected) {
		return fmt.Errorf("Fleet GitHub pull-request reservation differs from accepted intent")
	}
	return nil
}

func normalizeFleetGitHubPullRequestReservation(value FleetGitHubPullRequestReservation) FleetGitHubPullRequestReservation {
	value.PlanID = strings.TrimSpace(value.PlanID)
	value.PlanDigest = strings.TrimSpace(value.PlanDigest)
	value.SourceDigest = strings.TrimSpace(value.SourceDigest)
	value.Pool = strings.TrimSpace(value.Pool)
	value.Action = strings.TrimSpace(value.Action)
	value.OperationID = strings.TrimSpace(value.OperationID)
	return value
}

func validateFleetGitHubPullRequestAcceptance(input store.OperationAcceptance, reservation FleetGitHubPullRequestReservation) error {
	if err := validateStoredFleetGitHubPullRequestReservation(reservation); err != nil && reservation.OperationID != "" {
		return err
	}
	if _, err := uuid.Parse(reservation.PlanID); err != nil || reservation.PlanDigest == "" || reservation.SourceDigest == "" || reservation.Pool == "" || reservation.Action == "" || reservation.OperationID != "" {
		return fmt.Errorf("Fleet GitHub pull-request reservation is incomplete")
	}
	if input.Identity.Kind != fleetGitHubPullRequestOperationKind || input.Identity.Resource != reservation.PlanID || input.Operation.Kind != fleetGitHubPullRequestOperationKind || input.Operation.Ref != reservation.PlanID || input.Operation.Status != model.OperationQueued {
		return fmt.Errorf("Fleet GitHub pull-request acceptance does not bind the reservation")
	}
	return nil
}

// validateFleetGitHubPullRequestPlanBinding prevents a caller from reserving
// arbitrary GitHub content under a valid plan ID. Both the reservation and the
// signed operation payload must reproduce the immutable plan's exact intent.
func validateFleetGitHubPullRequestPlanBinding(plan model.Operation, operation model.Operation, reservation FleetGitHubPullRequestReservation) error {
	encodedPlan, err := json.Marshal(plan.Payload)
	if err != nil {
		return fmt.Errorf("encode fleet capacity plan: %w", err)
	}
	var typedPlan fleet.CapacityPlan
	if err := json.Unmarshal(encodedPlan, &typedPlan); err != nil || typedPlan.ID != plan.ID || typedPlan.Digest == "" || typedPlan.SourceDigest == "" || typedPlan.Pool == "" || typedPlan.Action == "" {
		return fmt.Errorf("fleet GitHub pull-request requires a complete immutable capacity plan payload")
	}
	expected := FleetGitHubPullRequestReservation{PlanID: typedPlan.ID, PlanDigest: typedPlan.Digest, SourceDigest: typedPlan.SourceDigest, Pool: typedPlan.Pool, Action: typedPlan.Action, Proposed: typedPlan.Proposed}
	if !sameFleetGitHubPullRequestReservation(expected, reservation) {
		return fmt.Errorf("Fleet GitHub pull-request reservation does not match the immutable capacity plan")
	}
	encodedOperation, err := json.Marshal(operation.Payload)
	if err != nil {
		return fmt.Errorf("encode Fleet GitHub pull-request payload: %w", err)
	}
	var payload struct {
		FleetGitHub struct {
			PlanID       string         `json:"planId"`
			PlanDigest   string         `json:"planDigest"`
			SourceDigest string         `json:"sourceDigest"`
			Pool         string         `json:"pool"`
			Action       string         `json:"action"`
			Proposed     fleet.NodePool `json:"proposed"`
		} `json:"fleetGitHub"`
	}
	if err := json.Unmarshal(encodedOperation, &payload); err != nil {
		return fmt.Errorf("signed Fleet GitHub pull-request payload does not match the immutable capacity plan")
	}
	intent := payload.FleetGitHub
	if !sameFleetGitHubPullRequestReservation(expected, FleetGitHubPullRequestReservation{PlanID: intent.PlanID, PlanDigest: intent.PlanDigest, SourceDigest: intent.SourceDigest, Pool: intent.Pool, Action: intent.Action, Proposed: intent.Proposed}) {
		return fmt.Errorf("signed Fleet GitHub pull-request payload does not match the immutable capacity plan")
	}
	return nil
}

func validateStoredFleetGitHubPullRequestReservation(value FleetGitHubPullRequestReservation) error {
	if _, err := uuid.Parse(value.PlanID); err != nil || value.PlanDigest == "" || value.SourceDigest == "" || value.Pool == "" || value.Action == "" || value.OperationID == "" || value.CreatedAt.IsZero() {
		return fmt.Errorf("incomplete reservation")
	}
	return nil
}

func sameFleetGitHubPullRequestReservation(left, right FleetGitHubPullRequestReservation) bool {
	return left.PlanID == right.PlanID && left.PlanDigest == right.PlanDigest && left.SourceDigest == right.SourceDigest && left.Pool == right.Pool && left.Action == right.Action && reflect.DeepEqual(left.Proposed, right.Proposed)
}

// VerifyFleetGitHubPullRequestReservation re-proves the signed intent before
// any recovered reservation is sent to GitHub.
func (s *V3OperationStore) VerifyFleetGitHubPullRequestReservation(ctx context.Context, reservation FleetGitHubPullRequestReservation) error {
	if err := validateStoredFleetGitHubPullRequestReservation(reservation); err != nil {
		return err
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(reservation.OperationID))
	if err != nil || len(index.Kvs) != 1 {
		return fmt.Errorf("load fleet GitHub pull-request acceptance: %w", err)
	}
	loaded, err := s.loadAcceptance(ctx, string(index.Kvs[0].Value))
	if err != nil {
		return err
	}
	accepted, err := s.replay(ctx, string(index.Kvs[0].Value), loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return err
	}
	if accepted.Operation.ID != reservation.OperationID || accepted.Operation.Kind != fleetGitHubPullRequestOperationKind || accepted.Operation.Ref != reservation.PlanID {
		return fmt.Errorf("fleet GitHub pull-request acceptance differs from reservation")
	}
	plan, _, err := s.load(ctx, reservation.PlanID)
	if err != nil {
		return err
	}
	return validateFleetGitHubPullRequestPlanBinding(plan.Operation, accepted.Operation, reservation)
}

// FinishFleetGitHubPullRequest stores a signed terminal result bound to the
// exact reservation and remote pull request. It is immutable or identical.
func (s *V3OperationStore) FinishFleetGitHubPullRequest(ctx context.Context, reservation FleetGitHubPullRequestReservation, result *githubapp.PullRequest) error {
	if result == nil || result.Number <= 0 || strings.TrimSpace(result.URL) == "" || strings.TrimSpace(result.Branch) != "norn/plan-"+reservation.PlanID {
		return fmt.Errorf("fleet GitHub pull-request result is invalid")
	}
	if err := s.VerifyFleetGitHubPullRequestReservation(ctx, reservation); err != nil {
		return err
	}
	op, rev, err := s.load(ctx, reservation.OperationID)
	if err != nil {
		return err
	}
	if op.Operation.Status == model.OperationSucceeded {
		return s.VerifyFleetGitHubPullRequestCompletion(ctx, reservation, result)
	}
	if op.Operation.Status != model.OperationQueued {
		return fmt.Errorf("fleet GitHub pull-request receipt is not queued")
	}
	remote := map[string]interface{}{"planId": reservation.PlanID, "pullRequestNumber": result.Number, "url": strings.TrimSpace(result.URL), "branch": strings.TrimSpace(result.Branch), "state": strings.TrimSpace(result.State), "merged": result.Merged, "headSha": strings.TrimSpace(result.HeadSHA)}
	canonical, err := json.Marshal(struct {
		Schema, OperationID, PlanID, Kind string
		Status                            model.OperationStatus
		Result                            map[string]interface{}
	}{"norn.fleet-github-completion/v1", op.Operation.ID, reservation.PlanID, fleetGitHubPullRequestOperationKind, model.OperationSucceeded, remote})
	if err != nil {
		return err
	}
	sig, err := s.signer.Sign(ctx, canonical)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	done := op.Operation
	done.Status, done.Message, done.FinishedAt, done.UpdatedAt = model.OperationSucceeded, "fleet pull request opened", &now, now
	payload := make(map[string]interface{}, len(done.Payload)+len(remote))
	for key, value := range done.Payload {
		payload[key] = value
	}
	done.Payload = payload
	for key, value := range remote {
		done.Payload[key] = value
	}
	if done.Metadata == nil {
		done.Metadata = map[string]interface{}{}
	}
	done.Metadata["fleetGitHubCompletion"] = map[string]interface{}{"schema": "norn.fleet-github-completion/v1", "canonicalBytes": base64.StdEncoding.EncodeToString(canonical), "signingAlgorithm": sig.Algorithm, "signingKeyId": sig.KeyID, "signature": sig.Value, "result": remote}
	record, err := json.Marshal(v3Record{Operation: done})
	if err != nil {
		return err
	}
	txn, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(s.opKey(reservation.OperationID)), "=", rev)).Then(clientv3.OpPut(s.opKey(reservation.OperationID), string(record))).Commit()
	if err != nil {
		return err
	}
	if txn.Succeeded {
		return nil
	}
	return s.VerifyFleetGitHubPullRequestCompletion(ctx, reservation, result)
}

func (s *V3OperationStore) VerifyFleetGitHubPullRequestCompletion(ctx context.Context, reservation FleetGitHubPullRequestReservation, expected *githubapp.PullRequest) error {
	if err := s.VerifyFleetGitHubPullRequestReservation(ctx, reservation); err != nil {
		return err
	}
	op, _, err := s.load(ctx, reservation.OperationID)
	if err != nil {
		return err
	}
	completion, ok := op.Operation.Metadata["fleetGitHubCompletion"].(map[string]interface{})
	if !ok || op.Operation.Status != model.OperationSucceeded {
		return fmt.Errorf("fleet GitHub pull-request completion is absent")
	}
	canonical, err := base64.StdEncoding.DecodeString(stringValue(completion["canonicalBytes"]))
	if err != nil {
		return err
	}
	if err := s.signer.Verify(ctx, store.AcceptanceSignature{Algorithm: stringValue(completion["signingAlgorithm"]), KeyID: stringValue(completion["signingKeyId"]), Value: stringValue(completion["signature"])}, canonical); err != nil {
		return err
	}
	var signed struct {
		Schema, OperationID, PlanID, Kind string
		Status                            model.OperationStatus
		Result                            struct {
			PullRequestNumber           int `json:"pullRequestNumber"`
			URL, Branch, State, HeadSHA string
			Merged                      bool
		}
	}
	if json.Unmarshal(canonical, &signed) != nil || signed.Schema != "norn.fleet-github-completion/v1" || signed.OperationID != reservation.OperationID || signed.PlanID != reservation.PlanID || signed.Kind != fleetGitHubPullRequestOperationKind || signed.Status != model.OperationSucceeded || expected == nil || signed.Result.PullRequestNumber != expected.Number || signed.Result.URL != strings.TrimSpace(expected.URL) || signed.Result.Branch != strings.TrimSpace(expected.Branch) || signed.Result.Merged != expected.Merged {
		return fmt.Errorf("fleet GitHub pull-request completion does not bind remote result")
	}
	if payloadNumber(op.Operation.Payload["pullRequestNumber"]) != signed.Result.PullRequestNumber || stringValue(op.Operation.Payload["planId"]) != signed.PlanID || stringValue(op.Operation.Payload["url"]) != signed.Result.URL || stringValue(op.Operation.Payload["branch"]) != signed.Result.Branch || stringValue(op.Operation.Payload["state"]) != signed.Result.State || boolValue(op.Operation.Payload["merged"]) != signed.Result.Merged || stringValue(op.Operation.Payload["headSha"]) != signed.Result.HeadSHA {
		return fmt.Errorf("fleet GitHub pull-request completion does not bind operation payload")
	}
	return nil
}

func payloadNumber(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		return 0
	}
}
func boolValue(value interface{}) bool { result, _ := value.(bool); return result }
