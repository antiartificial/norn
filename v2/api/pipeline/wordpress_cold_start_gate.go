package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// wordpressColdStartGate is deliberately narrower than an ordinary deploy.
// The durable reservation is useful only where there can be one observed
// runtime identity; rolling and multi-region delivery need their own protocol.
type wordpressColdStartGate struct {
	reservationID string
	region        model.ResolvedRegion
}

func (p *Pipeline) reserveWordPressVerifiedTLSColdStart(ctx context.Context, st *state) (*wordpressColdStartGate, error) {
	if !p.WPColdStartGate || !model.IsQualifiedWordPressVerifiedTLSPrebuilt(st.spec, st.imageTag) {
		return nil, nil
	}
	if p.DB == nil || p.Nomad == nil || st.database == nil || len(st.spec.ResolvedRegions()) != 1 || st.spec.ProcessCount() != 1 {
		return nil, fmt.Errorf("wordpress verified-TLS cold-start gate requires one region, one service allocation, PostgreSQL, Nomad, and accepted database targets")
	}
	if err := p.requireLiveClaim(ctx, st); err != nil {
		return nil, err
	}
	targets, err := wordpressAcceptedMySQLWriterTargets(st)
	if err != nil {
		return nil, err
	}
	region := st.spec.ResolvedRegions()[0]
	if err := p.Nomad.RequireColdStartJobAbsent(region.NomadRegion, st.spec.App); err != nil {
		return nil, fmt.Errorf("prove WordPress cold-start job absence: %w", err)
	}
	allocations, err := p.Nomad.JobAllocationsRegion(st.spec.App, region.NomadRegion)
	if err != nil {
		return nil, fmt.Errorf("inspect WordPress cold-start allocations: %w", err)
	}
	if len(activeWordPressAllocations(allocations)) != 0 {
		return nil, fmt.Errorf("wordpress verified-TLS cold-start gate refuses existing active allocations")
	}
	reservationID := wordpressColdStartReservationID(st)
	if _, err := p.DB.ReserveMySQLRuntimeLaunch(ctx, reservationID, targets); err != nil {
		return nil, fmt.Errorf("reserve wordpress verified-TLS MySQL launch: %w", err)
	}
	return &wordpressColdStartGate{reservationID: reservationID, region: region}, nil
}

// markWordPressVerifiedTLSColdStartLaunched records only an identity observed
// after this cold start's submit. A lost registration response, an unreadable
// allocation list, or any multiplicity is ambiguous and remains blocking.
func (p *Pipeline) markWordPressVerifiedTLSColdStartLaunched(ctx context.Context, gate *wordpressColdStartGate, app, evalID string) error {
	if strings.TrimSpace(evalID) == "" {
		p.containWordPressVerifiedTLSColdStart(ctx, gate)
		return fmt.Errorf("wordpress verified-TLS cold-start submit returned no evaluation identity")
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		allocations, err := p.Nomad.JobAllocationsRegion(app, gate.region.NomadRegion)
		if err != nil {
			p.containWordPressVerifiedTLSColdStart(ctx, gate)
			return fmt.Errorf("observe WordPress cold-start runtime: %w", err)
		}
		active := activeWordPressAllocations(allocations)
		if len(active) > 1 || (len(active) == 1 && !matchesWordPressColdStartAllocation(active[0], app, evalID)) {
			p.containWordPressVerifiedTLSColdStart(ctx, gate)
			return fmt.Errorf("wordpress verified-TLS cold-start runtime identity is ambiguous")
		}
		if len(active) == 1 {
			if err := p.DB.MarkMySQLRuntimeLaunchLaunched(ctx, gate.reservationID, active[0].ID); err != nil {
				p.containWordPressVerifiedTLSColdStart(ctx, gate)
				return fmt.Errorf("record WordPress cold-start runtime identity: %w", err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			p.containWordPressVerifiedTLSColdStart(ctx, gate)
			return fmt.Errorf("observe WordPress cold-start runtime: %w", ctx.Err())
		case <-deadline.C:
			p.containWordPressVerifiedTLSColdStart(ctx, gate)
			return fmt.Errorf("wordpress verified-TLS cold-start runtime identity is ambiguous")
		case <-ticker.C:
		}
	}
}

func matchesWordPressColdStartAllocation(allocation *nomadapi.AllocationListStub, app, evalID string) bool {
	return allocation != nil && strings.TrimSpace(allocation.ID) != "" && allocation.JobID == app && allocation.EvalID == evalID
}

func (p *Pipeline) containWordPressVerifiedTLSColdStart(ctx context.Context, gate *wordpressColdStartGate) {
	if gate != nil {
		_ = p.DB.ContainMySQLRuntimeLaunchForInspection(ctx, gate.reservationID)
	}
}

func wordpressAcceptedMySQLWriterTargets(st *state) ([]database.TargetIdentity, error) {
	set, err := recordedTargetSetFromPayload(st.operationPayload)
	if err != nil || set == nil || st.database == nil || len(set.Targets) != len(st.database.named) || len(set.Targets) == 0 {
		return nil, fmt.Errorf("wordpress verified-TLS cold-start gate cannot prove the accepted MySQL writer target set")
	}
	targets := make([]database.TargetIdentity, 0, len(set.Targets))
	seen := map[string]bool{}
	for _, recorded := range set.Targets {
		bound := st.database.named[recorded.Name]
		if bound == nil || bound.resolved.Target != recorded.Target || recorded.Target.Engine != database.EngineMySQL || seen[recorded.Name] {
			return nil, fmt.Errorf("wordpress verified-TLS cold-start gate cannot prove the accepted MySQL writer target set")
		}
		seen[recorded.Name] = true
		targets = append(targets, recorded.Target)
	}
	sort.Slice(targets, func(i, j int) bool {
		return fmt.Sprintf("%s/%d/%s/%d/%s/%s", targets[i].ServiceID, targets[i].ServiceGeneration, targets[i].BindingID, targets[i].BindingGeneration, targets[i].Database, targets[i].Role) < fmt.Sprintf("%s/%d/%s/%d/%s/%s", targets[j].ServiceID, targets[j].ServiceGeneration, targets[j].BindingID, targets[j].BindingGeneration, targets[j].Database, targets[j].Role)
	})
	return targets, nil
}

func wordpressColdStartReservationID(st *state) string {
	return store.WordPressColdStartReservationID(st.claim.OperationID(), st.deploymentID, stringFromMap(st.operationPayload, "specDigest"))
}

func activeWordPressAllocations(allocations []*nomadapi.AllocationListStub) []*nomadapi.AllocationListStub {
	active := make([]*nomadapi.AllocationListStub, 0, len(allocations))
	for _, allocation := range allocations {
		if allocation == nil {
			// A malformed response is not evidence of an empty allocation set.
			return []*nomadapi.AllocationListStub{{}}
		}
		switch allocation.ClientStatus {
		case "complete", "failed", "lost":
			continue
		default:
			active = append(active, allocation)
		}
	}
	return active
}
