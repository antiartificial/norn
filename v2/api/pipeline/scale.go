package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadScaleStage = "app.scale.nomad"

// NomadScaleEffects is the explicitly configured fenced boundary for scale
// mutations. A missing instance is an admission failure, never an accepted
// operation that is guaranteed to fail later.
type NomadScaleEffects struct {
	executor *effect.Executor
	store    *store.PGEffectStore
}

func NewNomadScaleEffects(db *store.DB, client *nomad.Client) (*NomadScaleEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad client is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	supervisor := &nomadScaleSupervisor{client: client}
	return &NomadScaleEffects{store: effectStore, executor: &effect.Executor{
		Store: effectStore, Supervisor: supervisor, Verifier: nomadScaleVerifier{},
	}}, nil
}

func (e *NomadScaleEffects) available() bool {
	return e != nil && e.store != nil && e.executor != nil
}

type scaleRequest struct {
	App   string `json:"app"`
	Group string `json:"group"`
	Count int    `json:"count"`
}

func (p *Pipeline) ScaleAvailable() bool { return p != nil && p.ScaleEffects.available() }

func (p *Pipeline) executeScale(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.ScaleAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.scale execution is unavailable"}
	}
	request, err := scaleRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	authority, err := p.ScaleEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: scaleResource(request), Reason: "control authority is unavailable", Cause: err})
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "encode scale request: " + err.Error()}
	}
	reservation := effect.Reservation{
		Authority: authority, Resource: scaleResource(request),
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          nomadScaleStage, Supervisor: "nomad-scale", LaunchPayload: payload,
	}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint scale request: " + err.Error()}
	}
	reservation.SupervisorExecutionID = scaleExecutionID(reservation)
	result, err := p.ScaleEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := p.ScaleEffects.store.UnresolvedForResource(ctx, authority, reservation.Resource)
		if lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking scale lookup failed", Cause: lookupErr})
		}
		if found {
			_, err = p.ScaleEffects.executor.Recover(ctx, blocking)
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "scale effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad scale request failed"}
	}
	metadata := map[string]interface{}{"group": request.Group, "count": request.Count, "effectId": result.EffectID, "effectReused": result.Reused}
	message := fmt.Sprintf("%s process %q scaled to %d", request.App, request.Group, request.Count)
	if err := p.finishScaleIntent(ctx, claim, request.App, request.Group, request.Count, message, metadata); err != nil {
		// Nomad's completed effect remains durably reusable. Do not terminalize
		// a post-effect database outage: defer so a successor claim retries only
		// the atomic intent/receipt write, never the Nomad launch.
		return deferredResult(claim, &effect.PendingError{EffectID: result.EffectID, Resource: scaleResource(request), Reason: "persist desired scale intent", Cause: err})
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, finished: true, publish: func(context.Context) {
		if p.WS != nil {
			p.WS.Broadcast(hub.Event{Type: "app.scaled", AppID: request.App, Payload: metadata})
		}
	}}
}

func (p *Pipeline) finishScaleIntent(ctx context.Context, claim store.OperationClaim, app, group string, count int, message string, metadata map[string]interface{}) error {
	if p != nil && p.FinishScaleIntent != nil {
		return p.FinishScaleIntent(ctx, claim, app, group, count, message, metadata)
	}
	if p == nil || p.DB == nil {
		return fmt.Errorf("desired replica store is unavailable")
	}
	return p.DB.FinishScaleClaimedOperation(ctx, claim, app, group, count, message, metadata)
}

func scaleRequestFromOperation(op *model.Operation) (scaleRequest, error) {
	if op == nil || strings.TrimSpace(op.App) == "" {
		return scaleRequest{}, fmt.Errorf("scale operation app is required")
	}
	request := scaleRequest{App: op.App, Group: stringFromMap(op.Payload, "group")}
	value, ok := op.Payload["count"]
	if !ok {
		return scaleRequest{}, fmt.Errorf("scale operation count is required")
	}
	switch count := value.(type) {
	case int:
		request.Count = count
	case float64:
		if count != float64(int(count)) {
			return scaleRequest{}, fmt.Errorf("scale operation count must be an integer")
		}
		request.Count = int(count)
	default:
		return scaleRequest{}, fmt.Errorf("scale operation count is invalid")
	}
	if strings.TrimSpace(request.Group) == "" || request.Count < 0 {
		return scaleRequest{}, fmt.Errorf("scale operation group and non-negative count are required")
	}
	return request, nil
}

