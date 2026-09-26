package nomad

import (
	"context"
	"fmt"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

// PeriodicForceEvaluation reads only the identity needed to prove that the
// evaluation returned by Force belongs to this periodic parent.
func (c *Client) PeriodicForceEvaluation(ctx context.Context, evalID, parentJobID string) (string, error) {
	if c == nil || c.api == nil || strings.TrimSpace(evalID) == "" || strings.TrimSpace(parentJobID) == "" {
		return "", fmt.Errorf("periodic force evaluation identity is invalid")
	}
	evaluation, _, err := c.api.Evaluations().Info(evalID, (&nomadapi.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return "", err
	}
	if evaluation == nil || evaluation.ID != evalID || evaluation.TriggeredBy != "periodic-job" || !strings.HasPrefix(evaluation.JobID, parentJobID+"/periodic-") {
		return "", fmt.Errorf("periodic force evaluation does not belong to requested parent")
	}
	return evaluation.JobID, nil
}
