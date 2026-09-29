package relay

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// client is one live signaling connection: a host waiting to be controlled, or
// a controller (laptop browser / phone). The relay's whole job on this channel
// is authentication, presence, and forwarding WebRTC setup messages between the
// two peers of a session. It never touches screen or input data.
type client struct {
	id       string
	username string
	isAdmin  bool
	role     protocol.Role
	name     string
	platform string

	conn *websocket.Conn
	send chan protocol.Signal
	hub  *Hub

	// For controllers: the set of session ids they currently have open, used to
	// enforce the per-controller cap.
	mu       sync.Mutex
	sessions map[string]bool
}

// Hub is the registry of connected clients and the router for signaling
// messages. One Hub per relay process.
type Hub struct {
	store *Store
	cfg   Config

	mu      sync.RWMutex
	clients map[string]*client // id -> client

	register   chan *client
	unregister chan *client
}

func NewHub(store *Store, cfg Config) *Hub {
	return &Hub{
		store:      store,
		cfg:        cfg,
		clients:    make(map[string]*client),
		register:   make(chan *client),
		unregister: make(chan *client),
	}
}

// Run is the hub's event loop.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c.id] = c
			h.mu.Unlock()
			if c.role == protocol.RoleHost {
				_ = h.store.RegisterHost(c.username, c.name, c.platform)
				h.store.Audit(c.username, "host_online", c.name, c.platform)
				h.broadcastDeviceEvent(c, true)
			}
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c.id]; ok {
				delete(h.clients, c.id)
				close(c.send)
			}
			h.mu.Unlock()
			if c.role == protocol.RoleHost {
				h.store.Audit(c.username, "host_offline", c.name, "")
				h.broadcastDeviceEvent(c, false)
			}
		}
	}
}

// onlineHostsFor returns the hosts a given user may currently reach: their own
// online hosts, plus every online host if the user is an admin.
func (h *Hub) onlineHostsFor(u *Claims) []protocol.DeviceInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []protocol.DeviceInfo
	for _, c := range h.clients {
		if c.role != protocol.RoleHost {
			continue
		}
		if !u.IsAdmin && c.username != u.Username {
			continue
		}
		out = append(out, protocol.DeviceInfo{
			ID:       c.id,
			Name:     c.name,
			Owner:    c.username,
			Platform: c.platform,
			Online:   true,
		})
	}
	return out
}

// broadcastDeviceEvent tells controllers that a host came online or went
// offline, respecting the same visibility rule (owners and admins only).
func (h *Hub) broadcastDeviceEvent(host *client, online bool) {
	dev := protocol.DeviceInfo{
		ID: host.id, Name: host.name, Owner: host.username,
		Platform: host.platform, Online: online,
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		if c.role != protocol.RoleController {
			continue
		}
		if !c.isAdmin && c.username != host.username {
			continue
		}
		select {
		case c.send <- protocol.Signal{Type: protocol.SigDeviceEvent, Device: &dev, Online: online}:
		default:
		}
	}
}

// route handles one inbound signaling message from a client.
func (h *Hub) route(c *client, msg protocol.Signal) {
	switch msg.Type {
	case protocol.SigConnect:
		h.handleConnect(c, msg)
	case protocol.SigCommand:
		h.handleCommand(c, msg)
	case protocol.SigOffer, protocol.SigAnswer, protocol.SigCandidate, protocol.SigBye:
		h.forward(c, msg)
	default:
		// Ignore unknown types rather than dropping the connection.
	}
}

