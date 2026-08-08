package handler

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/model"
)

var maintenanceRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,199}$`)

type platformRequest struct {
	Ref       string `json:"ref"`
	Mode      string `json:"mode,omitempty"`
	DrainMode string `json:"drainMode,omitempty"`
}

func (h *Handler) QueuePlatformPreflight(w http.ResponseWriter, r *http.Request) {
	var req platformRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if !maintenanceRefPattern.MatchString(req.Ref) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_ref", "invalid platform ref")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.preflight", req.Ref, "read-only candidate build", map[string]interface{}{"ref": req.Ref})
}

func (h *Handler) QueuePlatformUpgrade(w http.ResponseWriter, r *http.Request) {
	var req platformRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if !maintenanceRefPattern.MatchString(req.Ref) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_platform_ref", "invalid platform ref")
		return
	}
	if req.Mode == "" {
		req.Mode = "restart"
	}
	if req.Mode != "restart" && req.Mode != "proxy" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_upgrade_mode", "mode must be restart or proxy")
		return
	}
	if req.DrainMode == "" {
		req.DrainMode = "fail"
	}
	if req.DrainMode != "fail" && req.DrainMode != "wait" && req.DrainMode != "force" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_drain_mode", "drainMode must be fail, wait, or force")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.upgrade", req.Ref, "control-plane replacement", map[string]interface{}{
		"ref": req.Ref, "mode": req.Mode, "drainMode": req.DrainMode,
	})
}

func (h *Handler) QueuePlatformSmoke(w http.ResponseWriter, r *http.Request) {
	h.queueMaintenanceOperation(w, r, "platform.smoke", "", "read-only platform assurance", map[string]interface{}{})
}

func (h *Handler) QueuePlatformRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SHA string `json:"sha"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !maintenanceRefPattern.MatchString(req.SHA) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_sha", "invalid release sha")
		return
	}
	h.queueMaintenanceOperation(w, r, "platform.rollback", req.SHA, "control-plane rollback", map[string]interface{}{"sha": req.SHA})
}

func (h *Handler) QueueHostAssurance(w http.ResponseWriter, r *http.Request) {
	h.queueMaintenanceOperation(w, r, "host.assure", "", "bounded host repair and endpoint probes", map[string]interface{}{})
}

func (h *Handler) queueMaintenanceOperation(w http.ResponseWriter, r *http.Request, kind, ref, risk string, payload map[string]interface{}) {
	requiredScope := ScopePlatformOperate
	if kind == "host.assure" {
		requiredScope = ScopeHostOperate
	}
	if _, ok := requireControlScope(w, r, requiredScope); !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must not exceed 200 characters")
		return
	}
	if idempotencyKey != "" {
		if existing, err := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); err == nil {
			writeJSON(w, existing)
			return
		} else if err != pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to resolve idempotent operation")
			return
		}
	}
	now := time.Now().UTC()
	op := &model.Operation{
		ID: uuid.NewString(), Kind: kind, Ref: ref, Status: model.OperationQueued,
		Risk: risk, Source: "control-api", Message: "queued " + kind,
		Payload: payload, Metadata: map[string]interface{}{}, StartedAt: now, MaxAttempts: 1,
	}
	if idempotencyKey != "" {
		op.Metadata["idempotencyKey"] = idempotencyKey
	}
	if err := h.db.InsertOperation(r.Context(), op); err != nil {
		if idempotencyKey != "" {
			if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), idempotencyKey); lookupErr == nil {
				writeJSON(w, existing)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_create_failed", "failed to create operation")
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusAccepted, op)
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target interface{}) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := decodeControlJSON(w, r, target); err != nil && err != io.EOF {
		return fmt.Errorf("invalid request body")
	}
	return nil
}
