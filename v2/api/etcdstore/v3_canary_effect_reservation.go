package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// V3CanaryEffectReservations establishes the etcd side of the canary effect
// boundary. It is deliberately not an effect.Store yet: launch acknowledgement,
// terminal evidence, and lease-expiry recovery must be implemented before the
// etcd runtime may expose the promotion endpoint.
type V3CanaryEffectReservations struct{ operations *V3OperationStore }

func NewV3CanaryEffectReservations(operations *V3OperationStore) (*V3CanaryEffectReservations, error) {
	if operations == nil || operations.kv == nil || operations.lease == nil || operations.authority == "" {
		return nil, fmt.Errorf("etcd canary effect reservations require a v3 operation store")
	}
	return &V3CanaryEffectReservations{operations: operations}, nil
}

func (s *V3CanaryEffectReservations) Authority(ctx context.Context) (string, error) {
	return s.operations.Authority(ctx)
}

func (s *V3CanaryEffectReservations) effectKey(r effect.Reservation) string {
	material := r.OperationClaim.OperationID + "\x00" + r.Stage + "\x00" + r.InputDigest
	sum := sha256.Sum256([]byte(material))
	return s.operations.prefix + "/v3/effects/canary/" + hex.EncodeToString(sum[:])
}

func (s *V3CanaryEffectReservations) gateKey(app string) string {
	sum := sha256.Sum256([]byte(app))
	// Every future etcd app effect must use this gate, irrespective of stage.
	return s.operations.prefix + "/v3/effects/app-gates/" + hex.EncodeToString(sum[:])
}

type canaryEffectInput struct {
	App          string `json:"app"`
	Region       string `json:"region"`
	NomadRegion  string `json:"nomadRegion"`
	DeploymentID string `json:"deploymentId"`
}

func validateCanaryEffectReservation(r effect.Reservation, op model.Operation, authority string) (string, error) {
	if r.Authority != authority || r.OperationClaim.OperationID != op.ID || r.OperationClaim.OwnerID == "" || r.OperationClaim.Generation <= 0 ||
		r.Stage != "app.canary-promote.nomad" || r.Supervisor != "nomad-canary-promotion" || r.SupervisorExecutionID == "" || op.Kind != "app.canary-promote" {
		return "", fmt.Errorf("canary effect reservation does not match the accepted operation and authority")
	}
	var input canaryEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		return "", fmt.Errorf("decode canary effect input: %w", err)
	}
	if input.App == "" || input.Region == "" || input.NomadRegion == "" || input.DeploymentID == "" ||
		input.App != op.App || r.Resource != "app/"+input.App+"/canary-promote/"+input.Region ||
		input.Region != effectPayloadString(op.Payload, "region") || input.NomadRegion != effectPayloadString(op.Payload, "nomadRegion") || input.DeploymentID != effectPayloadString(op.Payload, "deploymentId") {
		return "", fmt.Errorf("canary effect input does not match accepted operation payload")
	}
	if strings.Contains(input.App, "/") || strings.Contains(input.Region, "/") {
		return "", fmt.Errorf("canary effect resource contains an invalid path component")
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || r.InputDigest != digest {
		return "", fmt.Errorf("canary effect input digest does not match its reservation")
	}
	return input.App, nil
}

func effectPayloadString(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}

