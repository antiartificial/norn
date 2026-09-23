package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type memoryEventStore struct {
	mu     sync.Mutex
	events []Event
}

type registrationGateStore struct {
	*memoryEventStore
	mu            sync.Mutex
	latestCalls   int
	registration  chan struct{}
	releaseCursor chan struct{}
}

type appendFailureStore struct{ *memoryEventStore }

func (s *appendFailureStore) AppendHubEvent(_ context.Context, event *Event) error {
	event.ID = 99
	return errors.New("append unavailable")
}

type transientListFailureStore struct {
	*memoryEventStore
	mu       sync.Mutex
	failures int
}

func (s *transientListFailureStore) ListHubEventsAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return nil, errors.New("list unavailable")
	}
	s.mu.Unlock()
	return s.memoryEventStore.ListHubEventsAfter(ctx, after, limit)
}

func (s *registrationGateStore) LatestHubEventID(ctx context.Context) (int64, error) {
	s.mu.Lock()
	s.latestCalls++
	call := s.latestCalls
	s.mu.Unlock()
	if call == 2 {
		close(s.registration)
		select {
		case <-s.releaseCursor:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return s.memoryEventStore.LatestHubEventID(ctx)
}

func (s *memoryEventStore) AppendHubEvent(_ context.Context, event *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.ID = int64(len(s.events) + 1)
	s.events = append(s.events, *event)
	return nil
}

func (s *memoryEventStore) ListHubEventsAfter(_ context.Context, after int64, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Event{}
	for _, event := range s.events {
		if event.ID > after {
			out = append(out, event)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *memoryEventStore) LatestHubEventID(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.events)), nil
}

func (s *memoryEventStore) HubEventBounds(_ context.Context) (EventBounds, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bounds := EventBounds{RetainedEvents: int64(len(s.events))}
	if len(s.events) > 0 {
		bounds.OldestCursor = s.events[0].ID
		bounds.LatestCursor = s.events[len(s.events)-1].ID
		oldest, latest := s.events[0].Timestamp, s.events[len(s.events)-1].Timestamp
		bounds.OldestTimestamp, bounds.LatestTimestamp = &oldest, &latest
	}
	return bounds, nil
}

func TestHubPersistsIDsAndReplaysAfterCursor(t *testing.T) {
	store := &memoryEventStore{events: []Event{{ID: 1, Timestamp: time.Now().UTC(), Type: "existing", Payload: map[string]string{"status": "complete"}}}}
	h := New(nil)
	h.SetStore(store)
	go h.Run()
	server := httptest.NewServer(http.HandlerFunc(h.HandleConnect))
	defer server.Close()

	connect := func(query string) *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + query
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}

	replay := connect("?after=0")
	defer replay.Close()
	_ = replay.SetReadDeadline(time.Now().Add(2 * time.Second))
	var existing Event
	if err := replay.ReadJSON(&existing); err != nil {
		t.Fatal(err)
	}
	if existing.ID != 1 || existing.Type != "existing" {
		t.Fatalf("replayed event = %#v", existing)
	}

	live := connect("")
	defer live.Close()
	h.Broadcast(Event{Type: "maintenance.started", Payload: map[string]string{"operationId": "op-1"}})
	_ = live.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event Event
	if err := live.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.ID != 2 || event.Timestamp.IsZero() || event.Type != "maintenance.started" {
		t.Fatalf("live event = %#v", event)
	}
}

func TestHubRegistersLiveClientBeforeUpgradeExposesConnection(t *testing.T) {
	store := &registrationGateStore{
		memoryEventStore: &memoryEventStore{},
		registration:     make(chan struct{}),
		releaseCursor:    make(chan struct{}),
	}
	h := New(nil)
	h.SetStore(store)
	go h.Run()
	server := httptest.NewServer(http.HandlerFunc(h.HandleConnect))
	defer server.Close()

	connected := make(chan *websocket.Conn, 1)
	connectErr := make(chan error, 1)
	go func() {
		url := "ws" + strings.TrimPrefix(server.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			connectErr <- err
			return
		}
		connected <- conn
	}()

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.releaseCursor) }) }
	defer release()
	select {
	case <-store.registration:
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not begin live cursor registration")
	}
	select {
	case conn := <-connected:
		conn.Close()
		t.Fatal("WebSocket upgrade completed before live registration established its cursor")
	case err := <-connectErr:
		t.Fatal(err)
	default:
	}
	h.Broadcast(Event{Type: "registration.race", Payload: map[string]string{"status": "durable"}})
	release()

	var conn *websocket.Conn
	select {
	case conn = <-connected:
	case err := <-connectErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket did not complete after registration cursor was released")
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "registration.race" || event.ID != 1 {
		t.Fatalf("event broadcast during registration = %#v", event)
	}
}

