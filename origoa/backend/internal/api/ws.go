package api

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
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
	// corsOrigin, when set, is an additional cross-origin allowed to open a
	// session (mirrors the REST CORS setting for the split-origin dev setup).
	corsOrigin string
}

type client struct {
	conn    *websocket.Conn
	send    chan []byte
	user    string
	viewing string // GUID currently opened by this user
	editing bool
}

// NewHub creates the session hub. corsOrigin is the extra cross-origin (if
// any) allowed to connect, matching the REST CORS configuration.
func NewHub(corsOrigin string) *Hub {
	return &Hub{clients: map[*client]bool{}, corsOrigin: corsOrigin}
}

// checkOrigin rejects cross-site WebSocket connections (CSWSH): a browser
// request with an Origin header is accepted only when it is same-origin, or
// matches the explicitly configured cross-origin. Non-browser clients (no
// Origin header) are allowed.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if h.corsOrigin == "*" || (h.corsOrigin != "" && origin == h.corsOrigin) {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// HandleWS upgrades the connection and joins the session service.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
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
