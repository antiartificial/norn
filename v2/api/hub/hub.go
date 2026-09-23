package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var errReplayPageExceeded = errors.New("event replay exceeds one page")

type Event struct {
	ID        int64       `json:"id,omitempty"`
	Timestamp time.Time   `json:"timestamp,omitempty"`
	Type      string      `json:"type"`
	AppID     string      `json:"appId,omitempty"`
	Payload   interface{} `json:"payload"`
}

type EventStore interface {
	AppendHubEvent(context.Context, *Event) error
	ListHubEventsAfter(context.Context, int64, int) ([]Event, error)
	LatestHubEventID(context.Context) (int64, error)
	HubEventBounds(context.Context) (EventBounds, error)
}

// ReplayEventStore snapshots the compaction watermark and replay page
// together. Implementations must never return a truncated page as valid.
type ReplayEventStore interface {
	ReplayHubEvents(context.Context, int64, int) ([]Event, EventBounds, ResyncDecision, error)
}

type ReplayCursorExpiredError struct {
	Bounds   EventBounds
	Decision ResyncDecision
}

func (e *ReplayCursorExpiredError) Error() string { return "event cursor expired" }

type EventBounds struct {
	OldestCursor        int64      `json:"oldestCursor"`
	LatestCursor        int64      `json:"latestCursor"`
	PrunedThroughCursor int64      `json:"prunedThroughCursor"`
	RetainedEvents      int64      `json:"retainedEvents"`
	OldestTimestamp     *time.Time `json:"oldestTimestamp,omitempty"`
	LatestTimestamp     *time.Time `json:"latestTimestamp,omitempty"`
}

type StreamInfo struct {
	ProtocolVersion  int            `json:"protocolVersion"`
	Bounds           EventBounds    `json:"bounds"`
	RetentionPolicy  string         `json:"retentionPolicy"`
	Retention        EventRetention `json:"retention"`
	GapDetection     bool           `json:"gapDetection"`
	HeartbeatMinimum int            `json:"heartbeatMinimumSeconds"`
	HeartbeatMaximum int            `json:"heartbeatMaximumSeconds"`
	Filters          []string       `json:"filters"`
}

type EventRetention struct {
	Mode             string `json:"mode"`
	AutomaticPruning bool   `json:"automaticPruning"`
	ReplayPageSize   int    `json:"replayPageSize"`
}

type eventFilter struct {
	types map[string]bool
	apps  map[string]bool
}

func (f eventFilter) allows(event Event) bool {
	if len(f.types) > 0 && !f.types[event.Type] {
		return false
	}
	return len(f.apps) == 0 || f.apps[event.AppID]
}

type client struct {
	conn      *websocket.Conn
	send      chan []byte
	filter    eventFilter
	heartbeat time.Duration
	cursor    int64
}

type registration struct {
	client *client
	after  int64
	replay bool
	ready  chan error
}

type Hub struct {
	mu             sync.RWMutex
	clients        map[*client]bool
	broadcast      chan Event
	register       chan registration
	unregister     chan *client
	upgrader       websocket.Upgrader
	store          EventStore
	externalCursor int64
}

func New(allowedOrigins []string) *Hub {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return &Hub{
		clients:    make(map[*client]bool),
		broadcast:  make(chan Event, 256),
		register:   make(chan registration),
		unregister: make(chan *client),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return OriginAllowed(r, allowed) },
		},
	}
}

func (h *Hub) SetStore(store EventStore) {
	h.store = store
	if store != nil {
		if cursor, err := store.LatestHubEventID(context.Background()); err == nil {
			h.externalCursor = cursor
		} else {
			log.Printf("hub: initialize event cursor: %v", err)
		}
	}
}

