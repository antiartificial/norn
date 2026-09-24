package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/hub"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadRestartStage = "app.restart.nomad"

// restartNomad is deliberately small so the effect protocol can be qualified
// without pretending a unit test has exercised Nomad itself.
type restartNomad interface {
	RestartSnapshot(context.Context, string) ([]nomad.RestartAllocation, error)
	StopRestartAllocation(context.Context, nomad.RestartAllocation) error
	RestartStatus(context.Context, string, []nomad.RestartAllocation) (nomad.RestartStatus, error)
}

// NomadRestartEffects fences allocation replacement behind an immutable source
// snapshot. A recovery observes only that source set and never stops a newly
// created allocation.
type NomadRestartEffects struct {
	executor *effect.Executor
	store    *store.PGEffectStore
	client   restartNomad
	db       *store.DB
}

func NewNomadRestartEffects(db *store.DB, client *nomad.Client) (*NomadRestartEffects, error) {
	return newNomadRestartEffects(db, client)
}

func newNomadRestartEffects(db *store.DB, client restartNomad) (*NomadRestartEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad client is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	effects := &NomadRestartEffects{store: effectStore, client: client, db: db}
	effects.executor = &effect.Executor{Store: effectStore, Supervisor: &nomadRestartSupervisor{client: client, db: db}, Verifier: nomadRestartVerifier{}}
	return effects, nil
}

func (e *NomadRestartEffects) available() bool {
	return e != nil && e.store != nil && e.executor != nil && e.client != nil
}

func (p *Pipeline) RestartAvailable() bool {
	if p == nil {
		return false
	}
	if p.RestartAvailability != nil {
		return p.RestartAvailability()
	}
	return p.RestartEffects.available()
}

type restartRequest struct {
	App         string                    `json:"app"`
	Allocations []nomad.RestartAllocation `json:"allocations"`
}

func (p *Pipeline) executeRestart(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.RestartAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.restart execution is unavailable"}
	}
	if op == nil || strings.TrimSpace(op.App) == "" {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "restart operation app is required"}
	}
	if p.RestartEffects.db != nil {
		if err := p.RestartEffects.db.CheckOperationClaim(ctx, claim); err != nil {
			return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "restart claim is no longer current", Cause: err})
		}
	}
	authority, err := p.RestartEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "control authority is unavailable", Cause: err})
	}
	var result effect.ExecuteResult
	if prior, found, lookupErr := p.RestartEffects.store.LatestForOperation(ctx, op.ID, nomadRestartStage); lookupErr != nil {
		return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "restart effect lookup failed", Cause: lookupErr})
	} else if found {
		// The accepted source set is immutable. A successor claim only observes
		// this record; it never obtains a new allocation snapshot. Sources marked
		// attempted are ambiguous and are never retried; only durably unattempted
		// sources may be advanced before evidence reconciliation.
		request, requestErr := restartRequestFromReservation(prior.Reservation)
		if requestErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "stored restart descriptor is invalid", Cause: requestErr})
		}
		if _, continueErr := (&nomadRestartSupervisor{client: p.RestartEffects.client, db: p.RestartEffects.db}).continueUnattempted(ctx, prior.Reservation, request); continueErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "restart source continuation is unresolved", Cause: continueErr})
		}
		result, err = p.RestartEffects.executor.Recover(ctx, prior)
	} else {
		// This read is performed under the claimed operation. The resulting exact
		// allocation IDs and create indexes are persisted in the effect reservation
		// before Launch can issue the first stop request.
		sources, snapshotErr := p.RestartEffects.client.RestartSnapshot(ctx, op.App)
		if snapshotErr != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "snapshot restart allocations: " + snapshotErr.Error()}
		}
		if len(sources) == 0 {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "no active allocations found for app restart"}
		}
		request := restartRequest{App: op.App, Allocations: sources}
		payload, encodeErr := json.Marshal(request)
		if encodeErr != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "encode restart request: " + encodeErr.Error()}
		}
		reservation := effect.Reservation{Authority: authority, Resource: restartResource(op.App), OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: nomadRestartStage, Supervisor: "nomad-restart", LaunchPayload: payload}
		if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint restart request: " + err.Error()}
		}
		reservation.SupervisorExecutionID = restartExecutionID(reservation)
		result, err = p.RestartEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	}
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := p.RestartEffects.store.UnresolvedForResource(ctx, authority, restartResource(op.App))
		if lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: restartResource(op.App), Reason: "blocking restart lookup failed", Cause: lookupErr})
		}
		if found {
			_, err = p.RestartEffects.executor.Recover(ctx, blocking)
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "restart effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad restart failed"}
	}
	metadata := map[string]interface{}{"effectId": result.EffectID, "effectReused": result.Reused}
	message := fmt.Sprintf("%s active allocations restarted", op.App)
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, publish: func(context.Context) {
		if p.WS != nil {
			p.WS.Broadcast(hub.Event{Type: "app.restarted", AppID: op.App, Payload: metadata})
		}
	}}
}

