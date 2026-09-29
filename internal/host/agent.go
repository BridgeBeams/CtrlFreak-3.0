// Package host is the CtrlFreak agent that runs on a machine to be controlled.
// It connects OUTBOUND to the relay (so no inbound firewall rule is needed),
// waits for a controller to ask for a session, and then opens a direct WebRTC
// peer connection over which it streams the screen and receives input.
package host

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"log"
	"os"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/wthomson/ctrlfreak/internal/protocol"
	"golang.org/x/image/draw"
)

// Capturer grabs the screen. Platform implementations live in capture_*.go.
type Capturer interface {
	Monitors() []protocol.MonitorDim
	Capture(monitor int) (*image.RGBA, error)
}

// Injector applies remote input to the local machine. Platform implementations
// live in input_*.go.
type Injector interface {
	MouseMove(monitor int, mons []protocol.MonitorDim, x, y int)
	MouseButton(btn string, down bool)
	MouseScroll(dx, dy int)
	Key(code string, down bool)
	TypeText(s string)
}

// clipMu serializes OS clipboard access so the periodic reader and an on-demand
// write from the controller never touch the clipboard at the same time.
var clipMu sync.Mutex

// Agent owns the signaling connection and all active sessions.
type Agent struct {
	relayURL string
	token    string
	name     string
	platform string

	cap Capturer
	inj Injector

	iceServers []webrtc.ICEServer

	mu       sync.Mutex
	sessions map[string]*hostSession
	sig      *sigConn
}

// NewAgent builds an agent. cap and inj are the platform back ends.
func NewAgent(relayURL, token, name, platform string, cap Capturer, inj Injector) *Agent {
	return &Agent{
		relayURL: relayURL, token: token, name: name, platform: platform,
		cap: cap, inj: inj,
		sessions: make(map[string]*hostSession),
	}
}

// Run connects to the relay and serves sessions until the process exits,
// reconnecting on drops.
func (a *Agent) Run() {
	for {
		if err := a.serve(); err != nil {
			log.Printf("relay connection lost: %v; retrying in 5s", err)
		}
		time.Sleep(5 * time.Second)
	}
}

// SetToken updates the auth token used for the next relay connection. The host
// command re-logins on each reconnect and calls this, so a long-running service
// keeps working past the token's expiry.
func (a *Agent) SetToken(token string) { a.token = token }

// Serve makes one connection attempt to the relay and blocks until it drops,
// returning the reason. The host command wraps this in a re-login loop.
func (a *Agent) Serve() error { return a.serve() }

func (a *Agent) serve() error {
	sc, welcome, err := dialSignaling(a.relayURL, a.token, a.name, a.platform)
	if err != nil {
		return err
	}
	a.sig = sc
	a.iceServers = toPionICE(welcome.ICEServers)
	log.Printf("registered with relay as %q (%d ICE servers)", a.name, len(a.iceServers))

	for {
		msg, err := sc.read()
		if err != nil {
			return err
		}
		a.handle(msg)
	}
}

func (a *Agent) handle(msg protocol.Signal) {
	switch msg.Type {
	case protocol.SigConnect:
		// A controller (msg.HostID carries the controller's id) wants in.
		go a.startSession(msg.SessionID, msg.HostID)
	case protocol.SigAnswer:
		a.withSession(msg.SessionID, func(s *hostSession) { s.onAnswer(msg) })
	case protocol.SigCandidate:
		a.withSession(msg.SessionID, func(s *hostSession) { s.onCandidate(msg) })
	case protocol.SigBye:
		a.withSession(msg.SessionID, func(s *hostSession) { s.close(false) })
	case protocol.SigCommand:
		go a.runCommand(msg.Command)
	}
}