// handleCommand relays a host command (reboot / restart_agent) from a controller
// to a host, after the same authorization check used for sessions. It travels
// the signaling link, so it reaches the host even when a WebRTC session is dead.
func (h *Hub) handleCommand(c *client, msg protocol.Signal) {
	if c.role != protocol.RoleController {
		return
	}
	if msg.Command != protocol.CmdReboot && msg.Command != protocol.CmdRestartAgent {
		c.trySend(protocol.Signal{Type: protocol.SigError, Message: "unknown command"})
		return
	}
	h.mu.RLock()
	target, ok := h.clients[msg.HostID]
	h.mu.RUnlock()
	if !ok || target.role != protocol.RoleHost {
		c.trySend(protocol.Signal{Type: protocol.SigError, Message: "host is not online"})
		return
	}
	if !c.isAdmin && target.username != c.username {
		h.store.Audit(c.username, "command_denied", target.name, msg.Command)
		c.trySend(protocol.Signal{Type: protocol.SigError, Message: "not authorized for this host"})
		return
	}
	detail := msg.Command
	if c.isAdmin && target.username != c.username {
		detail += " (admin cross-user)"
	}
	h.store.Audit(c.username, "command", target.name, detail)
	target.trySend(protocol.Signal{Type: protocol.SigCommand, Command: msg.Command})
}

// handleConnect validates that a controller may reach the requested host, then
// relays the connect request to that host so it can create a WebRTC offer.
func (h *Hub) handleConnect(c *client, msg protocol.Signal) {
	if c.role != protocol.RoleController {
		return
	}
	h.mu.RLock()
	target, ok := h.clients[msg.HostID]
	h.mu.RUnlock()
	if !ok || target.role != protocol.RoleHost {
		c.trySend(protocol.Signal{Type: protocol.SigError, SessionID: msg.SessionID,
			Message: "host is not online"})
		return
	}
	// Authorization: a controller may reach a host it owns, or any host if the
	// controller is an admin. This is the single gate that replaces the "silent
	// admin takeover" idea with an authorized, logged one.
	if !c.isAdmin && target.username != c.username {
		c.trySend(protocol.Signal{Type: protocol.SigError, SessionID: msg.SessionID,
			Message: "not authorized for this host"})
		h.store.Audit(c.username, "connect_denied", target.name, "not owner/admin")
		return
	}

	// Enforce the per-controller simultaneous-session cap.
	c.mu.Lock()
	if len(c.sessions) >= h.cfg.MaxSessionsPerController {
		c.mu.Unlock()
		c.trySend(protocol.Signal{Type: protocol.SigError, SessionID: msg.SessionID,
			Message: "session limit reached"})
		return
	}
	c.sessions[msg.SessionID] = true
	c.mu.Unlock()

	detail := "controller=" + c.username
	if c.isAdmin && target.username != c.username {
		detail += " (admin cross-user)"
	}
	h.store.Audit(c.username, "session_start", target.name, detail)

	// Ask the host to begin negotiation. We tag the message with the
	// controller's id so the host knows who to answer.
	target.trySend(protocol.Signal{
		Type:      protocol.SigConnect,
		SessionID: msg.SessionID,
		HostID:    c.id, // reuse HostID field to carry the controller's id back
	})
}

// forward relays a negotiation message (offer/answer/candidate/bye) to the
// other peer in the same session. The peer id is carried in HostID.
func (h *Hub) forward(c *client, msg protocol.Signal) {
	h.mu.RLock()
	target, ok := h.clients[msg.HostID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	// Stamp the sender's id so the receiver can reply.
	msg.HostID = c.id
	target.trySend(msg)

	if msg.Type == protocol.SigBye {
		if c.role == protocol.RoleController {
			c.mu.Lock()
			delete(c.sessions, msg.SessionID)
			c.mu.Unlock()
		}
		h.store.Audit(c.username, "session_end", "", msg.SessionID)
	}
}

func (c *client) trySend(msg protocol.Signal) {
	select {
	case c.send <- msg:
	default:
		log.Printf("dropped signal to %s (buffer full)", c.id)
	}
}

// readPump reads signaling messages from the socket and routes them.
func (c *client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(64 * 1024) // signaling messages are tiny
	_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg protocol.Signal
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		c.hub.route(c, msg)
	}
}

// writePump writes queued signaling messages and periodic pings.
func (c *client) writePump() {
	ping := time.NewTicker(30 * time.Second)
	defer func() {
		ping.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteJSON(msg); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func newClientID() string { return uuid.NewString() }
