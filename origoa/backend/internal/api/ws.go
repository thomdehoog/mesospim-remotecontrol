package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"origoa/internal/repo"
)

// Hub implements the WebSocket session service. It carries transient
// runtime information only — presence (who is viewing or editing which
// artifact), repository events, workflow transitions, maintenance mode
// and indexing progress. CRUD never happens over the WebSocket.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]bool
}

type client struct {
	conn    *websocket.Conn
	send    chan []byte
	user    string
	viewing string // GUID currently opened by this user
	editing bool
}

// NewHub creates the session hub.
func NewHub() *Hub {
	return &Hub{clients: map[*client]bool{}}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// The API is same-origin in production and CORS-open in development.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// HandleWS upgrades the connection and joins the session service.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	user := r.URL.Query().Get("user")
	if user == "" {
		user = "anonymous"
	}
	c := &client{conn: conn, send: make(chan []byte, 64), user: user}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()

	go c.writer()
	h.broadcastPresence()

	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		close(c.send)
		conn.Close()
		h.broadcastPresence()
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Type    string `json:"type"`
			GUID    string `json:"guid"`
			Editing bool   `json:"editing"`
		}
		if err := json.Unmarshal(msg, &m); err != nil {
			continue
		}
		switch m.Type {
		case "viewing":
			h.mu.Lock()
			c.viewing = m.GUID
			c.editing = m.Editing
			h.mu.Unlock()
			h.broadcastPresence()
		}
	}
}

func (c *client) writer() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// PresenceEntry describes one connected user.
type PresenceEntry struct {
	User    string `json:"user"`
	Viewing string `json:"viewing,omitempty"`
	Editing bool   `json:"editing,omitempty"`
}

func (h *Hub) broadcastPresence() {
	h.mu.Lock()
	entries := make([]PresenceEntry, 0, len(h.clients))
	for c := range h.clients {
		entries = append(entries, PresenceEntry{User: c.user, Viewing: c.viewing, Editing: c.editing})
	}
	h.mu.Unlock()
	h.broadcast(map[string]any{"type": "presence", "users": entries})
}

// BroadcastEvent distributes a repository event to all clients.
func (h *Hub) BroadcastEvent(e repo.Event) {
	h.broadcast(map[string]any{
		"type": "event", "event": e,
	})
}

func (h *Hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws: marshal: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default: // slow client: drop the message rather than block
		}
	}
}