func TestExternalPollDoesNotDuplicateRegistrationReplay(t *testing.T) {
	store := &memoryEventStore{events: []Event{{ID: 1, Timestamp: time.Now().UTC(), Type: "external", AppID: "atlas"}}}
	h := New(nil)
	h.SetStore(store)
	h.externalCursor = 0
	c := &client{send: make(chan []byte, 2), filter: eventFilter{apps: map[string]bool{"atlas": true}}}
	if err := h.registerClient(registration{client: c, after: 0, replay: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.send:
	default:
		t.Fatal("registration replay was not queued")
	}
	h.pollExternalEvents()
	select {
	case duplicate := <-c.send:
		t.Fatalf("external poll duplicated replayed event: %s", duplicate)
	default:
	}
}

func TestPersistentBroadcastDrainsOlderExternalEventBeforeNewerLocalEvent(t *testing.T) {
	store := &memoryEventStore{events: []Event{{ID: 1, Timestamp: time.Now().UTC(), Type: "existing"}}}
	h := New(nil)
	h.SetStore(store)
	c := &client{send: make(chan []byte, 4), cursor: 1, filter: eventFilter{}}
	h.clients[c] = true

	external := Event{Timestamp: time.Now().UTC(), Type: "external"}
	if err := store.AppendHubEvent(context.Background(), &external); err != nil {
		t.Fatal(err)
	}
	h.persistAndDeliver(Event{Timestamp: time.Now().UTC(), Type: "local"})

	var first, second Event
	if err := json.Unmarshal(<-c.send, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(<-c.send, &second); err != nil {
		t.Fatal(err)
	}
	if first.ID != 2 || first.Type != "external" || second.ID != 3 || second.Type != "local" {
		t.Fatalf("durable delivery order first=%#v second=%#v", first, second)
	}
	select {
	case duplicate := <-c.send:
		t.Fatalf("durable drain duplicated an event: %s", duplicate)
	default:
	}
	events, err := store.ListHubEventsAfter(context.Background(), second.ID, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("reconnect after contiguous cursor returned events=%v err=%v", events, err)
	}
}

func TestPersistentBroadcastNeverPublishesFailedAppend(t *testing.T) {
	store := &appendFailureStore{memoryEventStore: &memoryEventStore{}}
	h := New(nil)
	h.SetStore(store)
	c := &client{send: make(chan []byte, 1)}
	h.clients[c] = true
	event := Event{Type: "undurable"}
	h.persistAndDeliver(event)
	select {
	case got := <-c.send:
		t.Fatalf("failed append was broadcast: %s", got)
	default:
	}
}

func TestPersistentBroadcastRetriesDrainAfterListFailure(t *testing.T) {
	store := &transientListFailureStore{memoryEventStore: &memoryEventStore{}, failures: 1}
	h := New(nil)
	h.SetStore(store)
	c := &client{send: make(chan []byte, 2)}
	h.clients[c] = true
	h.persistAndDeliver(Event{Type: "durable"})
	select {
	case got := <-c.send:
		t.Fatalf("event delivered despite failed ordered drain: %s", got)
	default:
	}
	h.pollExternalEvents()
	var event Event
	if err := json.Unmarshal(<-c.send, &event); err != nil {
		t.Fatal(err)
	}
	if event.ID != 1 || event.Type != "durable" {
		t.Fatalf("retried durable event = %#v", event)
	}
	select {
	case duplicate := <-c.send:
		t.Fatalf("retried drain duplicated event: %s", duplicate)
	default:
	}
}

func TestOriginAllowedRejectsUnlistedRemoteOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://norn.test/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	if OriginAllowed(req, nil) {
		t.Fatal("unexpected cross-origin WebSocket allowance")
	}
	req.Header.Set("Origin", "http://localhost:5173")
	if !OriginAllowed(req, nil) {
		t.Fatal("localhost development origin should be allowed")
	}
}

