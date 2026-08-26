package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/store"
)

const mutationAuditSchema = "norn.mutation-audit/v1"
const mutationAuditIncidentSchema = "norn.mutation-audit-incident/v1"

var mutationAuditIncidentReasons = map[string]bool{
	"legacy_timestamp_precision":   true,
	"legacy_signing_compatibility": true,
	"signing_key_loss":             true,
	"storage_corruption":           true,
	"other":                        true,
}

// MutationAuditMiddleware reserves a durable receipt before a mutating handler
// executes. Production requests fail closed if that reservation cannot be
// written. A handler crash or post-action database failure leaves a visible
// started receipt instead of silently losing the audit trail.
func (h *Handler) MutationAuditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAuditedMutation(r) {
			next.ServeHTTP(w, r)
			return
		}

		production := h.cfg != nil && h.cfg.Production()
		if h.db == nil || h.db.Pool == nil {
			if production {
				WriteControlProblem(w, r, http.StatusServiceUnavailable, "mutation_audit_unavailable", "durable mutation audit is unavailable")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if production && len(h.cfg.AuditSigningKey) < 32 {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "mutation_audit_unsigned", "production mutation audit signing is not configured")
			return
		}

		started := time.Now().UTC()
		event := store.MutationAuditEvent{
			ID: uuid.NewString(), RequestID: middleware.GetReqID(r.Context()),
			PrincipalSubject: "unauthenticated", Method: r.Method, Path: mutationAuditPath(r),
			ClientIP: clientIP(r), UserAgent: truncateAuditValue(r.UserAgent(), 512), StartedAt: started,
		}
		if h.cfg != nil {
			event.KeyID = auditKeyID(h.cfg.AuditSigningKey)
		}
		if principal, ok := AccessPrincipalFromRequest(r); ok {
			event.PrincipalSubject = truncateAuditValue(principal.Subject, 256)
			if event.PrincipalSubject == "" {
				event.PrincipalSubject = "authenticated"
			}
			event.TokenID = truncateAuditValue(principal.TokenID, 128)
			event.DeviceID = truncateAuditValue(principal.DeviceID, 128)
			event.Scopes = append([]string{}, principal.Scopes...)
			sort.Strings(event.Scopes)
		}

		reserveCtx, cancelReserve := context.WithTimeout(r.Context(), 2*time.Second)
		err := h.db.ReserveMutationAudit(reserveCtx, &event)
		cancelReserve()
		if err != nil {
			if production {
				WriteControlProblem(w, r, http.StatusServiceUnavailable, "mutation_audit_unavailable", "failed to reserve durable mutation audit receipt")
				return
			}
			log.Printf("mutation audit reserve: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			// chi finalizes the matched route pattern while the downstream router
			// runs. Persist the template rather than resource identifiers from the
			// incoming URL whenever routing completed far enough to identify it.
			event.Path = mutationAuditPath(r)
			if recovered := recover(); recovered != nil {
				h.finishMutationAudit(event, http.StatusInternalServerError, "crashed", started)
				panic(recovered)
			}
			h.finishMutationAudit(event, recorder.status, mutationOutcome(recorder.status), started)
		}()
		next.ServeHTTP(recorder, r)
	})
}

