package handler

import (
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAccessLogLimit = 500

type AccessEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMs int64     `json:"durationMs"`
	ClientIP   string    `json:"clientIp,omitempty"`
	Forwarded  string    `json:"forwarded,omitempty"`
	CFIP       string    `json:"cfConnectingIp,omitempty"`
	CFEmail    string    `json:"cfAccessEmail,omitempty"`
	UserAgent  string    `json:"userAgent,omitempty"`
}

type AccessLog struct {
	mu     sync.Mutex
	limit  int
	events []AccessEvent
	next   int
	full   bool
}

func NewAccessLog(limit int) *AccessLog {
	if limit <= 0 {
		limit = defaultAccessLogLimit
	}
	return &AccessLog{limit: limit, events: make([]AccessEvent, limit)}
}

func (l *AccessLog) Record(event AccessEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events[l.next] = event
	l.next = (l.next + 1) % l.limit
	if l.next == 0 {
		l.full = true
	}
}

func (l *AccessLog) Recent(limit int) []AccessEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > l.limit {
		limit = l.limit
	}
	count := l.next
	if l.full {
		count = l.limit
	}
	events := make([]AccessEvent, 0, count)
	if l.full {
		events = append(events, l.events[l.next:]...)
		events = append(events, l.events[:l.next]...)
	} else {
		events = append(events, l.events[:l.next]...)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.After(events[j].Timestamp)
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events
}

func (h *Handler) AccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" || r.URL.Path == "/metrics" || r.URL.Path == "/api/metrics" || strings.HasSuffix(r.URL.Path, "/exec") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if h.access == nil {
			return
		}
		h.access.Record(AccessEvent{
			Timestamp:  start.UTC(),
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     rw.status,
			DurationMs: time.Since(start).Milliseconds(),
			ClientIP:   clientIP(r),
			Forwarded:  firstForwardedFor(r.Header.Get("X-Forwarded-For")),
			CFIP:       strings.TrimSpace(r.Header.Get("CF-Connecting-IP")),
			CFEmail:    strings.TrimSpace(r.Header.Get("Cf-Access-Authenticated-User-Email")),
			UserAgent:  r.UserAgent(),
		})
	})
}

func (h *Handler) AccessEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if h.access == nil {
		writeJSON(w, []AccessEvent{})
		return
	}
	writeJSON(w, h.access.Recent(limit))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func clientIP(r *http.Request) string {
	if cfIP := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cfIP != "" {
		return cfIP
	}
	if forwarded := firstForwardedFor(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstForwardedFor(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ",")
	return strings.TrimSpace(parts[0])
}
