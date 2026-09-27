package etcdstore

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// FleetIngressInventoryEvidence is an attempt-bound source for route readback.
// The deployment worker must still bind the Fleet plan to its accepted app
// intent and recheck this evidence before terminal traffic completion.
type FleetIngressInventoryEvidence struct {
	PlanID                string
	Cluster               string
	Environment           string
	ActivePointerRevision int64
	AttemptID             string
	AttemptRevision       int64
	PlanStateModRevision  int64
	CheckpointID          string
	CheckpointModRevision int64
	StateSerial           int64
	Digest                string
	Nodes                 []ingress.IngressNode
}

func (s *V3OperationStore) fleetIngressInventoryCompares(evidence FleetIngressInventoryEvidence) ([]clientv3.Cmp, error) {
	if evidence.PlanID == "" || evidence.AttemptID == "" || evidence.CheckpointID == "" || evidence.PlanStateModRevision <= 0 || evidence.CheckpointModRevision <= 0 || evidence.StateSerial <= 0 || evidence.Digest == "" || len(evidence.Nodes) < 2 {
		return nil, fmt.Errorf("Fleet ingress inventory evidence is incomplete")
	}
	compares := []clientv3.Cmp{
		clientv3.Compare(clientv3.ModRevision(s.fleetRunnerPlanStateKey(evidence.PlanID)), "=", evidence.PlanStateModRevision),
		clientv3.Compare(clientv3.ModRevision(s.fleetReconciliationKey(evidence.PlanID, evidence.CheckpointID)), "=", evidence.CheckpointModRevision),
	}
	if evidence.ActivePointerRevision > 0 {
		if evidence.Cluster == "" || evidence.Environment == "" {
			return nil, fmt.Errorf("active Fleet ingress inventory identity is incomplete")
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.activeFleetIngressKey(evidence.Cluster, evidence.Environment)), "=", evidence.ActivePointerRevision))
	}
	return compares, nil
}

func (s *V3OperationStore) CurrentFleetIngressInventory(ctx context.Context, planID, cluster, environment string, port int) (*FleetIngressInventoryEvidence, error) {
	before, err := s.kv.Get(ctx, s.fleetRunnerPlanStateKey(planID))
	if err != nil {
		return nil, err
	}
	if len(before.Kvs) != 1 {
		return nil, fmt.Errorf("Fleet plan state is unavailable")
	}
	attempts, err := s.ListFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return nil, err
	}
	history, revisions, err := s.listFleetReconciliations(ctx, planID)
	if err != nil {
		return nil, err
	}
	after, err := s.kv.Get(ctx, s.fleetRunnerPlanStateKey(planID))
	if err != nil || len(after.Kvs) != 1 || after.Kvs[0].ModRevision != before.Kvs[0].ModRevision {
		return nil, fmt.Errorf("Fleet plan state changed during ingress inventory readback")
	}
	proof, err := resolveFleetIngressInventory(planID, cluster, environment, port, attempts, history, revisions)
	if err != nil {
		return nil, err
	}
	proof.PlanStateModRevision = before.Kvs[0].ModRevision
	proof.Cluster, proof.Environment = cluster, environment
	return proof, nil
}

func resolveFleetIngressInventory(planID, cluster, environment string, port int, attempts []fleet.RunnerAttempt, history []model.Operation, revisions map[string]int64) (*FleetIngressInventoryEvidence, error) {
	if len(attempts) == 0 || attempts[0].PlanID != planID || attempts[0].Status != "succeeded" || attempts[0].CurrentPhase != "complete" {
		return nil, fmt.Errorf("latest Fleet attempt has not completed successfully")
	}
	latest := attempts[0]
	var configured *fleet.ReconciliationRequest
	checkpointID := ""
	terminalEvidence := false
	var stateSerial int64
	var terminalSerial int64
	for _, operation := range history {
		request, err := store.FleetReconciliationRequestFromOperation(operation)
		if err != nil {
			return nil, err
		}
		if request.AttemptID != latest.ID {
			continue
		}
		if request.CommitSHA != latest.CommitSHA || request.PlanSHA256 != latest.PlanSHA256 {
			return nil, fmt.Errorf("Fleet reconciliation differs from latest attempt identity")
		}
		if request.Status != "succeeded" || operation.Status != model.OperationSucceeded {
			continue
		}
		switch request.Phase {
		case "nodes_configured":
			if configured != nil || !fleet.ValidIngressInventoryCheckpoint(request) || request.IngressInventoryDigest == "" || request.StateSerial <= 0 {
				return nil, fmt.Errorf("Fleet ingress configuration checkpoint is absent or repeated")
			}
			copy := request
			configured, checkpointID, stateSerial = &copy, operation.ID, request.StateSerial
		case "readiness_verified", "old_nodes_drained":
			if request.StateSerial <= 0 || (terminalEvidence && request.StateSerial != terminalSerial) {
				return nil, fmt.Errorf("Fleet terminal predecessor evidence is inconsistent")
			}
			terminalEvidence = true
			terminalSerial = request.StateSerial
		}
	}
	if configured == nil || !terminalEvidence || checkpointID == "" || revisions[checkpointID] <= 0 || latest.Revision <= 0 || terminalSerial != stateSerial {
		return nil, fmt.Errorf("latest Fleet attempt lacks complete ingress inventory evidence")
	}
	canonical, err := fleet.CanonicalIngressInventory(configured.IngressInventory)
	if err != nil {
		return nil, err
	}
	nodes, err := ingress.ParseFleetIngressInventory(canonical, configured.IngressInventoryDigest, cluster, environment, port)
	if err != nil {
		return nil, err
	}
	return &FleetIngressInventoryEvidence{PlanID: planID, AttemptID: latest.ID, AttemptRevision: latest.Revision,
		CheckpointID: checkpointID, CheckpointModRevision: revisions[checkpointID], StateSerial: stateSerial,
		Digest: configured.IngressInventoryDigest, Nodes: nodes}, nil
}
