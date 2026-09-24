package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadCanaryPromotionStage = "app.canary-promote.nomad"

// NomadCanaryPromotionEffects is the explicitly configured durable boundary
// for Nomad's non-idempotent deployment-promotion endpoint.
type NomadCanaryPromotionEffects struct {
	executor *effect.Executor
	store    CanaryPromotionEffectStore
}

// CanaryPromotionEffectStore is the single-authority boundary for a promotion.
// Reserve must validate the operation claim and acquire the app gate in the
// same durable transaction. An etcd implementation must satisfy this contract
// before the etcd runtime can admit canary promotions.
type CanaryPromotionEffectStore interface {
	effect.Store
	effect.RecoveryStore
	Authority(context.Context) (string, error)
}

var _ CanaryPromotionEffectStore = (*store.PGEffectStore)(nil)

func NewNomadCanaryPromotionEffects(db *store.DB, client *nomad.Client) (*NomadCanaryPromotionEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad client is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	return NewNomadCanaryPromotionEffectsWithStore(effectStore, client)
}

// NewNomadCanaryPromotionEffectsWithStore permits a control backend to supply
// its own atomic claim/effect store. A missing store is rejected rather than
// allowing an unfenced Nomad write.
func NewNomadCanaryPromotionEffectsWithStore(effectStore CanaryPromotionEffectStore, client *nomad.Client) (*NomadCanaryPromotionEffects, error) {
	if effectStore == nil || client == nil {
		return nil, fmt.Errorf("durable canary promotion requires an effect store and Nomad client")
	}
	value := reflect.ValueOf(effectStore)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil, fmt.Errorf("durable canary promotion requires an effect store and Nomad client")
	}
	return &NomadCanaryPromotionEffects{store: effectStore, executor: &effect.Executor{
		Store: effectStore, Supervisor: &nomadCanaryPromotionSupervisor{client: client}, Verifier: nomadCanaryPromotionVerifier{},
	}}, nil
}

func (e *NomadCanaryPromotionEffects) available() bool {
	return e != nil && e.store != nil && e.executor != nil
}

// CanaryPromotionAvailable is an admission gate. A request is never accepted
// if the durable execution boundary is absent.
func (p *Pipeline) CanaryPromotionAvailable() bool {
	return p != nil && p.CanaryPromotionEffects.available()
}

type canaryPromotionRequest struct {
	App          string `json:"app"`
	Region       string `json:"region"`
	NomadRegion  string `json:"nomadRegion"`
	DeploymentID string `json:"deploymentId"`
}

func (p *Pipeline) executeCanaryPromotion(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CanaryPromotionAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.canary-promote execution is unavailable"}
	}
	request, err := canaryPromotionRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	authority, err := p.CanaryPromotionEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: canaryPromotionResource(request), Reason: "control authority is unavailable", Cause: err})
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "encode canary promotion request: " + err.Error()}
	}
	reservation := effect.Reservation{
		Authority: authority, Resource: canaryPromotionResource(request),
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          nomadCanaryPromotionStage, Supervisor: "nomad-canary-promotion", LaunchPayload: payload,
	}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint canary promotion request: " + err.Error()}
	}
	reservation.SupervisorExecutionID = canaryPromotionExecutionID(reservation)
	result, err := p.CanaryPromotionEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := p.CanaryPromotionEffects.store.UnresolvedForResource(ctx, authority, reservation.Resource)
		if lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking canary promotion lookup failed", Cause: lookupErr})
		}
		if found {
			_, err = p.CanaryPromotionEffects.executor.Recover(ctx, blocking)
			if err == nil {
				// A resolved predecessor releases the region gate for a successor
				// claim. This claim cannot safely assume it owns that next launch.
				return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking canary promotion resolved; defer successor for fresh launch"})
			}
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "canary promotion effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad canary promotion failed"}
	}
	metadata := map[string]interface{}{"region": request.Region, "nomadRegion": request.NomadRegion, "deploymentId": request.DeploymentID, "effectId": result.EffectID, "effectReused": result.Reused}
	message := fmt.Sprintf("canary deployment %s promoted for %s in %s", request.DeploymentID, request.App, request.Region)
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, publish: func(context.Context) {
		if p.WS != nil {
			p.WS.Broadcast(hub.Event{Type: "canary.promoted", AppID: request.App, Payload: metadata})
		}
	}}
}

func canaryPromotionRequestFromOperation(op *model.Operation) (canaryPromotionRequest, error) {
	if op == nil || strings.TrimSpace(op.App) == "" {
		return canaryPromotionRequest{}, fmt.Errorf("canary promotion operation app is required")
	}
	request := canaryPromotionRequest{App: op.App, Region: stringFromMap(op.Payload, "region"), NomadRegion: stringFromMap(op.Payload, "nomadRegion"), DeploymentID: stringFromMap(op.Payload, "deploymentId")}
	if strings.TrimSpace(request.Region) == "" || strings.TrimSpace(request.NomadRegion) == "" || strings.TrimSpace(request.DeploymentID) == "" {
		return canaryPromotionRequest{}, fmt.Errorf("canary promotion operation region, Nomad region, and deployment id are required")
	}
	return request, nil
}

