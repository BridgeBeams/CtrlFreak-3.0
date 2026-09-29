package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// Server ties together the store, token auth, hub, and HTTP surface.
type Server struct {
	cfg   Config
	store *Store
	auth  *TokenAuth
	hub   *Hub
	web   fs.FS
}

// NewServer builds the relay. webFS carries the browser controller assets
// (embedded at build time by cmd/relay).
func NewServer(cfg Config, store *Store, webFS fs.FS) *Server {
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = randomSecret()
		log.Println("WARNING: no jwt_secret set; generated a random one (logins reset on restart)")
	}
	auth := NewTokenAuth([]byte(cfg.JWTSecret), cfg.tokenTTL())
	hub := NewHub(store, cfg)
	return &Server{cfg: cfg, store: store, auth: auth, hub: hub, web: webFS}
}

// Handler returns the full HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/change-password", s.handleChangePassword)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/ice", s.handleICE)
	mux.HandleFunc("/api/hosts", s.handleHosts)

	// Admin-only
	mux.HandleFunc("/api/admin/users", s.handleAdminUsers)         // GET list, POST create
	mux.HandleFunc("/api/admin/users/delete", s.handleAdminDelete) // POST
	mux.HandleFunc("/api/admin/users/reset", s.handleAdminReset)   // POST
	mux.HandleFunc("/api/admin/users/role", s.handleAdminRole)     // POST
	mux.HandleFunc("/api/admin/audit", s.handleAdminAudit)         // GET

	// Signaling WebSocket
	mux.HandleFunc("/ws", s.handleWS)

	// Static controller UI
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return mux
}

// Run starts the hub loop and the HTTPS listener.
func (s *Server) Run() error {
	go s.hub.Run()

	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:      s.cfg.Addr,
		Handler:   s.Handler(),
		TLSConfig: tlsCfg,
	}
	log.Printf("CtrlFreak relay listening on https://%s", s.cfg.Addr)
	// Certs are already in TLSConfig, so empty paths are correct here.
	return srv.ListenAndServeTLS("", "")
}

