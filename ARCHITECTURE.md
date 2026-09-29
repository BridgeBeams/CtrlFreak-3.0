# CtrlFreak Architecture

This document explains how a session gets from your laptop or phone to a remote
machine, how it crosses firewalls, and how the pieces fit so you can extend them.

## The three parts

```
   +-------------------+           +-----------------------+
   |   Controller      |           |     Host agent        |
   | (browser: laptop  |           | (ctrlfreak-host.exe   |
   |  or Galaxy Fold)  |           |  on the remote PC)    |
   +---------+---------+           +-----------+-----------+
             |                                 |
   outbound  |  1. sign in (HTTPS)             | outbound
   WSS       |  2. signaling (WebSocket)       | WSS
             v                                 v
        +----+---------------------------------+----+
        |               RELAY SERVER                |
        |  cmd/relay: HTTPS + WebSocket + accounts  |
        |  - authenticates every connection         |
        |  - tracks which hosts are online          |
        |  - forwards ONLY WebRTC setup messages    |
        |  - serves the browser controller UI       |
        +-------------------------------------------+

   3. After introduction, the controller and host open a DIRECT,
      end-to-end encrypted WebRTC connection:

        Controller  <===== screen frames / input / files =====>  Host
                    (DTLS-encrypted DataChannels, peer to peer,
                     via STUN; or via TURN relay if blocked)
```

The relay is a matchmaker and a web server. Screen pixels and keystrokes do not
flow through it in the normal (STUN/direct) case, and when a TURN relay is
needed the relay only ever sees encrypted bytes.

## Why nothing needs an inbound firewall hole on controlled machines

Firewalls (corporate, Webroot, a home router) block *unsolicited inbound*
connections. They freely allow *outbound* connections your software starts, the
same way a browser reaches any website.

CtrlFreak only ever makes outbound connections from the machines you control:

1. The host agent dials the relay outbound over WSS (WebSocket over TLS, port
   8443 by default, and you can move it to 443 to look exactly like normal web
   traffic). It keeps that connection open and waits.
2. The controller also dials the relay outbound.
3. To start a session, the relay tells the host "a controller wants you," and the
   two peers exchange, through the relay, the small setup messages WebRTC needs:
   an SDP offer/answer and ICE candidates.
4. Using those, they establish a direct peer-to-peer connection. ICE (Interactive
   Connectivity Establishment) tries, in order:
   - **host candidates** (same LAN: a direct local connection, lowest latency),
   - **server-reflexive candidates** via **STUN** (each peer asks a STUN server
     "what public ip/port do you see me as?" and they connect through the NAT
     holes that opens),
   - **relayed candidates** via **TURN** (if both sides are behind firewalls
     strict enough to block the above, the encrypted media is relayed through a
     TURN server that both can reach outbound).

Because every step is outbound from the controlled machine, no port forwarding
or firewall exception is required there. The only machine that needs to be
reachable on the internet is the relay, and only on its single HTTPS port.

## The one inbound requirement: the relay

The relay must be reachable by hosts and controllers. Two clean options:

- **VPS (simplest):** run the relay on a small cloud server with a public IP and
  a domain. Nothing at home or work needs a firewall change.
- **Home server + one port forward:** forward one inbound TCP port (e.g. 8443)
  on your home router to the home server running the relay. This exposes only the
  relay, never any controlled machine. Put a real TLS cert on it and you are set.

Moving the relay to port 443 makes its traffic indistinguishable from ordinary
HTTPS, which is the most firewall-friendly option of all.

## Transport detail

- **Signaling channel:** `wss://relay/ws`. First message is a `hello` carrying
  the JWT from `/api/login`. The relay validates it, records the connection as a
  host or a controller, and from then on forwards only offer/answer/candidate/bye
  between the two peers of a session. Message shapes are in
  `internal/protocol/protocol.go`.