func canaryPromotionResource(request canaryPromotionRequest) string {
	// The gate serializes every promotion for this app's logical region. The
	// immutable deployment ID remains in the signed payload and input digest,
	// so recovery can reconcile only its accepted remote identity.
	return "app/" + request.App + "/canary-promote/" + request.Region
}

func canaryPromotionExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-canary-promotion-" + hex.EncodeToString(sum[:16])
}

type nomadCanaryPromotionSupervisor struct{ client *nomad.Client }

func (s *nomadCanaryPromotionSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, err := canaryPromotionRequestFromReservation(r)
	return err
}

func (s *nomadCanaryPromotionSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	if err := ctx.Err(); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	request, err := canaryPromotionRequestFromReservation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if err := s.client.PromoteDeploymentIDRegion(request.DeploymentID, request.NomadRegion); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-deployment:" + request.DeploymentID}, nil
}

func (s *nomadCanaryPromotionSupervisor) Query(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	if err := ctx.Err(); err != nil {
		return effect.Observation{}, err
	}
	request, err := canaryPromotionRequestFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	info, err := s.client.DeploymentByIDRegion(request.DeploymentID, request.NomadRegion)
	if err != nil {
		return effect.Observation{}, err
	}
	if info == nil || info.ID != request.DeploymentID || info.JobID != request.App {
		return effect.Observation{}, fmt.Errorf("Nomad deployment identity no longer matches accepted promotion")
	}
	output, _ := json.Marshal(map[string]interface{}{"app": request.App, "region": request.Region, "nomadRegion": request.NomadRegion, "deploymentId": info.ID, "status": info.Status, "statusDescription": info.StatusDesc, "canaryPromoted": info.CanaryPromoted})
	identity.Supervisor, identity.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if identity.RuntimeInstanceID == "" {
		identity.RuntimeInstanceID = "nomad-deployment:" + request.DeploymentID
	}
	phase := effect.SupervisorUnknown
	switch strings.ToLower(info.Status) {
	case "successful":
		if info.CanaryPromoted {
			phase = effect.SupervisorSucceeded
		}
	case "failed", "cancelled":
		phase = effect.SupervisorFailed
	}
	return effect.Observation{Identity: identity, Phase: phase, Output: output, Evidence: effect.RawEvidence{Source: "nomad.deployment", Reference: request.DeploymentID, Payload: output}}, nil
}

func (s *nomadCanaryPromotionSupervisor) Revoke(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, identity)
}

func (s *nomadCanaryPromotionSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity, _ string) ([]byte, error) {
	observation, err := s.Query(ctx, r, identity)
	return observation.Output, err
}

func canaryPromotionRequestFromReservation(r effect.Reservation) (canaryPromotionRequest, error) {
	var request canaryPromotionRequest
	if err := json.Unmarshal(r.LaunchPayload, &request); err != nil {
		return canaryPromotionRequest{}, fmt.Errorf("decode canary promotion descriptor: %w", err)
	}
	if strings.TrimSpace(request.App) == "" || strings.TrimSpace(request.Region) == "" || strings.TrimSpace(request.NomadRegion) == "" || strings.TrimSpace(request.DeploymentID) == "" {
		return canaryPromotionRequest{}, fmt.Errorf("canary promotion descriptor is invalid")
	}
	return request, nil
}

type nomadCanaryPromotionVerifier struct{}

func (nomadCanaryPromotionVerifier) Verify(_ context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	if observation.Identity.SupervisorExecutionID != record.Reservation.SupervisorExecutionID || observation.Identity.RuntimeInstanceID == "" {
		return effect.Verification{}, fmt.Errorf("Nomad canary promotion observation identity does not match reservation")
	}
	decision := effect.VerificationSucceeded
	if observation.Phase == effect.SupervisorFailed {
		decision = effect.VerificationFailed
	} else if observation.Phase != effect.SupervisorSucceeded {
		return effect.Verification{}, fmt.Errorf("Nomad canary promotion has no verified terminal outcome")
	}
	if decision == effect.VerificationSucceeded {
		var evidence struct {
			CanaryPromoted bool `json:"canaryPromoted"`
		}
		if err := json.Unmarshal(observation.Output, &evidence); err != nil || !evidence.CanaryPromoted {
			return effect.Verification{}, fmt.Errorf("Nomad canary promotion success is missing promoted task-group evidence")
		}
	}
	return effect.Verification{Decision: decision, InputDigest: record.Reservation.InputDigest, ResultDigest: effect.DigestInput(observation.Output), ResultReference: observation.Evidence.Reference, SupervisorExecutionID: observation.Identity.SupervisorExecutionID, RuntimeInstanceID: observation.Identity.RuntimeInstanceID, EvidenceSource: observation.Evidence.Source, EvidenceReference: observation.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
