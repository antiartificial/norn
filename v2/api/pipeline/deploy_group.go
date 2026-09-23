package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
)

// GroupDeployResult records the outcome of queuing a deploy for one app in a group.
type GroupDeployResult struct {
	App         string `json:"app"`
	SagaID      string `json:"sagaId,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	Replayed    bool   `json:"replayed,omitempty"`
	Error       string `json:"error,omitempty"`
}

type GroupQueueResult struct {
	OperationID string              `json:"operationId"`
	Replayed    bool                `json:"replayed,omitempty"`
	Deploys     []GroupDeployResult `json:"deploys"`
}

// RunGroup queues a deploy for each app in the deploy group and returns the results.
// If an app spec is not found, the error is recorded but processing continues.
func (p *Pipeline) RunGroup(ctx context.Context, group *model.DeployGroup, ref string, appsDir string, request EnqueueRequest) (GroupQueueResult, error) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return GroupQueueResult{}, fmt.Errorf("discover apps: %w", err)
	}

	specMap := make(map[string]*model.InfraSpec, len(specs))
	for _, s := range specs {
		specMap[s.App] = s
	}

	members := make([]string, 0, len(group.Apps))
	for _, app := range group.Apps {
		members = append(members, app.App)
	}
	sort.Strings(members)
	now := time.Now().UTC()
	finished := now
	parentOperation := model.Operation{ID: uuid.NewString(), Kind: "app.deploy-group", Ref: group.Name, Status: model.OperationSucceeded, Risk: "multi-app rolling update plan", Source: "pipeline", Message: "accepted deploy group " + group.Name, Payload: map[string]interface{}{"group": group.Name, "members": members, "ref": ref}, StartedAt: now, FinishedAt: &finished, MaxAttempts: 1}
	parentAccepted, err := p.acceptOperation(ctx, request, parentOperation, nil, nil)
	if err != nil {
		return GroupQueueResult{}, err
	}
	originalMembers := stringSliceFromMap(parentAccepted.Operation.Payload, "members")
	originalRef := stringFromMap(parentAccepted.Operation.Payload, "ref")
	if len(originalMembers) == 0 {
		return GroupQueueResult{}, fmt.Errorf("accepted deploy group manifest is empty")
	}
	results := make([]GroupDeployResult, 0, len(originalMembers))
	for _, appName := range originalMembers {
		spec, ok := specMap[appName]
		if !ok {
			results = append(results, GroupDeployResult{
				App:   appName,
				Error: fmt.Sprintf("app %s not found", appName),
			})
			continue
		}
		child := DerivedChildRequest(request, "deploy-group:"+group.Name, appName, map[string]interface{}{"group": group.Name, "parentOperationId": parentAccepted.Operation.ID, "members": originalMembers, "app": appName, "ref": originalRef})
		accepted, err := p.Run(ctx, spec, originalRef, child)
		if err != nil {
			results = append(results, GroupDeployResult{App: appName, Error: err.Error()})
			continue
		}
		results = append(results, GroupDeployResult{
			App: appName, SagaID: accepted.Operation.SagaID, OperationID: accepted.Operation.ID, Replayed: accepted.Replayed,
		})
	}
	return GroupQueueResult{OperationID: parentAccepted.Operation.ID, Replayed: parentAccepted.Replayed, Deploys: results}, nil
}