// runCommand executes a host command received over the signaling link. These are
// authorized on the relay before they reach us.
func (a *Agent) runCommand(name string) {
	switch name {
	case protocol.CmdReboot:
		log.Printf("remote command: reboot")
		if err := doReboot(); err != nil {
			log.Printf("reboot failed: %v", err)
		}
	case protocol.CmdRestartAgent:
		log.Printf("remote command: restart agent")
		// Give the log line a moment to flush, then exit. When installed as a
		// service with recovery actions (set at install time), the service
		// manager restarts the process within a few seconds.
		time.Sleep(750 * time.Millisecond)
		os.Exit(1)
	default:
		log.Printf("ignoring unknown command %q", name)
	}
}

func (a *Agent) withSession(id string, fn func(*hostSession)) {
	a.mu.Lock()
	s := a.sessions[id]
	a.mu.Unlock()
	if s != nil {
		fn(s)
	}
}

// startSession creates the peer connection, data channels, and SDP offer for a
// new controller. The host is always the offerer.
func (a *Agent) startSession(sessionID, controllerID string) {
	log.Printf("session %s: controller %s connecting", sessionID[:8], controllerID[:8])
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: a.iceServers})
	if err != nil {
		log.Printf("peerconnection: %v", err)
		return
	}
	s := &hostSession{
		id: sessionID, peerID: controllerID, agent: a, pc: pc,
		quality: quality{fps: 15, scale: 50, jpeg: 60}, monitor: 0,
		stop: make(chan struct{}),
	}

	a.mu.Lock()
	a.sessions[sessionID] = s
	a.mu.Unlock()

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		a.sig.write(protocol.Signal{
			Type: protocol.SigCandidate, SessionID: sessionID, HostID: controllerID,
			Candidate: c.ToJSON(),
		})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("session %s: %s", sessionID[:8], st)
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			s.close(false)
		}
	})

	// Channels: ctrl (input), video (screen), files.
	var e error
	if s.ctrl, e = pc.CreateDataChannel(protocol.ChanControl, nil); e != nil {
		log.Printf("ctrl channel: %v", e)
	}
	if s.screen, e = pc.CreateDataChannel(protocol.ChanScreen, nil); e != nil {
		log.Printf("screen channel: %v", e)
	}
	if s.files, e = pc.CreateDataChannel(protocol.ChanFiles, nil); e != nil {
		log.Printf("files channel: %v", e)
	}
	s.wireCtrl()
	s.wireFiles()
	s.screen.OnOpen(func() { go s.captureLoop() })

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Printf("create offer: %v", err)
		return
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Printf("set local desc: %v", err)
		return
	}
	a.sig.write(protocol.Signal{
		Type: protocol.SigOffer, SessionID: sessionID, HostID: controllerID, SDP: offer.SDP,
	})
}

func (a *Agent) removeSession(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

// ---------------------------------------------------------------------------

type quality struct {
	fps   int
	scale int // percent
	jpeg  int // 1..100
}

type hostSession struct {
	id     string
	peerID string
	agent  *Agent
	pc     *webrtc.PeerConnection

	ctrl   *webrtc.DataChannel
	screen *webrtc.DataChannel
	files  *webrtc.DataChannel

	mu       sync.Mutex
	quality  quality
	monitor  int
	seq      int
	paused   bool   // file-manager sessions pause screen streaming
	lastClip string // last clipboard text seen/sent, to avoid echo loops

	incoming map[string]*incomingFile // file transfers controller -> host

	closeOnce sync.Once
	stop      chan struct{}
}

func (s *hostSession) onAnswer(msg protocol.Signal) {
	err := s.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: msg.SDP,
	})
	if err != nil {
		log.Printf("set answer: %v", err)
	}
}

func (s *hostSession) onCandidate(msg protocol.Signal) {
	raw, _ := json.Marshal(msg.Candidate)
	var init webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &init); err != nil {
		return
	}
	_ = s.pc.AddICECandidate(init)
}