func OriginAllowed(r *http.Request, allowed map[string]bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if allowed[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (h *Hub) Run() {
	externalPoll := time.NewTicker(time.Second)
	defer externalPoll.Stop()
	for {
		select {
		case registration := <-h.register:
			registration.ready <- h.registerClient(registration)
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case event := <-h.broadcast:
			h.persistAndDeliver(event)
		case <-externalPoll.C:
			h.pollExternalEvents()
		}
	}
}

func (h *Hub) persistAndDeliver(event Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if h.store == nil {
		h.deliver(event)
		return
	}
	if err := h.store.AppendHubEvent(context.Background(), &event); err != nil {
		log.Printf("hub: persist event: %v", err)
		return
	}
	// The append protocol serializes ID allocation through commit. Drain from
	// the last contiguous cursor so externally committed rows always precede
	// this local row on every replica.
	h.pollExternalEvents()
}

// registerClient establishes the client's durable stream boundary before the
// WebSocket upgrade is exposed to the peer. The Hub event loop calls this
// synchronously, so a local broadcast is either behind the captured cursor and
// excluded or is processed after the client has joined the live set.
func (h *Hub) registerClient(registration registration) error {
	c := registration.client
	c.cursor = registration.after
	if h.store != nil {
		if registration.replay {
			var events []Event
			var err error
			if replayStore, ok := h.store.(ReplayEventStore); ok {
				var bounds EventBounds
				var decision ResyncDecision
				events, bounds, decision, err = replayStore.ReplayHubEvents(context.Background(), registration.after, 501)
				if err == nil && !decision.Replayable {
					return &ReplayCursorExpiredError{Bounds: bounds, Decision: decision}
				}
			} else {
				events, err = h.store.ListHubEventsAfter(context.Background(), registration.after, 501)
			}
			if err != nil {
				return fmt.Errorf("replay after %d: %w", registration.after, err)
			}
			if len(events) > 500 {
				return errReplayPageExceeded
			}
			for _, event := range events {
				if event.ID > c.cursor {
					c.cursor = event.ID
				}
				if !c.filter.allows(event) {
					continue
				}
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					continue
				}
				select {
				case c.send <- data:
				default:
					return fmt.Errorf("replay exceeds client buffer")
				}
			}
		} else {
			cursor, err := h.store.LatestHubEventID(context.Background())
			if err != nil {
				return fmt.Errorf("capture live cursor: %w", err)
			}
			c.cursor = cursor
		}
	}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	return nil
}

func (h *Hub) pollExternalEvents() {
	if h.store == nil {
		return
	}
	events, err := h.store.ListHubEventsAfter(context.Background(), h.externalCursor, 500)
	if err != nil {
		log.Printf("hub: poll external events: %v", err)
		return
	}
	for _, event := range events {
		if event.ID > h.externalCursor {
			h.externalCursor = event.ID
		}
		h.deliverExternal(event)
	}
}

func (h *Hub) deliver(event Event) {
	msg, err := json.Marshal(event)
	if err != nil {
		log.Printf("hub: marshal error: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.filter.allows(event) {
			continue
		}
		select {
		case c.send <- msg:
		default:
			close(c.send)
			delete(h.clients, c)
		}
	}
}

// deliverExternal advances the contiguous durable cursor observed through the
// store poller. Local broadcasts may be assigned a newer database ID before an
// older external event is polled, so they must not advance this watermark.
func (h *Hub) deliverExternal(event Event) {
	msg, err := json.Marshal(event)
	if err != nil {
		log.Printf("hub: marshal external event: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if event.ID > 0 {
			if event.ID <= c.cursor {
				continue
			}
			c.cursor = event.ID
		}
		if !c.filter.allows(event) {
			continue
		}
		select {
		case c.send <- msg:
		default:
			close(c.send)
			delete(h.clients, c)
		}
	}
}

func (h *Hub) Broadcast(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	h.broadcast <- evt
}

func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	replay := false
	rawAfter := r.URL.Query().Get("after")
	if rawAfter == "" {
		rawAfter = r.URL.Query().Get("cursor")
	}
	if rawAfter != "" {
		parsed, err := strconv.ParseInt(rawAfter, 10, 64)
		if err != nil || parsed < 0 {
			writeStreamProblem(w, http.StatusBadRequest, "invalid_event_cursor", "event cursor must be a non-negative integer", nil, nil)
			return
		}
		after = parsed
		replay = true
	}
	filter, err := parseEventFilter(r)
	if err != nil {
		writeStreamProblem(w, http.StatusBadRequest, "invalid_event_filter", err.Error(), nil, nil)
		return
	}
	heartbeat, err := parseHeartbeat(r.URL.Query().Get("heartbeat"))
	if err != nil {
		writeStreamProblem(w, http.StatusBadRequest, "invalid_heartbeat", err.Error(), nil, nil)
		return
	}
	if replay && h.store != nil {
		bounds, boundsErr := h.store.HubEventBounds(r.Context())
		if boundsErr != nil {
			writeStreamProblem(w, http.StatusServiceUnavailable, "event_store_unavailable", "event metadata is unavailable", nil, nil)
			return
		}
		if after > bounds.LatestCursor && bounds.LatestCursor > 0 {
			writeStreamProblem(w, http.StatusConflict, "event_cursor_ahead", "the requested cursor is newer than the event stream", &bounds, nil)
			return
		}
		decision := EvaluateReplay(bounds, after)
		if !decision.Replayable {
			writeStreamProblem(w, http.StatusConflict, "event_cursor_expired", "the requested cursor is older than retained event history", &bounds, &decision)
			return
		}
	}
	// One replay page fits without blocking the hub's single event loop. Slow
	// clients are disconnected by the non-blocking replay/broadcast paths.
	c := &client{send: make(chan []byte, 512), filter: filter, heartbeat: heartbeat}
	ready := make(chan error, 1)
	select {
	case h.register <- registration{client: c, after: after, replay: replay, ready: ready}:
	case <-r.Context().Done():
		return
	}
	if registerErr := <-ready; registerErr != nil {
		close(c.send)
		log.Printf("hub: register stream: %v", registerErr)
		if errors.Is(registerErr, errReplayPageExceeded) {
			writeStreamProblem(w, http.StatusConflict, "event_replay_too_large", "more than one replay page is pending; refresh authoritative state and reconnect from the current stream head", nil, nil)
			return
		}
		var expired *ReplayCursorExpiredError
		if errors.As(registerErr, &expired) {
			writeStreamProblem(w, http.StatusConflict, "event_cursor_expired", "the requested cursor is older than retained event history", &expired.Bounds, &expired.Decision)
			return
		}
		writeStreamProblem(w, http.StatusServiceUnavailable, "event_store_unavailable", "the event stream could not establish a durable cursor", nil, nil)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.unregister <- c
		log.Printf("ws upgrade: %v", err)
		return
	}
	c.conn = conn
	conn.SetReadLimit(64 << 10)

	go c.writePump()
	go c.readPump(h)
}

