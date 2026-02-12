package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

type Event struct {
	Type    string      `json:"type"`    // app.status, deploy.step, deploy.log, pod.event
	AppID   string      `json:"appId"`
	Payload interface{} `json:"payload"`
}

type client struct {
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]bool
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
	upgrader   websocket.Upgrader
}

func New(allowedOrigins []string) *Hub {
	// Build a set of allowed origin strings for fast lookup.
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return &Hub{
		clients:    make(map[*client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *client),
		unregister: make(chan *client),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true // non-browser clients (CLI, curl)
				}
				// Check against configured origins list.
				if allowed[origin] {
					return true
				}
				// Always allow localhost.
				u, err := url.Parse(origin)
				if err != nil {
					return false
				}
				host := u.Hostname()
				return host == "localhost" || host == "127.0.0.1" || host == "::1"
			},
		},
	}
}

func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
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
		case msg := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					close(c.send)
					delete(h.clients, c)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Broadcast(evt Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("hub: marshal error: %v", err)
		return
	}
	h.broadcast <- data
}

func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	c := &client{conn: conn, send: make(chan []byte, 64)}
	h.register <- c

	go c.writePump()
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
