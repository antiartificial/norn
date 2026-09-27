package etcdstore

import (
	"context"
	"fmt"

	"norn/v2/api/fleet"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// FleetIngressInventoryEvidence is an attempt-bound source for route readback.
// The deployment worker must still bind the Fleet plan to its accepted app
// intent and recheck this evidence before terminal traffic completion.
type FleetIngressInventoryEvidence struct {
	PlanID       string
	AttemptID    string
	CheckpointID string
	Digest       string
	Nodes        []ingress.IngressNode
}

func (s *V3OperationStore) CurrentFleetIngressInventory(ctx context.Context, planID, cluster, environment string, port int) (*FleetIngressInventoryEvidence, error) {
	attempts, err := s.ListFleetRunnerAttempts(ctx, planID)
	if err != nil {
		return nil, err
	}
	history, _, err := s.listFleetReconciliations(ctx, planID)
	if err != nil {
		return nil, err
	}
	return resolveFleetIngressInventory(planID, cluster, environment, port, attempts, history)
}

func resolveFleetIngressInventory(planID, cluster, environment string, port int, attempts []fleet.RunnerAttempt, history []model.Operation) (*FleetIngressInventoryEvidence, error) {
	if len(attempts) == 0 || attempts[0].PlanID != planID || attempts[0].Status != "succeeded" || attempts[0].CurrentPhase != "complete" {
		return nil, fmt.Errorf("latest Fleet attempt has not completed successfully")
	}
	latest := attempts[0]
	var configured *fleet.ReconciliationRequest
	checkpointID := ""
	complete := false
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
			if configured != nil || !fleet.ValidIngressInventoryCheckpoint(request) || request.IngressInventoryDigest == "" {
				return nil, fmt.Errorf("Fleet ingress configuration checkpoint is absent or repeated")
			}
			copy := request
			configured, checkpointID = &copy, operation.ID
		case "complete":
			if complete {
				return nil, fmt.Errorf("Fleet completion checkpoint is repeated")
			}
			complete = true
		}
	}
	if configured == nil || !complete || checkpointID == "" {
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
	return &FleetIngressInventoryEvidence{PlanID: planID, AttemptID: latest.ID, CheckpointID: checkpointID, Digest: configured.IngressInventoryDigest, Nodes: nodes}, nil
}