func scaleResource(request scaleRequest) string {
	return "app/" + request.App + "/scale/" + request.Group
}
func scaleExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-scale-" + hex.EncodeToString(sum[:16])
}

type nomadScaleSupervisor struct{ client *nomad.Client }

func (s *nomadScaleSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, err := scaleRequestFromReservation(r)
	return err
}
func (s *nomadScaleSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	if err := ctx.Err(); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	request, err := scaleRequestFromReservation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	evalID, err := s.client.ScaleJobWithMeta(request.App, request.Group, request.Count, map[string]interface{}{"norn.operationId": r.OperationClaim.OperationID, "norn.claimGeneration": strconv.FormatInt(r.OperationClaim.Generation, 10), "norn.executionId": r.SupervisorExecutionID})
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if evalID == "" {
		evalID = r.SupervisorExecutionID
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-eval:" + evalID}, nil
}
func (s *nomadScaleSupervisor) Query(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	if err := ctx.Err(); err != nil {
		return effect.Observation{}, err
	}
	request, err := scaleRequestFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	expectedEvalID := strings.TrimPrefix(identity.RuntimeInstanceID, "nomad-eval:")
	desired, matched, evalID, err := s.client.ScaleStatus(request.App, request.Group, r.OperationClaim.OperationID, strconv.FormatInt(r.OperationClaim.Generation, 10), r.SupervisorExecutionID, request.Count, expectedEvalID)
	if err != nil {
		return effect.Observation{}, err
	}
	output, _ := json.Marshal(map[string]interface{}{"app": request.App, "group": request.Group, "count": request.Count, "desired": desired, "operationId": r.OperationClaim.OperationID, "evalId": evalID, "matched": matched})
	identity.Supervisor, identity.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if matched && identity.RuntimeInstanceID == "" {
		identity.RuntimeInstanceID = "nomad-eval:" + evalID
	}
	phase := effect.SupervisorUnknown
	if desired == request.Count && matched {
		phase = effect.SupervisorSucceeded
	}
	return effect.Observation{Identity: identity, Phase: phase, Output: output, Evidence: effect.RawEvidence{Source: "nomad.scale-status", Reference: request.App + "/" + request.Group + "/" + evalID, Payload: output}}, nil
}
func (s *nomadScaleSupervisor) Revoke(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, identity)
}
func (s *nomadScaleSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity, _ string) ([]byte, error) {
	observation, err := s.Query(ctx, r, identity)
	return observation.Output, err
}
func scaleRequestFromReservation(r effect.Reservation) (scaleRequest, error) {
	var request scaleRequest
	if err := json.Unmarshal(r.LaunchPayload, &request); err != nil {
		return scaleRequest{}, fmt.Errorf("decode scale descriptor: %w", err)
	}
	if request.App == "" || request.Group == "" || request.Count < 0 {
		return scaleRequest{}, fmt.Errorf("scale descriptor is invalid")
	}
	return request, nil
}

type nomadScaleVerifier struct{}

func (nomadScaleVerifier) Verify(_ context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	if observation.Phase != effect.SupervisorSucceeded {
		return effect.Verification{}, fmt.Errorf("Nomad scale has no verified terminal success")
	}
	if observation.Identity.SupervisorExecutionID != record.Reservation.SupervisorExecutionID || observation.Identity.RuntimeInstanceID == "" {
		return effect.Verification{}, fmt.Errorf("Nomad scale observation identity does not match reservation")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest, ResultDigest: effect.DigestInput(observation.Output), ResultReference: observation.Evidence.Reference, SupervisorExecutionID: observation.Identity.SupervisorExecutionID, RuntimeInstanceID: observation.Identity.RuntimeInstanceID, EvidenceSource: observation.Evidence.Source, EvidenceReference: observation.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
