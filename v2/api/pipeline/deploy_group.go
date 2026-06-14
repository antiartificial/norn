package pipeline

import (
	"fmt"

	"norn/v2/api/model"
)

// GroupDeployResult records the outcome of queuing a deploy for one app in a group.
type GroupDeployResult struct {
	App    string `json:"app"`
	SagaID string `json:"sagaId,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RunGroup queues a deploy for each app in the deploy group and returns the results.
// If an app spec is not found, the error is recorded but processing continues.
func (p *Pipeline) RunGroup(group *model.DeployGroup, ref string, appsDir string) ([]GroupDeployResult, error) {
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return nil, fmt.Errorf("discover apps: %w", err)
	}

	specMap := make(map[string]*model.InfraSpec, len(specs))
	for _, s := range specs {
		specMap[s.App] = s
	}

	var results []GroupDeployResult
	for _, app := range group.Apps {
		spec, ok := specMap[app.App]
		if !ok {
			results = append(results, GroupDeployResult{
				App:   app.App,
				Error: fmt.Sprintf("app %s not found", app.App),
			})
			continue
		}
		sagaID := p.Run(spec, ref)
		results = append(results, GroupDeployResult{
			App:    app.App,
			SagaID: sagaID,
		})
	}

	return results, nil
}