func (h *Handler) finishMutationAudit(event store.MutationAuditEvent, status int, outcome string, started time.Time) {
	finished := time.Now().UTC()
	event.Status = status
	event.Outcome = outcome
	event.FinishedAt = &finished
	event.DurationMs = finished.Sub(started).Milliseconds()
	digest := ""
	if h.cfg != nil {
		digest = signMutationAudit(h.cfg.AuditSigningKey, event)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.db.FinishMutationAudit(ctx, event.ID, event.Path, status, outcome, finished, event.DurationMs, digest); err != nil {
		log.Printf("mutation audit finalize %s: %v", event.ID, err)
		return
	}
	h.maybePruneMutationAudits(finished)
}

func (h *Handler) maybePruneMutationAudits(now time.Time) {
	if h.cfg == nil || h.cfg.AuditRetentionDays <= 0 || h.db == nil || h.db.Pool == nil {
		return
	}
	h.auditPruneMu.Lock()
	if !h.auditPruneAt.IsZero() && now.Sub(h.auditPruneAt) < 6*time.Hour {
		h.auditPruneMu.Unlock()
		return
	}
	h.auditPruneAt = now
	h.auditPruneMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deleted, err := h.db.PruneMutationAudits(ctx, now.AddDate(0, 0, -h.cfg.AuditRetentionDays))
	if err != nil {
		h.auditPruneMu.Lock()
		if h.auditPruneAt.Equal(now) {
			h.auditPruneAt = time.Time{}
		}
		h.auditPruneMu.Unlock()
		log.Printf("mutation audit prune: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("mutation audit pruned %d expired receipt(s)", deleted)
	}
}

func (h *Handler) MutationAuditEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAdmin); !ok {
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
	if h.db == nil || h.db.Pool == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "mutation_audit_unavailable", "durable mutation audit is unavailable")
		return
	}
	events, err := h.db.ListMutationAudits(r.Context(), limit)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "mutation_audit_read_failed", "failed to read mutation audit events")
		return
	}
	keys := []string{}
	if h.cfg != nil {
		keys = append(keys, h.cfg.AuditSigningKey)
		keys = append(keys, h.cfg.AuditPreviousSigningKeys...)
	}
	for i := range events {
		events[i].Integrity = mutationAuditIntegrityWithKeys(keys, events[i])
	}
	eventIDs := make([]string, 0, len(events))
	for _, event := range events {
		eventIDs = append(eventIDs, event.ID)
	}
	incidents, err := h.db.ListMutationAuditIncidents(r.Context(), eventIDs)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "mutation_audit_incident_read_failed", "failed to read audit incidents")
		return
	}
	for i := range events {
		if incident, ok := incidents[events[i].ID]; ok {
			incident.Integrity = mutationAuditIncidentIntegrityWithKeys(keys, incident)
			events[i].Incident = &incident
			if events[i].Integrity == "invalid" && incident.Integrity == "verified" {
				events[i].Integrity = "acknowledged-invalid"
			}
		}
	}
	preventSensitiveResponseCaching(w)
	writeJSON(w, map[string]any{"schema": mutationAuditSchema, "events": events, "count": len(events)})
}

type mutationAuditIncidentRequest struct {
	ReasonCode  string `json:"reasonCode"`
	Explanation string `json:"explanation"`
}

func (h *Handler) AcknowledgeMutationAuditIncident(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireControlScope(w, r, ScopeAdmin)
	if !ok {
		return
	}
	if h.db == nil || h.db.Pool == nil || h.cfg == nil || len(h.cfg.AuditSigningKey) < 32 {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "mutation_audit_unavailable", "signed durable mutation audit is unavailable")
		return
	}
	eventID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, err := uuid.Parse(eventID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_audit_event_id", "audit event id must be a UUID")
		return
	}
	var request mutationAuditIncidentRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_audit_incident", err.Error())
		return
	}
	request.ReasonCode = strings.TrimSpace(request.ReasonCode)
	request.Explanation = strings.TrimSpace(request.Explanation)
	if !mutationAuditIncidentReasons[request.ReasonCode] {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_audit_incident_reason", "reasonCode is not supported")
		return
	}
	if len(request.Explanation) < 12 || len(request.Explanation) > 1024 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_audit_incident_explanation", "explanation must contain 12 to 1024 characters")
		return
	}
	event, err := h.db.GetMutationAudit(r.Context(), eventID)
	if err != nil {
		if err == pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusNotFound, "audit_event_not_found", "mutation audit event was not found")
		} else {
			WriteControlProblem(w, r, http.StatusInternalServerError, "mutation_audit_read_failed", "failed to read mutation audit event")
		}
		return
	}
	keys := append([]string{h.cfg.AuditSigningKey}, h.cfg.AuditPreviousSigningKeys...)
	if mutationAuditIntegrityWithKeys(keys, *event) != "invalid" {
		WriteControlProblem(w, r, http.StatusConflict, "audit_incident_not_applicable", "only an invalid completed mutation receipt can be acknowledged")
		return
	}
	acknowledgedBy := strings.TrimSpace(principal.Subject)
	if acknowledgedBy == "" {
		acknowledgedBy = "authenticated"
	}
	incident := store.MutationAuditIncident{
		ID: uuid.NewString(), AuditEventID: eventID, ReasonCode: request.ReasonCode,
		Explanation: request.Explanation, AcknowledgedBy: truncateAuditValue(acknowledgedBy, 256),
		AcknowledgedAt: time.Now().UTC().Truncate(time.Microsecond), KeyID: auditKeyID(h.cfg.AuditSigningKey),
	}
	incident.RecordDigest = signMutationAuditIncident(h.cfg.AuditSigningKey, incident)
	if err := h.db.InsertMutationAuditIncident(r.Context(), &incident); err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			WriteControlProblem(w, r, http.StatusConflict, "audit_incident_exists", "this mutation audit event already has an incident acknowledgement")
		} else {
			WriteControlProblem(w, r, http.StatusInternalServerError, "audit_incident_write_failed", "failed to persist audit incident")
		}
		return
	}
	incident.Integrity = "verified"
	preventSensitiveResponseCaching(w)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, incident)
}