// wireCtrl handles input and quality-control messages from the controller.
func (s *hostSession) wireCtrl() {
	s.ctrl.OnOpen(func() {
		mons := s.agent.cap.Monitors()
		var w, h int
		if len(mons) > 0 {
			w, h = mons[0].Width, mons[0].Height
		}
		s.sendCtrl(protocol.DataMsg{Type: protocol.DataScreenInfo, Width: w, Height: h, Monitors: mons})
		go s.clipboardWatch()
	})
	s.ctrl.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			var dm protocol.DataMsg
			if json.Unmarshal(m.Data, &dm) != nil {
				return
			}
			s.applyInput(dm)
		}
	})
}

func (s *hostSession) applyInput(dm protocol.DataMsg) {
	mons := s.agent.cap.Monitors()
	switch dm.Type {
	case protocol.DataMouseMove:
		s.agent.inj.MouseMove(s.monitor, mons, dm.MX, dm.MY)
	case protocol.DataMouseButton:
		if dm.MX != 0 || dm.MY != 0 {
			s.agent.inj.MouseMove(s.monitor, mons, dm.MX, dm.MY)
		}
		s.agent.inj.MouseButton(dm.Button, dm.Down)
	case protocol.DataMouseScroll:
		s.agent.inj.MouseScroll(dm.Dx, dm.Dy)
	case protocol.DataKey:
		s.agent.inj.Key(dm.Code, dm.Down)
	case protocol.DataTypeText:
		s.agent.inj.TypeText(dm.Text)
	case protocol.DataSetQuality:
		s.mu.Lock()
		if dm.FPS > 0 {
			s.quality.fps = clamp(dm.FPS, 1, 60)
		}
		if dm.Scale > 0 {
			s.quality.scale = clamp(dm.Scale, 10, 100)
		}
		if dm.Quality > 0 {
			s.quality.jpeg = clamp(dm.Quality, 10, 95)
		}
		s.mu.Unlock()
	case protocol.DataSelectMon:
		s.mu.Lock()
		s.monitor = dm.Monitor
		s.mu.Unlock()
	case protocol.DataPause:
		s.mu.Lock()
		s.paused = dm.Paused
		s.mu.Unlock()
	case protocol.DataClipboardRx:
		// Controller set our clipboard. Remember the text so our watcher does
		// not immediately echo it back. Do the actual Win32 clipboard write on a
		// separate goroutine (serialized with the reader): clipboard calls can
		// stall in a windowless elevated process, and this handler also carries
		// mouse and keyboard, so a blocking write here would freeze all input.
		s.mu.Lock()
		s.lastClip = dm.Text
		s.mu.Unlock()
		text := dm.Text
		go func() {
			clipMu.Lock()
			writeClipboard(text)
			clipMu.Unlock()
		}()
	case protocol.DataExec:
		go s.runExec(dm.Cmd)
	}
}

// clipboardWatch polls the host clipboard and pushes changes to the controller,
// so text copied on the remote machine can be pasted locally. It runs for the
// life of the session.
func (s *hostSession) clipboardWatch() {
	t := time.NewTicker(800 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		clipMu.Lock()
		text, ok := readClipboard()
		clipMu.Unlock()
		if !ok || text == "" {
			continue
		}
		s.mu.Lock()
		changed := text != s.lastClip
		if changed {
			s.lastClip = text
		}
		s.mu.Unlock()
		if changed {
			s.sendCtrl(protocol.DataMsg{Type: protocol.DataClipboardTx, Text: text})
		}
	}
}

// runExec runs a shell command on the host and returns the combined output over
// the control channel. Authorized on the relay (owner/admin) before it arrives.
func (s *hostSession) runExec(cmd string) {
	if cmd == "" {
		return
	}
	log.Printf("session %s: exec %q", s.id[:8], cmd)
	out := runShell(cmd)
	if len(out) > 200*1024 {
		out = out[:200*1024] + "\n...[truncated]"
	}
	s.sendCtrl(protocol.DataMsg{Type: protocol.DataExecResult, Output: out})
}

