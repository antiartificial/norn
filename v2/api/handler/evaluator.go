package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type evaluatorReadinessResponse struct {
	GeneratedAt  string                        `json:"generatedAt"`
	Namespaces   []evaluatorNamespaceReadiness `json:"namespaces"`
	OverallReady bool                          `json:"overallReady"`
	Summary      string                        `json:"summary"`
}

type evaluatorNamespaceReadiness struct {
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

func (h *Handler) EvaluatorReadiness(w http.ResponseWriter, r *http.Request) {
	spec := h.findSpec("contextdb")
	if spec == nil {
		writeError(w, http.StatusNotFound, "contextdb app not found")
		return
	}

	workerURL := ""
	if manifest, err := h.buildServiceManifest(); err == nil {
		for _, svc := range manifest.Services {
			if svc.App == "contextdb" && svc.Process == "review-worker" {
				workerURL = firstReachableServiceURL(svc)
				break
			}
		}
	}
	if workerURL == "" {
		writeError(w, http.StatusBadGateway, "contextdb review worker unavailable")
		return
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	var workerStatus contextDBWorkerStatus
	if err := getContextDBJSON(httpClient, strings.TrimRight(workerURL, "/")+"/v1/status", &workerStatus); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("worker status: %v", err))
		return
	}

	out := evaluatorReadinessResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Namespaces:  []evaluatorNamespaceReadiness{},
	}

	providerBackedTotal := 0
	providerBackedReady := 0

	for _, ns := range workerStatus.Policy.Namespaces {
		blockers := []string{}

		isProviderBacked := ns.Evaluator != "rules" && ns.Evaluator != ""

		if ns.Evaluator == "rules" {
			blockers = append(blockers, "using rules-only evaluator")
		}
		if ns.ProviderKeyRequired && !ns.ProviderKeyConfigured {
			blockers = append(blockers, "provider key not configured")
		}
		if !ns.OK {
			msg := "policy has errors"
			if ns.Error != "" {
				msg = "policy has errors: " + ns.Error
			}
			blockers = append(blockers, msg)
		}
		if ns.DryRun {
			blockers = append(blockers, "namespace is in dry-run mode")
		}
		if len(ns.Warnings) > 0 {
			blockers = append(blockers, "policy has warnings")
		}

		ready := len(blockers) == 0

		if isProviderBacked {
			providerBackedTotal++
			if ready {
				providerBackedReady++
			}
		}

		out.Namespaces = append(out.Namespaces, evaluatorNamespaceReadiness{
			Namespace:             ns.Namespace,
			Evaluator:             ns.Evaluator,
			Provider:              ns.Provider,
			DryRun:                ns.DryRun,
			ProviderKeyRequired:   ns.ProviderKeyRequired,
			ProviderKeyConfigured: ns.ProviderKeyConfigured,
			MutationAllowed:       ns.MutationAllowed,
			Ready:                 ready,
			Blockers:              blockers,
		})
	}

	if providerBackedTotal == 0 {
		out.OverallReady = false
		out.Summary = "no provider-backed evaluators configured"
	} else {
		out.OverallReady = providerBackedReady == providerBackedTotal
		out.Summary = fmt.Sprintf("%d/%d provider-backed namespaces ready", providerBackedReady, providerBackedTotal)
	}

	writeJSON(w, out)
}
