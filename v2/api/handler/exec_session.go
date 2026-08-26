package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/hub"
	"norn/v2/api/store"
)

const execSessionTTL = time.Hour

type execSessionCreateRequest struct {
	AllocationID string   `json:"allocationId"`
	Process      string   `json:"process"`
	Argv         []string `json:"argv"`
	Terminal     *bool    `json:"terminal"`
	Columns      int      `json:"columns"`
	Rows         int      `json:"rows"`
}

func (h *Handler) CreateExecSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || principal.TokenID == "" || principal.DeviceID == "" {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_device_required", "exec sessions require an enrolled device token")
		return
	}
	if h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "nomad_unavailable", "Nomad is not connected")
		return
	}
	appID := chi.URLParam(r, "id")
	stepUpToken := strings.TrimSpace(r.Header.Get("X-Norn-Step-Up"))
	claims, err := h.verifyExecStepUp(stepUpToken, principal, appID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "exec_step_up_required", "a valid, app-bound X-Norn-Step-Up token is required")
		return
	}
	var req execSessionCreateRequest
	if err := decodeControlJSON(w, r, &req); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_exec_request", "invalid exec session request")
		return
	}
	if len(req.Argv) == 0 {
		req.Argv = []string{"/bin/sh"}
	}
	if len(req.Argv) > 128 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_exec_argv", "argv may not contain more than 128 elements")
		return
	}
	for _, arg := range req.Argv {
		if len(arg) > 8192 {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_exec_argv", "argv element exceeds 8192 bytes")
			return
		}
	}
	terminal := true
	if req.Terminal != nil {
		terminal = *req.Terminal
	}
	if req.Columns == 0 {
		req.Columns = 80
	}
	if req.Rows == 0 {
		req.Rows = 24
	}
	if req.Columns < 1 || req.Columns > 1000 || req.Rows < 1 || req.Rows > 1000 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_terminal_size", "columns and rows must be between 1 and 1000")
		return
	}
	allocationID, task, err := h.nomad.ResolveExecTarget(appID, req.AllocationID, req.Process)
	if err != nil {
		WriteControlProblem(w, r, http.StatusNotFound, "running_allocation_not_found", err.Error())
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(execSessionTTL)
	if !principal.ExpiresAt.IsZero() && principal.ExpiresAt.Before(expiresAt) {
		expiresAt = principal.ExpiresAt
	}
	if !expiresAt.After(now) {
		WriteControlProblem(w, r, http.StatusUnauthorized, "token_expired", "access token has expired")
		return
	}
	session := &store.ExecSession{
		ID: uuid.NewString(), DeviceID: principal.DeviceID, TokenJTI: principal.TokenID,
		ChallengeID: claims.ChallengeID, AppID: appID, AllocationID: allocationID, Task: task,
		Command: req.Argv, CommandDigest: execCommandDigest(req.Argv), Terminal: terminal, Columns: req.Columns, Rows: req.Rows,
		Status: "pending", CreatedAt: now, ExpiresAt: expiresAt,
		RemoteAddr: r.RemoteAddr, UserAgent: bounded(r.UserAgent(), 300),
	}
	if err := h.db.CreateExecSession(r.Context(), session); err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusConflict, "step_up_already_consumed", "step-up authorization is expired or has already been consumed")
		return
	} else if err == store.ErrTooManyActiveExecSessions {
		w.Header().Set("Retry-After", "60")
		WriteControlProblem(w, r, http.StatusTooManyRequests, "exec_session_limit_reached", "device already has the maximum number of active exec sessions")
		return
	} else if err == store.ErrRateLimited {
		w.Header().Set("Retry-After", "3600")
		WriteControlProblem(w, r, http.StatusTooManyRequests, "exec_session_rate_limited", "too many exec sessions were created recently")
		return
	} else if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "exec_session_create_failed", "failed to persist exec session")
		return
	}
	w.Header().Set("Location", "/api/v1/exec-sessions/"+session.ID)
	preventSensitiveResponseCaching(w)
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{
		"session": session, "protocol": "norn.exec/v1",
		"streamPath": "/api/v1/exec-sessions/" + session.ID + "/stream",
	})
}

func (h *Handler) GetExecSession(w http.ResponseWriter, r *http.Request) {
	session, ok := h.authorizedExecSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, session)
}

func (h *Handler) ListExecSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || (principal.DeviceID == "" && !principal.Allows(ScopeAdmin)) {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_device_required", "exec audit records require an enrolled device token")
		return
	}
	sessions, err := h.db.ListExecSessions(r.Context(), principal.DeviceID, principal.Allows(ScopeAdmin))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "exec_session_list_failed", "failed to list exec sessions")
		return
	}
	writeJSON(w, map[string]interface{}{"sessions": sessions})
}