// ---------- auth endpoints ----------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type loginResp struct {
	Token      string `json:"token"`
	IsAdmin    bool   `json:"is_admin"`
	MustChange bool   `json:"must_change"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	u, err := s.store.Authenticate(req.Username, req.Password)
	if err != nil {
		s.store.Audit(req.Username, "login_fail", "", clientIP(r))
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	tok, err := s.auth.Issue(u.Username, u.IsAdmin)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.store.Audit(u.Username, "login_ok", "", clientIP(r))
	writeJSON(w, loginResp{Token: tok, IsAdmin: u.IsAdmin, MustChange: u.MustChange})
}

type changePwReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAuth(w, r)
	if claims == nil {
		return
	}
	var req changePwReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.store.Authenticate(claims.Username, req.OldPassword); err != nil {
		http.Error(w, "current password is wrong", http.StatusUnauthorized)
		return
	}
	if len(req.NewPassword) < 8 {
		http.Error(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if err := s.store.SetPassword(claims.Username, req.NewPassword, false); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.store.Audit(claims.Username, "password_change", claims.Username, "self")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAuth(w, r)
	if claims == nil {
		return
	}
	writeJSON(w, map[string]interface{}{
		"username":     claims.Username,
		"is_admin":     claims.IsAdmin,
		"max_sessions": s.cfg.MaxSessionsPerController,
	})
}

func (s *Server) handleICE(w http.ResponseWriter, r *http.Request) {
	if s.requireAuth(w, r) == nil {
		return
	}
	writeJSON(w, map[string]interface{}{"ice_servers": s.cfg.ICEServers})
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAuth(w, r)
	if claims == nil {
		return
	}
	// Registered hosts (may be offline). The signaling channel adds live
	// online/offline state on top of this.
	var hosts []Host
	var err error
	if claims.IsAdmin {
		// Admins see everyone's registered hosts.
		users, _ := s.store.ListUsers()
		for _, u := range users {
			hs, _ := s.store.ListHosts(u.Username)
			hosts = append(hosts, hs...)
		}
	} else {
		hosts, err = s.store.ListHosts(claims.Username)
	}
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"hosts": hosts})
}

// ---------- admin endpoints ----------

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAdmin(w, r)
	if claims == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := s.store.ListUsers()
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		// Never include pass_hash in API output.
		type safeUser struct {
			Username   string    `json:"username"`
			IsAdmin    bool      `json:"is_admin"`
			MustChange bool      `json:"must_change"`
			CreatedAt  time.Time `json:"created_at"`
		}
		out := make([]safeUser, 0, len(users))
		for _, u := range users {
			out = append(out, safeUser{u.Username, u.IsAdmin, u.MustChange, u.CreatedAt})
		}
		writeJSON(w, map[string]interface{}{"users": out})
	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			IsAdmin  bool   `json:"is_admin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Username) == "" || len(req.Password) < 8 {
			http.Error(w, "username required, password >= 8 chars", http.StatusBadRequest)
			return
		}
		if err := s.store.CreateUser(req.Username, req.Password, req.IsAdmin); err != nil {
			http.Error(w, "could not create user (may already exist)", http.StatusConflict)
			return
		}
		s.store.Audit(claims.Username, "user_create", req.Username, boolStr(req.IsAdmin, "admin", "user"))
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAdmin(w, r)
	if claims == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.EqualFold(req.Username, claims.Username) {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteUser(req.Username); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.store.Audit(claims.Username, "user_delete", req.Username, "")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAdmin(w, r)
	if claims == nil {
		return
	}
	var req struct {
		Username    string `json:"username"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		http.Error(w, "temporary password must be >= 8 chars", http.StatusBadRequest)
		return
	}
	// temporary=true forces the user to set their own password at next login.
	if err := s.store.SetPassword(req.Username, req.NewPassword, true); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.store.Audit(claims.Username, "password_reset", req.Username, "admin set temporary")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdminRole(w http.ResponseWriter, r *http.Request) {
	claims := s.requireAdmin(w, r)
	if claims == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetAdmin(req.Username, req.IsAdmin); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.store.Audit(claims.Username, "role_change", req.Username, boolStr(req.IsAdmin, "admin", "user"))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	entries, err := s.store.RecentAudit(500)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"audit": entries})
}

// ---------- signaling websocket ----------

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin is enforced by serving the UI from this origin; the token in
	// the hello message is the real auth. Allow all origins so native clients
	// (host agent, future APK) can connect.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// First message must be a valid hello with a good token.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello protocol.Signal
	if err := conn.ReadJSON(&hello); err != nil || hello.Type != protocol.SigHello {
		_ = conn.WriteJSON(protocol.Signal{Type: protocol.SigError, Message: "expected hello"})
		_ = conn.Close()
		return
	}
	claims, err := s.auth.Verify(hello.Token)
	if err != nil {
		_ = conn.WriteJSON(protocol.Signal{Type: protocol.SigError, Message: "invalid token"})
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	role := hello.Role
	if role != protocol.RoleHost && role != protocol.RoleController {
		role = protocol.RoleController
	}
	c := &client{
		id:       newClientID(),
		username: claims.Username,
		isAdmin:  claims.IsAdmin,
		role:     role,
		name:     defaultName(hello.DeviceName, claims.Username, role),
		platform: hello.Platform,
		conn:     conn,
		send:     make(chan protocol.Signal, 64),
		hub:      s.hub,
		sessions: make(map[string]bool),
	}

	// Welcome, with the assigned id and the ICE server list so the peer can
	// build its RTCPeerConnection immediately.
	c.send <- protocol.Signal{
		Type:       protocol.SigWelcome,
		ClientID:   c.id,
		ICEServers: s.cfg.ICEServers,
	}
	// Controllers get the current device list right away.
	if role == protocol.RoleController {
		c.send <- protocol.Signal{Type: protocol.SigDeviceList, Devices: s.hub.onlineHostsFor(claims)}
	}

	s.hub.register <- c
	go c.writePump()
	c.readPump()
}

// ---------- helpers ----------

func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) *Claims {
	tok := bearer(r)
	if tok == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return nil
	}
	claims, err := s.auth.Verify(tok)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return nil
	}
	return claims
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *Claims {
	claims := s.requireAuth(w, r)
	if claims == nil {
		return nil
	}
	if !claims.IsAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return nil
	}
	return claims
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	// Fallback for the browser EventSource / simple GETs.
	return r.URL.Query().Get("token")
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func defaultName(name, user string, role protocol.Role) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	if role == protocol.RoleHost {
		return user + "-host"
	}
	return user + "-controller"
}

// tlsConfig loads the configured cert/key, or generates a self-signed cert if
// none is set. Self-signed is fine to get running; browsers will warn once and
// you accept the exception. For a clean padlock, drop in a real cert.
func (s *Server) tlsConfig() (*tls.Config, error) {
	if s.cfg.TLSCert != "" && s.cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	}
	cert, err := generateSelfSigned()
	if err != nil {
		return nil, err
	}
	log.Println("using generated self-signed certificate (accept the browser warning once)")
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// generateSelfSigned makes an in-memory ECDSA self-signed cert valid for a year.
func generateSelfSigned() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "CtrlFreak Relay"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

// webAssets is populated by cmd/relay via SetWebAssets so tests can pass a
// different FS. Kept here to keep the embed directive in the command package.
var _ embed.FS