- **Session channel:** WebRTC DataChannels between the peers. Three channels per
  session:
  - `ctrl` (reliable, ordered): input events and quality controls, plus the
    initial screen dimensions.
  - `video` (reliable, ordered): screen frames. Each frame is a small JSON header
    followed by the binary JPEG payload.
  - `files` (reliable, ordered): file transfer in either direction, as headers
    plus binary chunks, with backpressure so a big file cannot swamp the link.
- **Encryption:** WebRTC DataChannels are DTLS-encrypted end to end. Keys are
  negotiated directly between the two peers; the relay never holds them.

## Screen and input on the host

- **Capture** (`internal/host/capture_windows.go`): grabs the selected monitor
  via the Windows GDI path in `kbinani/screenshot`, which uses only Windows
  syscalls, so the host stays a single cgo-free `.exe`. Multi-monitor is
  enumerated and selectable.
- **Encoding** (`internal/host/agent.go`): each frame is scaled by the requested
  percentage and JPEG-encoded at the requested quality. The controller sets fps,
  scale, and quality live from the session toolbar. Backpressure skips frames
  rather than growing latency when the link is slow.
- **Input** (`internal/host/input_windows.go`): applies mouse and keyboard via
  the Win32 `SendInput` API (again pure syscalls). Mouse coordinates arrive as
  monitor-local pixels and are mapped to the absolute virtual-desktop space so
  multi-monitor positioning is correct. A `KeyboardEvent.code`-to-virtual-key map
  covers the common key set and is easy to extend.

## Extending

The platform-specific pieces sit behind two interfaces in
`internal/host/agent.go`:

```go
type Capturer interface {
    Monitors() []protocol.MonitorDim
    Capture(monitor int) (*image.RGBA, error)
}
type Injector interface {
    MouseMove(monitor int, mons []protocol.MonitorDim, x, y int)
    MouseButton(btn string, down bool)
    MouseScroll(dx, dy int)
    Key(code string, down bool)
}
```

To add **Linux or macOS host support**, implement these in new build-tagged
files (`capture_linux.go`, `input_linux.go`, and so on). The stubs in
`capture_other.go` / `input_other.go` show the shape and let the host build on
any platform today.

To move from JPEG frames to **delta rectangles or a real video codec**: the
frame header already carries `Seq`, `KeyFrame`, and dirty-rect `X`/`Y` fields.
A delta encoder would diff against the previous frame and ship only changed
tiles; a codec path would add a WebRTC video track alongside the data channels
and encode with VP8/H.264.

## Standing up a TURN server (for the strict-firewall case)

Public STUN handles most networks. For networks that block peer-to-peer entirely
(some corporate LANs), run a TURN server such as **coturn**:

```bash
# on a public host:
sudo apt install coturn
# /etc/turnserver.conf (minimal):
#   listening-port=3478
#   fingerprint
#   lt-cred-mech
#   user=ctrlfreak:a-long-shared-secret
#   realm=your-relay.example.com
```

Then add it to the relay `config.json` so every peer receives it:

```json
"ice_servers": [
  { "urls": ["stun:stun.l.google.com:19302"] },
  { "urls": ["turn:your-relay.example.com:3478"], "username": "ctrlfreak", "credential": "a-long-shared-secret" }
]
```

For production, prefer short-lived TURN credentials (a REST endpoint that mints
time-limited username/password pairs) over a static shared secret. That is a
natural addition to the relay's API.

## Auto-starting the host on Windows

Register the agent so it runs without someone launching it:

- **Scheduled Task (simplest):** create a task set to "run whether user is logged
  on or not," trigger "at log on," action pointing at `ctrlfreak-host.exe` with
  its flags. Store the password via the task's stored credentials, or pass a
  token instead of a password.
- **Windows service:** wrap the agent with a service shim (for example the
  `kardianos/service` package) so Windows manages start/stop and restart. This is
  the cleanest option for the always-on home server and work PC.
