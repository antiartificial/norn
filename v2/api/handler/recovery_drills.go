package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/store"
)

const recoveryDrillsSchema = "norn.recovery-drills/v1"

var requiredRecoveryDrillKinds = []string{"artifact.rollback", "database.restore", "node.failover"}

var recoveryDrillKinds = map[string]bool{
	"artifact.rollback": true,
	"database.restore":  true,
	"node.failover":     true,
	"region.failover":   true,
	"control.restore":   true,
}

func (h *Handler) ListRecoveryDrills(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	if h.db == nil || h.db.Pool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "recovery_drills_unavailable", "recovery drill storage is unavailable")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	drills, err := h.db.ListRecoveryDrills(r.Context(), limit)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "recovery_drills_read_failed", "failed to read recovery drills")
		return
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]any{"schema": recoveryDrillsSchema, "requiredKinds": requiredRecoveryDrillKinds, "drills": drills, "count": len(drills)})
}

func (h *Handler) StartRecoveryDrill(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAdmin)
	if !ok {
		return
	}
	if h.db == nil || h.db.Pool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "recovery_drills_unavailable", "recovery drill storage is unavailable")
		return
	}
	var req struct {
		Kind   string `json:"kind"`
		Target string `json:"target,omitempty"`
	}
	if err := decodeControlJSON(w, r, &req); err != nil {
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.Target = strings.TrimSpace(req.Target)
	if !recoveryDrillKinds[req.Kind] {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_recovery_drill_kind", "unsupported recovery drill kind")
		return
	}
	if len(req.Target) > 256 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_recovery_drill_target", "target must not exceed 256 characters")
		return
	}
	drill := &store.RecoveryDrill{
		ID: uuid.NewString(), Kind: req.Kind, Target: req.Target, Status: "running",
		InitiatedBy: principal.Subject, Evidence: map[string]string{}, StartedAt: time.Now().UTC(),
	}
	if drill.InitiatedBy == "" {
		drill.InitiatedBy = "authenticated"
	}
	if err := h.db.InsertRecoveryDrill(r.Context(), drill); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "recovery_drill_create_failed", "failed to create recovery drill")
		return
	}
	w.Header().Set("Location", "/api/v1/production/drills/"+drill.ID)
	writeJSONStatus(w, http.StatusCreated, drill)
}

func (h *Handler) CompleteRecoveryDrill(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
		return
	}
	if h.db == nil || h.db.Pool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "recovery_drills_unavailable", "recovery drill storage is unavailable")
		return
	}
	var req struct {
		Status   string            `json:"status"`
		Evidence map[string]string `json:"evidence,omitempty"`
	}
	if err := decodeControlJSON(w, r, &req); err != nil {
		return
	}
	req.Status = strings.TrimSpace(req.Status)
	if req.Status != "passed" && req.Status != "failed" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_recovery_drill_status", "status must be passed or failed")
		return
	}
	if err := validateRecoveryDrillEvidence(req.Evidence); err != "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_recovery_drill_evidence", err)
		return
	}
	drill, err := h.db.FinishRecoveryDrill(r.Context(), chi.URLParam(r, "id"), req.Status, req.Evidence, time.Now().UTC())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteControlProblem(w, r, http.StatusConflict, "recovery_drill_not_running", "recovery drill was not found or is already complete")
		} else {
			WriteControlProblem(w, r, http.StatusInternalServerError, "recovery_drill_complete_failed", "failed to complete recovery drill")
		}
		return
	}
	writeJSON(w, drill)
}

func validateRecoveryDrillEvidence(evidence map[string]string) string {
	if len(evidence) > 20 {
		return "evidence must contain at most 20 entries"
	}
	for key, value := range evidence {
		if len(strings.TrimSpace(key)) == 0 || len(key) > 64 {
			return "evidence keys must contain 1 to 64 characters"
		}
		if len(value) > 1024 {
			return "evidence values must not exceed 1024 characters"
		}
		normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
		for _, sensitive := range []string{"password", "secret", "token", "credential", "privatekey", "apikey", "accesskey"} {
			if strings.Contains(normalized, sensitive) {
				return "evidence must contain references or checksums, not secret-bearing fields"
			}
		}
	}
	return ""
}
