// Package protocol defines the wire messages shared by the relay, the host
// agent, and the browser controller. Everything that crosses a network hop in
// CtrlFreak is one of these types, serialized as JSON.
//
// There are two transports:
//
//  1. Signaling (WebSocket to the relay): used only to authenticate, discover
//     which hosts are online, and exchange the small handful of WebRTC setup
//     messages (SDP offer/answer and ICE candidates) needed to open a direct
//     peer connection. No screen data or input ever flows through the relay's
//     signaling channel.
//
//  2. Session (WebRTC DataChannel, controller <-> host): once the peer
//     connection is up, screen frames, input events, clipboard, and file
//     chunks travel here, end to end encrypted by DTLS. The relay never sees
//     this traffic in the direct/STUN case, and sees only ciphertext when a
//     TURN relay is required.
package protocol

// SignalType enumerates messages on the signaling WebSocket.
type SignalType string

const (
	// Auth / session lifecycle
	SigHello       SignalType = "hello"        // client -> relay: token, role, device info
	SigWelcome     SignalType = "welcome"      // relay -> client: accepted, assigned id
	SigError       SignalType = "error"        // relay -> client: human-readable reason
	SigDeviceList  SignalType = "device_list"  // relay -> controller: online hosts this user may reach
	SigDeviceEvent SignalType = "device_event" // relay -> controller: a host came online / went offline

	// WebRTC negotiation (relayed verbatim between the two peers)
	SigConnect   SignalType = "connect"    // controller -> relay: start a session to host_id
	SigOffer     SignalType = "offer"      // peer -> peer: SDP offer
	SigAnswer    SignalType = "answer"     // peer -> peer: SDP answer
	SigCandidate SignalType = "candidate"  // peer -> peer: ICE candidate
	SigBye       SignalType = "bye"        // either peer: tear down this session
	SigIceConfig SignalType = "ice_config" // relay -> client: STUN/TURN servers to use

	// Host commands (controller -> relay -> host). These ride the always-on
	// signaling link, NOT the WebRTC session, so they work even when a session
	// is frozen or refusing to connect.
	SigCommand SignalType = "command"
)

// Host command names carried in Signal.Command.
const (
	CmdReboot       = "reboot"        // shutdown /r on the host
	CmdRestartAgent = "restart_agent" // bounce the CtrlFreak agent/service
)

// Role identifies what a signaling connection is.
type Role string

const (
	RoleHost       Role = "host"       // a machine that can be controlled
	RoleController Role = "controller" // laptop browser or phone doing the controlling
)

// Signal is the envelope for every signaling-channel message. Only the fields
// relevant to a given Type are populated.
type Signal struct {
	Type SignalType `json:"type"`

	// Session correlation. A single controller can run several sessions at
	// once (up to the per-device cap), so every negotiation message carries
	// the session id it belongs to.
	SessionID string `json:"session_id,omitempty"`

	// Hello fields
	Token      string `json:"token,omitempty"`       // JWT from /api/login
	Role       Role   `json:"role,omitempty"`        // host or controller
	DeviceName string `json:"device_name,omitempty"` // "Wade Work PC", "Home Server"
	Platform   string `json:"platform,omitempty"`    // "windows", "linux", "android"

	// Welcome fields
	ClientID string `json:"client_id,omitempty"` // relay-assigned connection id

	// Connect / negotiation
	HostID    string      `json:"host_id,omitempty"`   // target host connection id
	SDP       string      `json:"sdp,omitempty"`       // offer/answer payload
	Candidate interface{} `json:"candidate,omitempty"` // ICE candidate (opaque JSON)

	// Device list / events
	Devices []DeviceInfo `json:"devices,omitempty"`
	Device  *DeviceInfo  `json:"device,omitempty"`
	Online  bool         `json:"online,omitempty"`

	// ICE config
	ICEServers []ICEServer `json:"ice_servers,omitempty"`

	// Host command (reboot / restart_agent), see Cmd* constants.
	Command string `json:"command,omitempty"`

	// Errors
	Message string `json:"message,omitempty"`
}

// DeviceInfo is what a controller sees about a reachable host.
type DeviceInfo struct {
	ID       string `json:"id"`       // current connection id (changes per session)
	Name     string `json:"name"`     // friendly name
	Owner    string `json:"owner"`    // username who registered the host
	Platform string `json:"platform"` // os
	Online   bool   `json:"online"`
}

// ICEServer mirrors the browser RTCIceServer shape so the same struct can be
// handed to pion on the host and to the JS RTCPeerConnection in the browser.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ---------------------------------------------------------------------------
// DataChannel messages (controller <-> host, over WebRTC)
// ---------------------------------------------------------------------------

// ChannelKind labels the DataChannels a session opens. Screen frames get their
// own channel so a large frame never head-of-line-blocks an input event.
const (
	ChanControl = "ctrl"  // input, clipboard, session control (reliable, ordered)
	ChanScreen  = "video" // screen frames host -> controller (see note in host)
	ChanFiles   = "files" // file transfer, both directions (reliable, ordered)
)

// DataType enumerates messages on the control and screen channels.
type DataType string