func (s *hostSession) sendCtrl(dm protocol.DataMsg) {
	b, _ := json.Marshal(dm)
	if s.ctrl != nil {
		_ = s.ctrl.SendText(string(b))
	}
}

// captureLoop grabs the screen at the requested rate, scales and JPEG-encodes
// each frame, and pushes it down the screen channel as a JSON header followed by
// the binary payload.
//
// v1 sends full frames. The obvious next optimization is delta rectangles
// (only ship changed regions) or a real VP8/H264 video track; the protocol
// already carries the fields for delta frames (see DataMsg.X/Y/KeyFrame).
func (s *hostSession) captureLoop() {
	log.Printf("session %s: streaming started", s.id[:8])
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		s.mu.Lock()
		q := s.quality
		mon := s.monitor
		paused := s.paused
		s.seq++
		seq := s.seq
		s.mu.Unlock()

		// A file-manager session pauses streaming to save bandwidth: it opened
		// the session only to browse and transfer files.
		if paused {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		frameStart := time.Now()
		img, err := s.agent.cap.Capture(mon)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		payload, w, h := encodeFrame(img, q)

		// Backpressure: if the channel is backed up, skip this frame rather
		// than pile up latency.
		if s.screen.BufferedAmount() > 8*1024*1024 {
			s.sleepFrame(q, frameStart)
			continue
		}
		s.sendScreen(seq, w, h, payload)
		s.sleepFrame(q, frameStart)
	}
}

// screenChunk is the largest binary payload we put in one DataChannel message.
// Full-resolution JPEG frames exceed the SCTP single-message limit, so a frame
// is split into chunks the controller concatenates before decoding.
const screenChunk = 60 * 1024

func (s *hostSession) sendScreen(seq, w, h int, payload []byte) {
	if s.screen == nil {
		return
	}
	parts := (len(payload) + screenChunk - 1) / screenChunk
	if parts < 1 {
		parts = 1
	}
	hdr := protocol.DataMsg{Type: protocol.DataFrame, Seq: seq, KeyFrame: true, Width: w, Height: h, Parts: parts}
	b, _ := json.Marshal(hdr)
	if err := s.screen.SendText(string(b)); err != nil {
		return
	}
	for off := 0; off < len(payload); off += screenChunk {
		end := off + screenChunk
		if end > len(payload) {
			end = len(payload)
		}
		if err := s.screen.Send(payload[off:end]); err != nil {
			return
		}
	}
}

func (s *hostSession) sleepFrame(q quality, start time.Time) {
	budget := time.Second / time.Duration(max(q.fps, 1))
	if d := budget - time.Since(start); d > 0 {
		time.Sleep(d)
	}
}

func (s *hostSession) close(tellPeer bool) {
	s.closeOnce.Do(func() {
		close(s.stop)
		if tellPeer {
			s.agent.sig.write(protocol.Signal{Type: protocol.SigBye, SessionID: s.id, HostID: s.peerID})
		}
		_ = s.pc.Close()
		s.agent.removeSession(s.id)
		log.Printf("session %s: closed", s.id[:8])
	})
}

// encodeFrame scales an RGBA frame by q.scale percent and JPEG-encodes it.
func encodeFrame(img *image.RGBA, q quality) ([]byte, int, int) {
	src := img.Bounds()
	w := src.Dx() * q.scale / 100
	h := src.Dy() * q.scale / 100
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	var toEncode image.Image = img
	if q.scale != 100 {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, src, draw.Over, nil)
		toEncode = dst
	} else {
		w, h = src.Dx(), src.Dy()
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, toEncode, &jpeg.Options{Quality: q.jpeg})
	return buf.Bytes(), w, h
}

func toPionICE(in []protocol.ICEServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(in))
	for _, s := range in {
		srv := webrtc.ICEServer{URLs: s.URLs}
		if s.Username != "" {
			srv.Username = s.Username
			srv.Credential = s.Credential
		}
		out = append(out, srv)
	}
	if len(out) == 0 {
		out = append(out, webrtc.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}})
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