func TestEventStreamInfoReportsRetentionBounds(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryEventStore{events: []Event{{ID: 7, Timestamp: now, Type: "app.ready", AppID: "atlas"}}}
	h := New(nil)
	h.SetStore(store)
	rec := httptest.NewRecorder()
	h.HandleInfo(rec, httptest.NewRequest(http.MethodGet, "/api/v1/events/info", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var info StreamInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if !info.GapDetection || info.Bounds.OldestCursor != 7 || info.Bounds.LatestCursor != 7 || info.HeartbeatMinimum != 10 {
		t.Fatalf("info = %#v", info)
	}
}

func TestEventStreamRejectsCursorGapBeforeUpgrade(t *testing.T) {
	store := &memoryEventStore{events: []Event{
		{ID: 10, Timestamp: time.Now().UTC(), Type: "retained"},
		{ID: 11, Timestamp: time.Now().UTC(), Type: "retained"},
	}}
	h := New(nil)
	h.SetStore(store)
	server := httptest.NewServer(http.HandlerFunc(h.HandleConnect))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "?after=3"
	_, response, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil || response == nil {
		t.Fatal("expected WebSocket upgrade to be rejected")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var problem map[string]interface{}
	if json.Unmarshal(body, &problem) != nil || problem["code"] != "event_cursor_gap" {
		t.Fatalf("problem = %s", body)
	}
}

func TestEventStreamRejectsReplayLargerThanBoundedPage(t *testing.T) {
	events := make([]Event, 501)
	for index := range events {
		events[index] = Event{ID: int64(index + 1), Timestamp: time.Now().UTC(), Type: "retained"}
	}
	store := &memoryEventStore{events: events}
	h := New(nil)
	h.SetStore(store)
	go h.Run()
	server := httptest.NewServer(http.HandlerFunc(h.HandleConnect))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "?after=0"
	_, response, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil || response == nil {
		t.Fatal("expected oversized replay to be rejected before upgrade")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var problem map[string]interface{}
	if response.StatusCode != http.StatusConflict || json.Unmarshal(body, &problem) != nil || problem["code"] != "event_replay_too_large" {
		t.Fatalf("status=%d problem=%s", response.StatusCode, body)
	}
}

func TestEventStreamFiltersReplayByTypeAndApp(t *testing.T) {
	store := &memoryEventStore{events: []Event{
		{ID: 1, Timestamp: time.Now().UTC(), Type: "app.ready", AppID: "other"},
		{ID: 2, Timestamp: time.Now().UTC(), Type: "app.failed", AppID: "atlas"},
		{ID: 3, Timestamp: time.Now().UTC(), Type: "app.ready", AppID: "atlas"},
	}}
	h := New(nil)
	h.SetStore(store)
	go h.Run()
	server := httptest.NewServer(http.HandlerFunc(h.HandleConnect))
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "?after=0&types=app.ready&apps=atlas"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event Event
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.ID != 3 {
		t.Fatalf("filtered replay event = %#v", event)
	}
	h.mu.RLock()
	for client := range h.clients {
		if client.cursor != 3 {
			t.Fatalf("filtered replay cursor=%d, want contiguous cursor 3", client.cursor)
		}
	}
	h.mu.RUnlock()
}

func TestHeartbeatBounds(t *testing.T) {
	if _, err := parseHeartbeat("9"); err == nil {
		t.Fatal("heartbeat below minimum should fail")
	}
	if got, err := parseHeartbeat("10"); err != nil || got != 10*time.Second {
		t.Fatalf("heartbeat = %v, %v", got, err)
	}
	if _, err := parseHeartbeat("121"); err == nil {
		t.Fatal("heartbeat above maximum should fail")
	}
}
