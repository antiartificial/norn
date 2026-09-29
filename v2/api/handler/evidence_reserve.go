package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"norn/v2/api/store"
)

// evidenceReserveTTL bounds how stale one process's admission view may be.
const evidenceReserveTTL = 2 * time.Second

// EvidenceReserveAdmissionMiddleware refuses new audited mutations while the
// durable evidence reserve is exhausted (too much unarchived evidence, or
// the archive reports no capacity), instead of letting the hot store fill or
// evidence be discarded. The refusal itself passes through the mutation
// audit. Reads, diagnostics and log collection are unaffected, and token
// revocation stays possible so a security response is never blocked.
func (h *Handler) EvidenceReserveAdmissionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAuditedMutation(r) || evidenceReserveExempt(r) || h.db == nil || h.db.Pool == nil {
			next.ServeHTTP(w, r)
			return
		}
		status, err := h.evidenceReserve(r.Context())
		if err != nil {
			w.Header().Set("Retry-After", "5")
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "evidence_reserve_unavailable", "the evidence reserve could not be read; audited mutations are refused")
			return
		}
		if status.Exhausted {
			w.Header().Set("Retry-After", "60")
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "evidence_reserve_exhausted", "audited mutations are refused until evidence reserve capacity is restored: "+strings.Join(status.Reasons, "; "))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func evidenceReserveExempt(r *http.Request) bool {
	// A caller-supplied Idempotency-Key does not prove that the matched handler
	// uses atomic acceptance. Exempting it would let legacy mutation routes
	// bypass the reserve entirely. A route-aware replay exception requires an
	// explicit server-owned acceptance marker at the routing boundary.
	return r.URL.Path == "/api/v1/auth/revoke"
}

func (h *Handler) evidenceReserve(ctx context.Context) (store.EvidenceReserveStatus, error) {
	h.evidenceReserveMu.Lock()
	defer h.evidenceReserveMu.Unlock()
	if !h.evidenceReserveAt.IsZero() && time.Since(h.evidenceReserveAt) < evidenceReserveTTL {
		return h.evidenceReserveStatus, nil
	}
	status, err := h.db.EvidenceReserve(ctx)
	if err != nil {
		return status, err
	}
	h.evidenceReserveStatus, h.evidenceReserveAt = status, time.Now()
	return status, nil
}