func restartResource(app string) string { return "app/" + app + "/restart" }

func restartExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-restart-" + hex.EncodeToString(sum[:16])
}

type nomadRestartSupervisor struct {
	client restartNomad
	db     *store.DB
}

func (s *nomadRestartSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	request, err := restartRequestFromReservation(r)
	if err != nil || s.db == nil {
		return err
	}
	claim, err := store.NewOperationClaim(r.OperationClaim.OperationID, r.OperationClaim.OwnerID, r.OperationClaim.Generation)
	if err != nil {
		return err
	}
	return s.db.EnsureRestartEffectSources(context.Background(), claim, request.Allocations)
}

func (s *nomadRestartSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	request, err := restartRequestFromReservation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	return s.continueUnattempted(ctx, r, request)
}
func (s *nomadRestartSupervisor) continueUnattempted(ctx context.Context, r effect.Reservation, request restartRequest) (effect.ExecutionIdentity, error) {
	claim, err := store.NewOperationClaim(r.OperationClaim.OperationID, r.OperationClaim.OwnerID, r.OperationClaim.Generation)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	states := map[string]store.RestartEffectSource{}
	if s.db != nil {
		rows, loadErr := s.db.RestartEffectSources(ctx, claim.OperationID())
		if loadErr != nil {
			return effect.ExecutionIdentity{}, loadErr
		}
		for _, row := range rows {
			states[row.Allocation.ID] = row
		}
	}
	var firstErr error
	for _, source := range request.Allocations {
		if state, ok := states[source.ID]; ok && state.Attempted {
			continue
		}
		if s.db != nil {
			if claimErr := s.db.MarkRestartSourceAttempted(ctx, claim, source); claimErr != nil {
				return effect.ExecutionIdentity{}, claimErr
			}
		}
		if err := s.client.StopRestartAllocation(ctx, source); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("stop source allocation %s: %w", source.ID, err)
			}
			continue
		}
		if s.db != nil {
			if ackErr := s.db.MarkRestartSourceAcknowledged(ctx, claim, source); ackErr != nil {
				return effect.ExecutionIdentity{}, ackErr
			}
		}
	}
	if firstErr != nil {
		return effect.ExecutionIdentity{}, firstErr
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-restart:" + r.SupervisorExecutionID}, nil
}

func (s *nomadRestartSupervisor) Query(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	request, err := restartRequestFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	status, err := s.client.RestartStatus(ctx, request.App, request.Allocations)
	if err != nil {
		return effect.Observation{}, err
	}
	output, _ := json.Marshal(status)
	identity.Supervisor, identity.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if identity.RuntimeInstanceID == "" {
		identity.RuntimeInstanceID = "nomad-restart:" + r.SupervisorExecutionID
	}
	phase := effect.SupervisorUnknown
	if status.Replaced {
		phase = effect.SupervisorSucceeded
	}
	return effect.Observation{Identity: identity, Phase: phase, Output: output, Evidence: effect.RawEvidence{Source: "nomad.restart-status", Reference: request.App + "/" + r.SupervisorExecutionID, Payload: output}}, nil
}
func (s *nomadRestartSupervisor) Revoke(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, identity)
}
func (s *nomadRestartSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity, _ string) ([]byte, error) {
	observation, err := s.Query(ctx, r, identity)
	return observation.Output, err
}

func restartRequestFromReservation(r effect.Reservation) (restartRequest, error) {
	var request restartRequest
	if err := json.Unmarshal(r.LaunchPayload, &request); err != nil {
		return restartRequest{}, fmt.Errorf("decode restart descriptor: %w", err)
	}
	if strings.TrimSpace(request.App) == "" || len(request.Allocations) == 0 {
		return restartRequest{}, fmt.Errorf("restart descriptor is invalid")
	}
	seen := make(map[string]struct{}, len(request.Allocations))
	for _, source := range request.Allocations {
		if strings.TrimSpace(source.ID) == "" || source.CreateIndex == 0 || strings.TrimSpace(source.JobID) != request.App {
			return restartRequest{}, fmt.Errorf("restart descriptor source is invalid")
		}
		if _, duplicate := seen[source.ID]; duplicate {
			return restartRequest{}, fmt.Errorf("restart descriptor repeats source allocation")
		}
		seen[source.ID] = struct{}{}
	}
	return request, nil
}

type nomadRestartVerifier struct{}

func (nomadRestartVerifier) Verify(_ context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	if observation.Phase != effect.SupervisorSucceeded || observation.Identity.RuntimeInstanceID == "" {
		return effect.Verification{}, fmt.Errorf("Nomad restart has no verified replacement outcome")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest, ResultDigest: effect.DigestInput(observation.Output), ResultReference: observation.Evidence.Reference, SupervisorExecutionID: observation.Identity.SupervisorExecutionID, RuntimeInstanceID: observation.Identity.RuntimeInstanceID, EvidenceSource: observation.Evidence.Source, EvidenceReference: observation.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
