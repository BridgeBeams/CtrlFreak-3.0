package host

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// sigConn is the host's WebSocket connection to the relay. It is used only for
// registration and WebRTC negotiation; no media flows here.
type sigConn struct {
	conn *websocket.Conn
	mu   sync.Mutex // serialize writes
}

// dialSignaling connects, sends the hello, and waits for the welcome (which
// carries the assigned id and ICE servers).
func dialSignaling(relayURL, token, name, platform string) (*sigConn, protocol.Signal, error) {
	// Accept self-signed relay certs for first-light; a production relay should
	// use a real cert and this can be tightened.
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 15 * time.Second
	dialer.TLSClientConfig = insecureTLS()

	conn, _, err := dialer.Dial(relayURL, nil)
	if err != nil {
		return nil, protocol.Signal{}, err
	}
	sc := &sigConn{conn: conn}

	if err := sc.write(protocol.Signal{
		Type: protocol.SigHello, Token: token, Role: protocol.RoleHost,
		DeviceName: name, Platform: platform,
	}); err != nil {
		conn.Close()
		return nil, protocol.Signal{}, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	welcome, err := sc.read()
	if err != nil {
		conn.Close()
		return nil, protocol.Signal{}, err
	}
	if welcome.Type == protocol.SigError {
		conn.Close()
		return nil, protocol.Signal{}, errors.New(welcome.Message)
	}
	if welcome.Type != protocol.SigWelcome {
		conn.Close()
		return nil, protocol.Signal{}, errors.New("unexpected first message from relay")
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Keepalive pings.
	go sc.pinger()
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	return sc, welcome, nil
}

func (s *sigConn) read() (protocol.Signal, error) {
	var msg protocol.Signal
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return msg, err
	}
	err = json.Unmarshal(data, &msg)
	return msg, err
}

func (s *sigConn) write(msg protocol.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.conn.WriteJSON(msg)
}

func (s *sigConn) pinger() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := s.conn.WriteMessage(websocket.PingMessage, nil)
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}
