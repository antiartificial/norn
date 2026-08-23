package handler

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

const productionMutationGateTTL = 5 * time.Second

// ProductionMutationAdmissionMiddleware keeps infrastructure-changing
// requests fail-closed when the scheduler, discovery plane, database, or
// durable audit substrate cannot prove production readiness. Recovery and
// identity operations remain available so an operator can repair the system.
func (h *Handler) ProductionMutationAdmissionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg == nil || !h.cfg.Production() || !productionSubstrateMutation(r) {
			next.ServeHTTP(w, r)
			return
		}
		blockers := h.productionMutationBlockers(r)
		if len(blockers) > 0 {
			w.Header().Set("Retry-After", "5")
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "production_substrate_unready", "production mutation blocked by: "+strings.Join(blockers, ", "))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) productionMutationBlockers(r *http.Request) []string {
	now := time.Now()
	h.productionGateMu.Lock()
	defer h.productionGateMu.Unlock()
	if !h.productionGateAt.IsZero() && now.Sub(h.productionGateAt) < productionMutationGateTTL {
		return append([]string{}, h.productionGateBlockers...)
	}
	report := h.buildProductionReadiness(r.Context())
	blockers := productionCriticalBlockers(report)
	h.productionGateAt = now
	h.productionGateBlockers = append([]string{}, blockers...)
	return blockers
}

func productionSubstrateMutation(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	path := r.URL.Path
	return strings.HasPrefix(path, "/api/apps/") ||
		strings.HasPrefix(path, "/api/deploy-groups/") ||
		strings.HasPrefix(path, "/api/platform/releases/") ||
		strings.HasPrefix(path, "/api/v1/platform/") ||
		strings.HasPrefix(path, "/api/webhooks/") ||
		strings.HasPrefix(path, "/api/ops/contextdb/feedback/")
}

func productionCriticalBlockers(report ProductionReadinessReport) []string {
	critical := map[string]bool{
		"nomad.reachable": true, "nomad.acl": true, "nomad.tls": true,
		"nomad.quorum": true, "nomad.clients": true,
		"consul.reachable": true, "consul.acl": true, "consul.tls": true,
		"consul.quorum": true, "database.tls": true, "database.external": true,
		"database.pitr": true, "database.replicas": true, "audit.durable": true,
	}
	blockers := []string{}
	for _, check := range report.Checks {
		if critical[check.ID] && check.Status == "fail" {
			blockers = append(blockers, check.ID)
		}
	}
	sort.Strings(blockers)
	return blockers
}