const (
	// Host -> controller
	DataScreenInfo  DataType = "screen_info"  // dimensions, monitor list
	DataFrame       DataType = "frame"        // one screen frame (JPEG bytes follow as binary)
	DataClipboardTx DataType = "clipboard_tx" // host clipboard changed

	// Controller -> host
	DataMouseMove   DataType = "mouse_move"
	DataMouseButton DataType = "mouse_button"
	DataMouseScroll DataType = "mouse_scroll"
	DataKey         DataType = "key"
	DataTypeText    DataType = "type_text"    // type a string of unicode text (phone keyboards)
	DataClipboardRx DataType = "clipboard_rx" // set host clipboard
	DataSetQuality  DataType = "set_quality"  // fps / jpeg quality / scale
	DataSelectMon   DataType = "select_mon"   // choose which monitor to stream
	DataRequestKey  DataType = "request_key"  // ask host for a fresh full frame
	DataPause       DataType = "pause"        // pause/resume screen streaming (file-only sessions)

	// Remote command execution (controller -> host, host -> controller)
	DataExec       DataType = "exec"        // controller -> host: run a shell command
	DataExecResult DataType = "exec_result" // host -> controller: combined output
)

// DataMsg is the JSON envelope for control/screen-channel messages. Binary
// frame payloads are sent as a separate binary DataChannel message immediately
// following the DataFrame JSON header.
type DataMsg struct {
	Type DataType `json:"type"`

	// Screen info
	Width    int          `json:"w,omitempty"`
	Height   int          `json:"h,omitempty"`
	Monitors []MonitorDim `json:"monitors,omitempty"`
	Monitor  int          `json:"mon,omitempty"`

	// Frame header (bytes follow as the next binary message(s))
	Seq      int  `json:"seq,omitempty"`
	KeyFrame bool `json:"key,omitempty"`
	// Parts is how many binary messages make up this frame. A full-resolution
	// JPEG can exceed the WebRTC data-channel message limit, so the host splits
	// it into Parts chunks that the controller concatenates before decoding.
	Parts int `json:"parts,omitempty"`
	// Optional dirty-rect for delta frames; zero value means full frame.
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`

	// Remote command (DataExec / DataExecResult)
	Cmd    string `json:"cmd,omitempty"`
	Output string `json:"output,omitempty"`

	// Mouse (coordinates are in remote screen pixels)
	MX     int    `json:"mx,omitempty"`
	MY     int    `json:"my,omitempty"`
	Button string `json:"btn,omitempty"` // "left","right","middle"
	Down   bool   `json:"down,omitempty"`
	Dx     int    `json:"dx,omitempty"`
	Dy     int    `json:"dy,omitempty"`

	// Keyboard
	Code  string `json:"code,omitempty"` // JS KeyboardEvent.code, e.g. "KeyA","Enter"
	Alt   bool   `json:"alt,omitempty"`
	Ctrl  bool   `json:"ctrl,omitempty"`
	Shift bool   `json:"shift,omitempty"`
	Meta  bool   `json:"meta,omitempty"`

	// Quality controls
	FPS     int `json:"fps,omitempty"`
	Quality int `json:"quality,omitempty"` // JPEG quality 1..100
	Scale   int `json:"scale,omitempty"`   // percent, e.g. 100, 75, 50

	// Clipboard
	Text string `json:"text,omitempty"`

	// Pause/resume screen streaming (DataPause). Used by the file manager,
	// which opens a session purely to browse/transfer and does not need video.
	Paused bool `json:"paused,omitempty"`
}

// MonitorDim describes one display on the host.
type MonitorDim struct {
	Index  int `json:"index"`
	Width  int `json:"w"`
	Height int `json:"h"`
	X      int `json:"x"`
	Y      int `json:"y"`
}

// ---------------------------------------------------------------------------
// File transfer (on the files channel)
// ---------------------------------------------------------------------------

// FileType enumerates messages on the files channel.
type FileType string

const (
	FileOfferTx  FileType = "offer"    // sender announces a file (name,size,id)
	FileAccept   FileType = "accept"   // receiver is ready
	FileReject   FileType = "reject"   // receiver declines
	FileChunkHdr FileType = "chunk"    // header; binary chunk follows
	FileDone     FileType = "done"     // all chunks sent
	FileError    FileType = "file_err" // something went wrong
	FileList     FileType = "list"     // request/response: remote directory listing
	FileListResp FileType = "list_resp"
	FileGet      FileType = "get"    // controller -> host: send me this file (download)
	FileDrives   FileType = "drives" // controller -> host: list drive roots / start places
)

// FileMsg is the JSON envelope on the files channel. Chunk bodies follow as
// binary DataChannel messages after a FileChunkHdr.
type FileMsg struct {
	Type FileType `json:"type"`

	TransferID string `json:"tid,omitempty"`
	Name       string `json:"name,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	// Direction as seen by the host: "upload" = controller -> host,
	// "download" = host -> controller.
	Direction string `json:"dir,omitempty"`
	// Dest is the absolute destination folder for an upload (FileOfferTx). When
	// empty the host writes to its default CtrlFreak Downloads folder. The file
	// manager sets this so a transfer lands in the folder you navigated to.
	Dest string `json:"dest,omitempty"`

	// Directory listing
	Path    string      `json:"path,omitempty"`   // absolute path that was listed
	Parent  string      `json:"parent,omitempty"` // parent of Path, "" at a root
	Entries []FileEntry `json:"entries,omitempty"`

	Message string `json:"message,omitempty"`
}

// FileEntry is one item in a remote directory listing.
type FileEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"dir"`
	Size  int64  `json:"size"`
	Mod   int64  `json:"mod,omitempty"` // unix seconds, modified time
}
