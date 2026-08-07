package hub

import (
	"context"
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
