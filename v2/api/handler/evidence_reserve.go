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
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "evidence_reserve_exhausted", "audited mutations are refused until evidence is archived: "+strings.Join(status.Reasons, "; "))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func evidenceReserveExempt(r *http.Request) bool {
	// V3 accepted mutations reserve evidence atomically after resolving their
	// identity. Letting them reach that transaction keeps an exact replay
	// available even while the reserve is exhausted; this cached middleware
	// check remains for non-idempotent legacy mutations.
	return r.URL.Path == "/api/v1/auth/revoke" || strings.TrimSpace(r.Header.Get("Idempotency-Key")) != ""
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