func isAuditedMutation(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		path := r.URL.Path
		if path == "/api/access/observations" || path == "/api/access/cloudflare/logpush" {
			return false
		}
		return strings.HasPrefix(path, "/api/")
	default:
		return false
	}
}

func mutationAuditPath(r *http.Request) string {
	if route := strings.TrimSpace(chi.RouteContext(r.Context()).RoutePattern()); route != "" {
		return truncateAuditValue(route, 512)
	}
	path := r.URL.Path
	if marker := strings.Index(path, "/secrets/"); marker >= 0 {
		path = path[:marker] + "/secrets/{key}"
	}
	return truncateAuditValue(path, 512)
}

func mutationOutcome(status int) string {
	switch {
	case status >= 500:
		return "failed"
	case status >= 400:
		return "rejected"
	default:
		return "succeeded"
	}
}

func truncateAuditValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func signMutationAudit(key string, event store.MutationAuditEvent) string {
	if len(key) < 32 || event.FinishedAt == nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(mutationAuditCanonical(event)))
	return hex.EncodeToString(mac.Sum(nil))
}

func mutationAuditIntegrity(key string, event store.MutationAuditEvent) string {
	if event.Outcome == "started" || event.FinishedAt == nil {
		return "pending"
	}
	if event.RecordDigest == "" {
		return "unsigned"
	}
	if event.KeyID != "" && auditKeyID(key) != event.KeyID {
		return "invalid"
	}
	expected := signMutationAudit(key, event)
	if expected != "" && hmac.Equal([]byte(expected), []byte(event.RecordDigest)) {
		return "verified"
	}
	return "invalid"
}

func mutationAuditIntegrityWithKeys(keys []string, event store.MutationAuditEvent) string {
	if event.Outcome == "started" || event.FinishedAt == nil {
		return "pending"
	}
	if event.RecordDigest == "" {
		return "unsigned"
	}
	for _, key := range keys {
		if len(key) >= 32 && mutationAuditIntegrity(key, event) == "verified" {
			return "verified"
		}
	}
	return "invalid"
}

func auditKeyID(key string) string {
	if len(key) < 32 {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:8])
}

func signMutationAuditIncident(key string, incident store.MutationAuditIncident) string {
	if len(key) < 32 {
		return ""
	}
	canonical := struct {
		Schema, ID, AuditEventID, ReasonCode, Explanation, AcknowledgedBy, AcknowledgedAt, KeyID string
	}{
		Schema: mutationAuditIncidentSchema, ID: incident.ID, AuditEventID: incident.AuditEventID,
		ReasonCode: incident.ReasonCode, Explanation: incident.Explanation, AcknowledgedBy: incident.AcknowledgedBy,
		AcknowledgedAt: canonicalAuditTime(incident.AcknowledgedAt), KeyID: incident.KeyID,
	}
	encoded, _ := json.Marshal(canonical)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func mutationAuditIncidentIntegrityWithKeys(keys []string, incident store.MutationAuditIncident) string {
	if incident.RecordDigest == "" {
		return "unsigned"
	}
	for _, key := range keys {
		if len(key) < 32 || (incident.KeyID != "" && auditKeyID(key) != incident.KeyID) {
			continue
		}
		expected := signMutationAuditIncident(key, incident)
		if hmac.Equal([]byte(expected), []byte(incident.RecordDigest)) {
			return "verified"
		}
	}
	return "invalid"
}

func mutationAuditCanonical(event store.MutationAuditEvent) string {
	finished := ""
	if event.FinishedAt != nil {
		finished = canonicalAuditTime(*event.FinishedAt)
	}
	canonical := struct {
		Schema, ID, RequestID, PrincipalSubject, TokenID, DeviceID, KeyID string
		Scopes                                                            []string
		Method, Path, ClientIP, UserAgent, StartedAt, FinishedAt          string
		Status                                                            int
		Outcome                                                           string
		DurationMs                                                        int64
	}{
		Schema: mutationAuditSchema, ID: event.ID, RequestID: event.RequestID,
		PrincipalSubject: event.PrincipalSubject, TokenID: event.TokenID, DeviceID: event.DeviceID, KeyID: event.KeyID,
		Scopes: event.Scopes, Method: event.Method, Path: event.Path, ClientIP: event.ClientIP, UserAgent: event.UserAgent,
		StartedAt: canonicalAuditTime(event.StartedAt), FinishedAt: finished,
		Status: event.Status, Outcome: event.Outcome, DurationMs: event.DurationMs,
	}
	encoded, _ := json.Marshal(canonical)
	return string(encoded)
}

// PostgreSQL timestamptz stores microsecond precision. Canonicalize to the same
// precision before signing so a receipt still verifies after a database
// round-trip instead of signing nanoseconds that PostgreSQL cannot preserve.
func canonicalAuditTime(value time.Time) string {
	return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}
