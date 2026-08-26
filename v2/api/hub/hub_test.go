package hub

import (
	"context"
	"encoding/json"
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