// Reserve validates the live claim and acquires the app effect gate in one
// etcd transaction. Neither an expired owner lease nor a changed operation
// generation can create a reservation, even if a stale caller read both first.
func (s *V3CanaryEffectReservations) Reserve(ctx context.Context, r effect.Reservation) (effect.ReservationResult, error) {
	if s == nil || s.operations == nil {
		return effect.ReservationResult{}, fmt.Errorf("etcd canary effect reservations are unavailable")
	}
	op, revision, err := s.operations.load(ctx, r.OperationClaim.OperationID)
	if err != nil {
		return effect.ReservationResult{}, store.ErrOperationOwnershipLost
	}
	app, err := validateCanaryEffectReservation(r, op.Operation, s.operations.authority)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	ownerKey := s.operations.ownerKey(op.Operation.ID)
	owner, err := s.operations.kv.Get(ctx, ownerKey)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	if len(owner.Kvs) != 1 || owner.Kvs[0].Lease == 0 || op.Operation.Status != model.OperationRunning ||
		op.Operation.LockedBy != r.OperationClaim.OwnerID || op.Generation != r.OperationClaim.Generation ||
		string(owner.Kvs[0].Value) != claimOwnerValue(r.OperationClaim.OwnerID, r.OperationClaim.Generation) {
		return effect.ReservationResult{}, store.ErrOperationOwnershipLost
	}
	effectKey, gateKey := s.effectKey(r), s.gateKey(app)
	prior, err := s.operations.kv.Get(ctx, effectKey)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	if len(prior.Kvs) != 0 {
		var record effect.Record
		if err := json.Unmarshal(prior.Kvs[0].Value, &record); err != nil {
			return effect.ReservationResult{}, fmt.Errorf("decode existing canary effect: %w", err)
		}
		if record.Reservation.Authority != r.Authority || record.Reservation.OperationClaim.OperationID != r.OperationClaim.OperationID ||
			record.Reservation.Stage != r.Stage || record.Reservation.InputDigest != r.InputDigest || record.Reservation.Resource != r.Resource ||
			record.Reservation.SupervisorExecutionID == "" || record.Token.EffectID == "" {
			return effect.ReservationResult{}, fmt.Errorf("existing canary effect identity does not match reservation")
		}
		gate, err := s.operations.kv.Get(ctx, gateKey)
		if err != nil {
			return effect.ReservationResult{}, err
		}
		if record.Lifecycle == effect.LifecycleReserved || record.Lifecycle == effect.LifecycleLaunched {
			if len(gate.Kvs) != 1 || string(gate.Kvs[0].Value) != effectKey {
				return effect.ReservationResult{}, fmt.Errorf("existing canary effect lost its app gate")
			}
		} else if len(gate.Kvs) != 0 && string(gate.Kvs[0].Value) == effectKey {
			return effect.ReservationResult{}, fmt.Errorf("terminal canary effect still owns the app gate")
		}
		return effect.ReservationResult{Record: record}, nil
	}
	record := effect.Record{Token: effect.Token{EffectID: uuid.NewString(), Generation: r.OperationClaim.Generation}, Reservation: r, Lifecycle: effect.LifecycleReserved}
	encoded, err := json.Marshal(record)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	txn, err := s.operations.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.operations.opKey(op.Operation.ID)), "=", revision),
		clientv3.Compare(clientv3.ModRevision(ownerKey), "=", owner.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.Value(ownerKey), "=", claimOwnerValue(r.OperationClaim.OwnerID, r.OperationClaim.Generation)),
		clientv3.Compare(clientv3.CreateRevision(effectKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(gateKey), "=", 0),
	).Then(clientv3.OpPut(effectKey, string(encoded)), clientv3.OpPut(gateKey, effectKey)).Commit()
	if err != nil {
		return effect.ReservationResult{}, err
	}
	if txn.Succeeded {
		return effect.ReservationResult{Record: record, Created: true}, nil
	}
	// Distinguish a raced same-input reservation from a different unresolved
	// effect. The caller will retry to validate a new claim; this attempt never
	// launches after losing its compare transaction.
	gate, err := s.operations.kv.Get(ctx, gateKey)
	if err != nil {
		return effect.ReservationResult{}, err
	}
	if len(gate.Kvs) != 0 && string(gate.Kvs[0].Value) != effectKey {
		return effect.ReservationResult{}, &effect.ResourceBlockedError{Resource: r.Resource}
	}
	return effect.ReservationResult{}, store.ErrOperationOwnershipLost
}