func (c *client) writePump() {
	defer c.conn.Close()
	var heartbeat <-chan time.Time
	var ticker *time.Ticker
	if c.heartbeat > 0 {
		ticker = time.NewTicker(c.heartbeat)
		heartbeat = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok || c.conn.WriteMessage(websocket.TextMessage, msg) != nil {
				return
			}
		case now := <-heartbeat:
			frame, _ := json.Marshal(map[string]interface{}{"frame": "heartbeat", "timestamp": now.UTC()})
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if c.conn.WriteMessage(websocket.TextMessage, frame) != nil {
				return
			}
		}
	}
}

func (h *Hub) HandleInfo(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeStreamProblem(w, http.StatusServiceUnavailable, "event_store_unavailable", "event persistence is not configured", nil, nil)
		return
	}
	bounds, err := h.store.HubEventBounds(r.Context())
	if err != nil {
		writeStreamProblem(w, http.StatusServiceUnavailable, "event_store_unavailable", "event metadata is unavailable", nil, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(StreamInfo{
		ProtocolVersion: 2, Bounds: bounds, RetentionPolicy: "bounded-with-resync",
		Retention:    EventRetention{Mode: "bounded", AutomaticPruning: false, ReplayPageSize: 500},
		GapDetection: true, HeartbeatMinimum: 10, HeartbeatMaximum: 120,
		Filters: []string{"types", "apps"},
	})
}

func parseEventFilter(r *http.Request) (eventFilter, error) {
	parse := func(raw string) (map[string]bool, error) {
		out := map[string]bool{}
		if strings.TrimSpace(raw) == "" {
			return out, nil
		}
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" || len(value) > 100 {
				return nil, fmt.Errorf("filters must be non-empty and at most 100 characters")
			}
			out[value] = true
			if len(out) > 50 {
				return nil, fmt.Errorf("no more than 50 filter values are allowed")
			}
		}
		return out, nil
	}
	types, err := parse(r.URL.Query().Get("types"))
	if err != nil {
		return eventFilter{}, err
	}
	apps, err := parse(r.URL.Query().Get("apps"))
	if err != nil {
		return eventFilter{}, err
	}
	return eventFilter{types: types, apps: apps}, nil
}

func parseHeartbeat(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 10 || seconds > 120 {
		return 0, fmt.Errorf("heartbeat must be between 10 and 120 seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}

func writeStreamProblem(w http.ResponseWriter, status int, code, detail string, bounds *EventBounds, resync *ResyncDecision) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	body := map[string]interface{}{
		"type": "https://norn.dev/problems/" + code, "title": http.StatusText(status),
		"status": status, "code": code, "detail": detail,
	}
	if bounds != nil {
		body["eventBounds"] = bounds
	}
	if resync != nil {
		body["resync"] = resync
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (c *client) readPump(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}