func (h *Handler) CancelExecSession(w http.ResponseWriter, r *http.Request) {
	session, ok := h.authorizedExecSession(w, r)
	if !ok {
		return
	}
	if session.Status != "pending" && session.Status != "running" {
		WriteControlProblem(w, r, http.StatusConflict, "exec_session_not_cancelable", "exec session is already terminal")
		return
	}
	if err := h.db.FinishExecSession(r.Context(), session.ID, "canceled", nil, "exec_session_canceled"); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "exec_session_cancel_failed", "failed to cancel exec session")
		return
	}
	if conn, exists := h.execConns.LoadAndDelete(session.ID); exists {
		_ = conn.(*websocket.Conn).Close()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ExecSessionStream(w http.ResponseWriter, r *http.Request) {
	if h.nomad == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "nomad_unavailable", "Nomad is not connected")
		return
	}
	session, ok := h.authorizedExecSession(w, r)
	if !ok {
		return
	}
	if session.Status != "pending" || time.Now().After(session.ExpiresAt) {
		WriteControlProblem(w, r, http.StatusConflict, "exec_session_inactive", "exec session is expired, active, or already terminal")
		return
	}
	allowedOrigins := map[string]bool{"http://localhost:5173": true, "http://localhost:3000": true}
	if h.cfg != nil {
		for _, origin := range strings.Split(h.cfg.AllowedOrigins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				allowedOrigins[origin] = true
			}
		}
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		CheckOrigin: func(req *http.Request) bool { return hub.OriginAllowed(req, allowedOrigins) },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("exec session websocket upgrade: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(64 << 10)
	if err := h.db.ConnectExecSession(r.Context(), session.ID); err != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteJSON(map[string]interface{}{
			"frame": "error", "sequence": 1, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"code": "exec_session_inactive", "detail": "session could not be connected",
		})
		return
	}
	h.execConns.Store(session.ID, conn)
	defer h.execConns.Delete(session.ID)
	revocationCtx, stopRevocationWatch := context.WithCancel(r.Context())
	defer stopRevocationWatch()
	go h.watchExecSessionRevocation(revocationCtx, session.ID, conn)
	_ = conn.SetReadDeadline(session.ExpiresAt)
	exitCode, execErr := h.nomad.ExecSessionWebSocket(session.AllocationID, session.Task, session.Command, session.Terminal, session.Columns, session.Rows, session.ExpiresAt, conn)
	auditContext, cancelAudit := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAudit()
	current, _ := h.db.GetExecSession(auditContext, session.ID)
	if current != nil && current.Status == "canceled" {
		return
	}
	if execErr != nil {
		if !time.Now().Before(session.ExpiresAt) {
			_ = h.db.FinishExecSession(auditContext, session.ID, "expired", &exitCode, "exec_session_expired")
			return
		}
		_ = h.db.FinishExecSession(auditContext, session.ID, "failed", &exitCode, "exec_transport_failed")
		log.Printf("exec session %s failed: %v", session.ID, execErr)
		return
	}
	_ = h.db.FinishExecSession(auditContext, session.ID, "completed", &exitCode, "")
}

func (h *Handler) watchExecSessionRevocation(ctx context.Context, sessionID string, conn *websocket.Conn) {
	if h.db == nil || conn == nil {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			session, err := h.db.GetExecSession(lookupCtx, sessionID)
			cancel()
			if err == pgx.ErrNoRows || (err == nil && session.Status != "running") {
				_ = conn.Close()
				return
			}
		}
	}
}

func (h *Handler) authorizedExecSession(w http.ResponseWriter, r *http.Request) (*store.ExecSession, bool) {
	principal, ok := AccessPrincipalFromRequest(r)
	if !ok || (principal.DeviceID == "" && !principal.Allows(ScopeAdmin)) {
		WriteControlProblem(w, r, http.StatusUnauthorized, "managed_device_required", "exec sessions require an enrolled device token")
		return nil, false
	}
	session, err := h.db.GetExecSession(r.Context(), chi.URLParam(r, "id"))
	if err == pgx.ErrNoRows {
		WriteControlProblem(w, r, http.StatusNotFound, "exec_session_not_found", "exec session was not found")
		return nil, false
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "exec_session_lookup_failed", "failed to load exec session")
		return nil, false
	}
	if session.DeviceID != principal.DeviceID && !principal.Allows(ScopeAdmin) {
		WriteControlProblem(w, r, http.StatusForbidden, "exec_session_forbidden", "exec session belongs to another device")
		return nil, false
	}
	return session, true
}

func (h *Handler) closeExecSessionConnections(ids []string) {
	for _, id := range ids {
		if conn, exists := h.execConns.LoadAndDelete(id); exists {
			_ = conn.(*websocket.Conn).Close()
		}
	}
}

func execCommandDigest(argv []string) string {
	encoded, _ := json.Marshal(argv)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
