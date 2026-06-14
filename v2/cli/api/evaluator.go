package api

type EvaluatorReadiness struct {
	GeneratedAt  string                        `json:"generatedAt"`
	Namespaces   []EvaluatorNamespaceReadiness `json:"namespaces"`
	OverallReady bool                          `json:"overallReady"`
	Summary      string                        `json:"summary"`
}

type EvaluatorNamespaceReadiness struct {
	Namespace             string   `json:"namespace"`
	Evaluator             string   `json:"evaluator"`
	Provider              string   `json:"provider"`
	DryRun                bool     `json:"dryRun"`
	ProviderKeyRequired   bool     `json:"providerKeyRequired"`
	ProviderKeyConfigured bool     `json:"providerKeyConfigured"`
	MutationAllowed       bool     `json:"mutationAllowed"`
	Ready                 bool     `json:"ready"`
	Blockers              []string `json:"blockers"`
}

func (c *Client) EvaluatorReadiness() (*EvaluatorReadiness, error) {
	var out EvaluatorReadiness
	if err := c.get("/api/ops/contextdb/evaluator-readiness", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
