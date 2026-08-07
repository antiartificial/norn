package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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
}

type client struct {
	conn *websocket.Conn
	send chan []byte
}

type registration struct {
	client *client
	after  int64
	replay bool
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
	deliveredIDs   map[int64]struct{}
}

func New(allowedOrigins []string) *Hub {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return &Hub{
		clients:      make(map[*client]bool),
		broadcast:    make(chan Event, 256),
		register:     make(chan registration),
		unregister:   make(chan *client),
		deliveredIDs: make(map[int64]struct{}),
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
			c := registration.client
			if registration.replay && h.store != nil {
				events, err := h.store.ListHubEventsAfter(context.Background(), registration.after, 500)
				if err != nil {
					log.Printf("hub: replay after %d: %v", registration.after, err)
				} else {
					for _, event := range events {
						data, marshalErr := json.Marshal(event)
						if marshalErr != nil {
							continue
						}
						c.send <- data
					}
				}
			}
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case event := <-h.broadcast:
			if event.Timestamp.IsZero() {
				event.Timestamp = time.Now().UTC()
			}
			if h.store != nil {
				if err := h.store.AppendHubEvent(context.Background(), &event); err != nil {
					log.Printf("hub: persist event: %v", err)
				} else if event.ID > 0 {
					h.deliveredIDs[event.ID] = struct{}{}
				}
			}
			h.deliver(event)
		case <-externalPoll.C:
			h.pollExternalEvents()
		}
	}
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
		if _, delivered := h.deliveredIDs[event.ID]; delivered {
			delete(h.deliveredIDs, event.ID)
			continue
		}
		h.deliver(event)
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
			http.Error(w, "invalid event cursor", http.StatusBadRequest)
			return
		}
		after = parsed
		replay = true
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	c := &client{conn: conn, send: make(chan []byte, 64)}
	go c.writePump()
	h.register <- registration{client: c, after: after, replay: replay}
	go c.readPump(h)
}

func (c *client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
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
