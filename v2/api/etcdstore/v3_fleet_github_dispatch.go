package etcdstore

// This aggregate keeps the one-time GitHub dispatch nonce private in etcd
// until a verified workflow run is bound. A lost response can therefore be
// reconciled with the exact nonce instead of issuing a second dispatch.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
)

var ErrFleetGitHubDispatchBound = errors.New("fleet GitHub dispatch is already bound")

// FleetGitHubDispatchPreparation is private server state. DispatchNonce must
// never be copied into an HTTP response, an operation payload, or a receipt.
type FleetGitHubDispatchPreparation struct {
	PlanID              string
	PlanRunID           int64
	PlanSHA256          string
	ApprovedHeadSHA     string
	FleetEnvironment    string
	AllowDestructive    bool
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

// PrepareFleetGitHubDispatch atomically creates or returns the one private
// nonce for one immutable successful plan and one approved GitHub plan run.
// It performs no remote call.
func (s *V3OperationStore) PrepareFleetGitHubDispatch(ctx context.Context, input FleetGitHubDispatchPreparation) (FleetGitHubDispatchPreparation, bool, error) {
	input = normalizedFleetGitHubDispatchPreparation(input)
	if !validFleetGitHubDispatchPreparation(input) {
		return FleetGitHubDispatchPreparation{}, false, fmt.Errorf("fleet GitHub dispatch preparation is invalid")
	}
	plan, revision, err := s.load(ctx, input.PlanID)
	if err != nil {
		return FleetGitHubDispatchPreparation{}, false, fmt.Errorf("load fleet plan: %w", err)
	}
	if plan.Operation.Kind != "fleet.capacity-plan" || plan.Operation.Status != model.OperationSucceeded {
		return FleetGitHubDispatchPreparation{}, false, fmt.Errorf("fleet GitHub dispatch requires a successful immutable fleet plan")
	}
	if _, err := s.loadFleetRunnerDispatch(ctx, input.PlanID); err == nil {
		return FleetGitHubDispatchPreparation{}, false, ErrFleetGitHubDispatchBound
	} else if !errors.Is(err, ErrNotFound) {
		return FleetGitHubDispatchPreparation{}, false, err
	}
	nonce, err := fleetGitHubDispatchNonce()
	if err != nil {
		return FleetGitHubDispatchPreparation{}, false, err
	}
	input.DispatchNonce = nonce
	digest := sha256.Sum256([]byte(nonce))
	input.DispatchNonceSHA256 = hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(v3FleetGitHubDispatchPreparation{FleetGitHubDispatchPreparation: input, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)})
	if err != nil {
		return FleetGitHubDispatchPreparation{}, false, err
	}
	key := s.fleetGitHubDispatchPreparationKey(input.PlanID)
	txn, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(s.opKey(input.PlanID)), "=", revision),
		clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerDispatchKey(input.PlanID)), "=", 0),
	).Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		return FleetGitHubDispatchPreparation{}, false, err
	}
	if txn.Succeeded {
		return input, true, nil
	}
	existing, err := s.GetFleetGitHubDispatchPreparation(ctx, input.PlanID)
	if err == nil {
		if sameFleetGitHubDispatchPreparation(existing, input) {
			return existing, false, nil
		}
		return FleetGitHubDispatchPreparation{}, false, fmt.Errorf("fleet GitHub dispatch preparation already exists with different approved plan")
	}
	if errors.Is(err, ErrNotFound) {
		if _, bindErr := s.loadFleetRunnerDispatch(ctx, input.PlanID); bindErr == nil {
			return FleetGitHubDispatchPreparation{}, false, ErrFleetGitHubDispatchBound
		} else if !errors.Is(bindErr, ErrNotFound) {
			return FleetGitHubDispatchPreparation{}, false, bindErr
		}
	}
	return FleetGitHubDispatchPreparation{}, false, err
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
	if record.PlanID != strings.TrimSpace(planID) || !validFleetGitHubDispatchPreparation(record.FleetGitHubDispatchPreparation) {
		return FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch preparation is corrupt")
	}
	digest := sha256.Sum256([]byte(record.DispatchNonce))
	if !strings.EqualFold(record.DispatchNonceSHA256, hex.EncodeToString(digest[:])) {
		return FleetGitHubDispatchPreparation{}, fmt.Errorf("fleet GitHub dispatch preparation nonce is corrupt")
	}
	return record.FleetGitHubDispatchPreparation, nil
}

// FinishFleetGitHubDispatch binds only a verified GitHub run to its existing
// private preparation. BindFleetRunnerDispatch makes that public-to-runners
// fact immutable; the preparation remains private recovery evidence.
func (s *V3OperationStore) FinishFleetGitHubDispatch(ctx context.Context, planID, nonceSHA256 string, runID int64, workflowURL string) error {
	prepared, err := s.GetFleetGitHubDispatchPreparation(ctx, planID)
	if err != nil {
		return err
	}
	if nonceSHA256 != prepared.DispatchNonceSHA256 {
		return fmt.Errorf("fleet GitHub dispatch nonce does not match preparation")
	}
	return s.BindFleetRunnerDispatch(ctx, FleetRunnerDispatchBinding{PlanID: prepared.PlanID, PlanSHA256: prepared.PlanSHA256, ApprovedHeadSHA: prepared.ApprovedHeadSHA, DispatchNonceSHA256: prepared.DispatchNonceSHA256, RunID: runID, WorkflowURL: workflowURL})
}

func (s *V3OperationStore) GetFleetRunnerDispatchBinding(ctx context.Context, planID string) (FleetRunnerDispatchBinding, error) {
	record, err := s.loadFleetRunnerDispatch(ctx, strings.TrimSpace(planID))
	if err != nil {
		return FleetRunnerDispatchBinding{}, err
	}
	return record.FleetRunnerDispatchBinding, nil
}

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
		(input.DispatchNonceSHA256 == "" || fleetLowerHex(input.DispatchNonceSHA256, 64))
}

func sameFleetGitHubDispatchPreparation(left, right FleetGitHubDispatchPreparation) bool {
	return left.PlanID == right.PlanID && left.PlanRunID == right.PlanRunID && left.PlanSHA256 == right.PlanSHA256 &&
		left.ApprovedHeadSHA == right.ApprovedHeadSHA && left.FleetEnvironment == right.FleetEnvironment &&
		left.AllowDestructive == right.AllowDestructive
}

func fleetGitHubDispatchNonce() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate fleet GitHub dispatch nonce: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
