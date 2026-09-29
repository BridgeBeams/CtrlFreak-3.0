# CtrlFreak

A self-hosted, server/client remote control tool. Think TeamViewer, but you own
the whole stack: your relay, your accounts, your machines. Control any of your
Windows PCs from your laptop or your phone, across networks, through corporate
and home firewalls, with encrypted end-to-end sessions and drag-and-drop file
transfer.

Built for the setup you described:

- One relay you run (on the home server or a small VPS).
- A **host agent** (`ctrlfreak-host.exe`) you install on each machine you want
  to reach: work PC, home server, laptop, and any you hand to family.
- A **browser controller** served by the relay that works on your laptop and on
  the Galaxy Fold. Up to **3 simultaneous sessions** from the laptop, **1** from
  the phone.
- **Accounts** for you and your kids. You (admin) can create and delete users,
  reset passwords, promote/demote admins, and reach any user's machines. Every
  action is logged.

## Why it gets through firewalls

This is the part you were right to worry about, and it is handled by design.

Nothing ever listens for inbound connections on the machines you control. The
host agent and the controller both make **outbound** connections to the relay,
which is traffic your company firewall and Webroot already allow (it looks like
any HTTPS/WebSocket client). The relay introduces the two sides, and they then
open a **direct, encrypted WebRTC connection** using STUN to punch through NAT.
When a firewall is strict enough to block even that (some corporate networks
do), the encrypted stream falls back to a **TURN relay**, so it still connects.
See `ARCHITECTURE.md` for the full path and how to add a TURN server.

## What is in the box

```
ctrlfreak/
  cmd/relay/       the relay server (also serves the web controller UI)
  cmd/relay/web/   the browser controller (HTML/JS, embedded into the relay .exe)
  cmd/host/        the host agent (the machine being controlled)
  internal/relay/  auth, accounts, signaling, admin API
  internal/host/   screen capture, input injection, file transfer, WebRTC
  internal/protocol/  the shared wire format
  Makefile, scripts/  build everything
```

## Getting the installer (one .exe, no build tools, no batch files)

CtrlFreak ships as a single `CtrlFreak-Setup.exe`. Run it on any machine and it
asks what that machine's job is, then installs the matching piece(s):

- **Hub (relay)** the central server everything connects through (your home
  server or a VPS). Installs as an auto-starting Windows service.
- **Controlled machine** lets you remote into that PC. Installs the host agent as
  an auto-starting Windows service.
- **Controller shortcut** a desktop shortcut that opens the web controller in
  your browser (on a laptop the controller is just the browser; the phone gets a
  real app, the APK).

Tick one, two, or all three on the same machine.

You do not build this by hand. GitHub builds it for you in the cloud:

1. Put this code in a GitHub repository (upload through GitHub's website, or use
   GitHub Desktop, no command line either way). Include the `.github` folder.
2. Click **Releases > Draft a new release**, give it a tag like `v2.0`, and
   **Publish**. The workflow compiles everything on a Windows runner and attaches
   **`CtrlFreak-Setup.exe`** to that release.
3. Download that one file. That is what you run on every machine.

(Every ordinary push also builds it and attaches it under the run's page as a
zipped artifact. A `Makefile` still exists for local builds, but nobody needs it.)

## Run it

### 1. Set up the hub

On your home server or VPS, run **`CtrlFreak-Setup.exe`**, tick **Hub**, and
enter an admin password and port in the wizard. It installs the hub as a service
and starts it. The web controller is then at `https://<hub-host>:<port>/`.

It uses a self-signed certificate by default, so the browser warns once and you
accept the exception. For a clean padlock, drop a real cert path into
`config.json` next to the installed exe (see `config.example.json`). For the hub
to be reachable from your work PC and phone, its one HTTPS port must be open to
the internet: run it on a VPS, or forward one inbound port on your home router to
the hub only (never to a controlled machine).

### 2. Set up each controlled machine

On each PC you want to reach, run the same **`CtrlFreak-Setup.exe`**, tick
**Controlled machine**, and enter the hub URL (`wss://<hub-host>:<port>/ws`), your
username, and password. It installs the agent as an auto-starting service that
runs on every boot, no window, no command line. Same file for all your machines.

### 3. Control from the laptop or the Fold

Open `https://<hub-host>:<port>/` in any browser, sign in, and click a machine to
start a session. Drag files onto the remote screen to send them. On the laptop
you can open several sessions side by side (the cap is `max_sessions_per_controller`);
the phone runs one at a time.

### 4. The Android app (native)

The `android/` folder is a full native Kotlin client for your phone. GitHub builds
the `.apk` for you (see `.github/workflows/build-android.yml`); it is attached to
each published release as `CtrlFreak.apk`, and also appears as a build artifact on
ordinary pushes. To install: on the Fold, allow "install unknown apps" for your
browser or file manager, download `CtrlFreak.apk`, tap it, and open it. Sign in
with the same hub URL and account, tap a machine to control it, and use the
on-screen bar for the keyboard, right-click, Esc, Win, and to close. One finger
moves and drags/clicks; two fingers scroll. The keyboard types real Unicode into
the remote PC. Reboot and Restart-agent are on each machine in the list.

## Accounts and admin

Everyone can change their own password from the **Account** button in the top
bar (current password plus the new one). This works for regular users and for
the admin.

Sign in as the admin (you) and open the **Admin** tab:

- **Create user**: give each child a username and a temporary password. They set
  their own password at first login.
- **Reset password**: sets a new temporary password for any user; they must
  change it next login. (You cannot *view* anyone's password, and that is on
  purpose. See `SECURITY.md`.)
- **Make admin / Make user**: promote or demote.
- **Delete**: removes a user and the machines they registered.
- **Access log**: every sign in, session start/stop, file transfer, and admin
  action, with who and when.

As admin you can open a session to any user's online machine. That access is
logged like everything else, and the person's machine shows the normal session
indicator: CtrlFreak is an honest support tool, not a hidden one. (This is the
one place the design intentionally differs from your first sketch; the reasons
are in `SECURITY.md`.)

## What works today, and what is next

**Working:** relay + accounts + admin, outbound firewall traversal via
WebRTC/STUN (TURN-ready), Windows host (screen streaming + full mouse/keyboard),
browser controller on laptop and phone, multi-session (configurable), drag-and-drop
file send, adjustable quality (resolution scale + fps), self-service password
change, and **remote power actions** (Restart agent and Reboot) sent over the
signaling link so they work even when a session is frozen. Each device row in the
controller has a Restart-agent button and a Reboot button; both confirm first and
are written to the audit log.

**Good next steps** (all scaffolded to slot in):

1. **Native Android APK** wrapping the controller (Capacitor or a thin native
   shell) so the phone gets a real app icon and background reconnect.
2. **Delta-frame or H.264 video** for lower bandwidth (the protocol already
   carries the fields; today it streams scaled JPEG frames, which is simple and
   robust).
3. **Linux/macOS host capture and input** (Windows is done; the interfaces are
   ready for the other back ends).
4. **A real TURN server** in your infrastructure for the strict-firewall case.
5. **Auto-start installer** for the host (Windows service wrapper).

See `ARCHITECTURE.md` for how each piece fits and `SECURITY.md` for the security
model and the deliberate design choices.
