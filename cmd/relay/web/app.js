// CtrlFreak browser controller.
//
// Talks to the relay for login + signaling, then opens a direct encrypted
// WebRTC peer connection to each host. Screen frames, input, and files travel
// over DataChannels, never through the relay's signaling path.
//
// The host is the WebRTC offerer (it creates the data channels and the SDP
// offer); the browser answers. That keeps the host outbound-only, which is what
// gets it through the corporate firewall and Webroot.

'use strict';

const API = {
  token: localStorage.getItem('cf_token') || '',
  isAdmin: false,
  username: '',
  async login(username, password) {
    const r = await fetch('/api/login', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username, password}),
    });
    if (!r.ok) throw new Error((await r.text()) || 'login failed');
    return r.json();
  },
  async changePassword(oldp, newp) {
    return this._post('/api/change-password', {old_password: oldp, new_password: newp});
  },
  async me() { return this._get('/api/me'); },
  async ice() { return this._get('/api/ice'); },
  async adminUsers() { return this._get('/api/admin/users'); },
  async adminCreate(u) { return this._post('/api/admin/users', u); },
  async adminDelete(username) { return this._post('/api/admin/users/delete', {username}); },
  async adminReset(username, new_password) { return this._post('/api/admin/users/reset', {username, new_password}); },
  async adminRole(username, is_admin) { return this._post('/api/admin/users/role', {username, is_admin}); },
  async adminAudit() { return this._get('/api/admin/audit'); },
  async _get(path) {
    const r = await fetch(path, {headers: {'Authorization': 'Bearer ' + this.token}});
    if (r.status === 401) { logout(); throw new Error('session expired'); }
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  async _post(path, body) {
    const r = await fetch(path, {
      method: 'POST',
      headers: {'Authorization': 'Bearer ' + this.token, 'Content-Type': 'application/json'},
      body: JSON.stringify(body),
    });
    if (r.status === 401) { logout(); throw new Error('session expired'); }
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
};

// ---- view switching ----
const views = {
  login: document.getElementById('login-view'),
  change: document.getElementById('change-view'),
  main: document.getElementById('main-view'),
};
function show(name) {
  for (const k in views) views[k].classList.toggle('hidden', k !== name);
}

// ---- session cap ----
// A phone caps itself at 1 live screen; a laptop uses the server's configured
// max (set via max_sessions_per_controller in the relay config). enterApp()
// fills in the laptop value from /api/me; this is the fallback until then.
const IS_MOBILE = window.matchMedia('(max-width:820px)').matches;
let MAX_SESSIONS = IS_MOBILE ? 1 : 10;

// =====================================================================
//  Signaling
// =====================================================================
let ws = null;
let myClientId = '';
let iceServers = [{urls: 'stun:stun.l.google.com:19302'}];
const sessions = new Map();      // sessionId -> Session (visible screen tiles)
const peers = new Map();         // sessionId -> anything with onOffer/onCandidate/close
const devices = new Map();       // hostId -> device info

function connectSignaling() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onopen = () => {
    ws.send(JSON.stringify({type: 'hello', token: API.token, role: 'controller', platform: 'web'}));
  };
  ws.onmessage = (ev) => {
    let msg; try { msg = JSON.parse(ev.data); } catch { return; }
    handleSignal(msg);
  };
  ws.onclose = () => {
    // Auto-reconnect while logged in.
    if (API.token) setTimeout(connectSignaling, 2000);
  };
}

function signal(obj) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(obj)); }

function handleSignal(msg) {
  switch (msg.type) {
    case 'welcome':
      myClientId = msg.client_id;
      if (msg.ice_servers && msg.ice_servers.length) iceServers = msg.ice_servers;
      break;
    case 'device_list':
      devices.clear();
      (msg.devices || []).forEach(d => devices.set(d.id, d));
      renderDevices();
      break;
    case 'device_event':
      if (msg.device) {
        if (msg.online) devices.set(msg.device.id, msg.device);
        else devices.delete(msg.device.id);
        renderDevices();
      }
      break;
    case 'offer': {
      const s = peers.get(msg.session_id);
      if (s) s.onOffer(msg);
      break;
    }
    case 'candidate': {
      const s = peers.get(msg.session_id);
      if (s) s.onCandidate(msg);
      break;
    }
    case 'bye': {
      const s = peers.get(msg.session_id);
      if (s) s.close(false);
      break;
    }
    case 'error':
      if (msg.session_id && peers.has(msg.session_id)) {
        const p = peers.get(msg.session_id);
        if (p.setStat) p.setStat('error: ' + msg.message);
        if (p.onError) p.onError(msg.message);
        p.close(false);
      } else {
        alert('CtrlFreak: ' + msg.message);
      }
      break;
  }
}

// =====================================================================
//  Session (one remote machine)
// =====================================================================
class Session {
  constructor(device) {
    this.id = crypto.randomUUID();
    peers.set(this.id, this);
    this.device = device;
    this.peerId = device.id;
    this.lastRemoteClip = '';
    this.pc = null;
    this.ctrl = null; this.screen = null; this.files = null;
    this.pendingFrame = null;
    this.remoteW = 0; this.remoteH = 0;
    this.transfers = new Map(); // tid -> {meta, chunks, received}
    this.buildTile();
  }

  buildTile() {
    const tpl = document.getElementById('session-tile-tpl');
    this.el = tpl.content.firstElementChild.cloneNode(true);
    this.el.querySelector('.tile-name').textContent = this.device.name;
    this.canvas = this.el.querySelector('canvas.screen');
    this.cctx = this.canvas.getContext('2d', {alpha: false});
    this.statEl = this.el.querySelector('.tile-stat');

    this.el.querySelector('.tile-close').onclick = () => this.close(true);
    // Fullscreen button toggles: enter if not fullscreen, exit if already.
    this.el.querySelector('.tile-full').onclick = () => {
      if (document.fullscreenElement) {
        document.exitFullscreen && document.exitFullscreen();
      } else if (this.el.requestFullscreen) {
        this.el.requestFullscreen();
      }
    };
    // Remote command button.
    const cmdBtn = this.el.querySelector('.tile-cmd');
    if (cmdBtn) cmdBtn.onclick = () => this.openCmd();
    // Clipboard: push the local clipboard to the remote machine.
    const clipBtn = this.el.querySelector('.tile-clip');
    if (clipBtn) clipBtn.onclick = () => this.sendClipboardToRemote();
    this.el.querySelector('.tile-files').onclick = () => {
      const d = this.el.querySelector('.tile-files-drawer');
      d.classList.toggle('hidden');
      if (!d.classList.contains('hidden')) this.refreshRemoteList();
    };
    this.el.querySelector('.files-close').onclick = () =>
      this.el.querySelector('.tile-files-drawer').classList.add('hidden');

    this.el.querySelector('.q-scale').onchange = (e) => this.sendQuality({scale: +e.target.value});
    this.el.querySelector('.q-fps').onchange = (e) => this.sendQuality({fps: +e.target.value});

    this.wireInput();
    this.wireFiles();

    document.getElementById('session-grid').appendChild(this.el);
    updateGrid();
  }

  setStat(t) { this.statEl.textContent = t; }

  async start() {
    // Tell the relay we want this host; the host will send us an offer.
    this.setStat('connecting...');
    signal({type: 'connect', session_id: this.id, host_id: this.peerId});
  }

  makePC() {
    this.pc = new RTCPeerConnection({iceServers});
    this.pc.onicecandidate = (e) => {
      if (e.candidate) signal({type: 'candidate', session_id: this.id, host_id: this.peerId, candidate: e.candidate});
    };
    this.pc.onconnectionstatechange = () => {
      const st = this.pc.connectionState;
      if (st === 'connected') this.setStat('live');
      else if (st === 'failed' || st === 'disconnected') this.setStat(st);
    };
    // Host creates the channels; we receive them.
    this.pc.ondatachannel = (e) => {
      const ch = e.channel;
      if (ch.label === 'ctrl') this.bindCtrl(ch);
      else if (ch.label === 'video') this.bindScreen(ch);
      else if (ch.label === 'files') this.bindFiles(ch);
    };
  }

  async onOffer(msg) {
    if (!this.pc) this.makePC();
    this.peerId = msg.host_id || this.peerId;
    await this.pc.setRemoteDescription({type: 'offer', sdp: msg.sdp});
    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    signal({type: 'answer', session_id: this.id, host_id: this.peerId, sdp: answer.sdp});
  }

  async onCandidate(msg) {
    if (this.pc && msg.candidate) {
      try { await this.pc.addIceCandidate(msg.candidate); } catch (e) {}
    }
  }

  // ---- control channel ----
  bindCtrl(ch) {
    this.ctrl = ch;
    ch.onmessage = (e) => {
      let m; try { m = JSON.parse(e.data); } catch { return; }
      if (m.type === 'screen_info') {
        this.remoteW = m.w; this.remoteH = m.h;
        this.canvas.width = m.w; this.canvas.height = m.h;
      } else if (m.type === 'clipboard_tx') {
        // Remote clipboard changed. Mirror it into the local clipboard when we
        // have focus (browsers block clipboard writes otherwise); always keep
        // the last value so the "copy remote clipboard" button works.
        this.lastRemoteClip = m.text || '';
        if (document.hasFocus() && navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(this.lastRemoteClip).catch(() => {});
        }
      } else if (m.type === 'exec_result') {
        this.showCmdOutput(m.output || '(no output)');
      }
    };
    ch.onopen = () => { this.sendQuality({}); }; // push initial quality prefs
  }
  sendCtrl(obj) { if (this.ctrl && this.ctrl.readyState === 'open') this.ctrl.send(JSON.stringify(obj)); }
  sendQuality(partial) {
    const scale = +this.el.querySelector('.q-scale').value;
    const fps = +this.el.querySelector('.q-fps').value;
    this.sendCtrl(Object.assign({type: 'set_quality', scale, fps, quality: 60}, partial));
  }

  // ---- screen channel ----
  // A frame arrives as a JSON header (with "parts": N) then N binary chunks.
  // At full resolution a JPEG is too big for one WebRTC message, so the host
  // splits it; we collect the chunks and decode once we have all of them.
  bindScreen(ch) {
    this.screen = ch;
    ch.binaryType = 'arraybuffer';
    this._frameHdr = null;
    this._frameParts = [];
    ch.onmessage = async (e) => {
      if (typeof e.data === 'string') {
        try { this._frameHdr = JSON.parse(e.data); } catch { this._frameHdr = null; }
        this._frameParts = [];
        return;
      }
      const hdr = this._frameHdr;
      if (!hdr) return;
      this._frameParts.push(e.data);
      const need = hdr.parts && hdr.parts > 0 ? hdr.parts : 1;
      if (this._frameParts.length < need) return;
      const blob = new Blob(this._frameParts, {type: 'image/jpeg'});
      this._frameParts = [];
      try {
        const bmp = await createImageBitmap(blob);
        if (this.canvas.width !== bmp.width) this.canvas.width = bmp.width;
        if (this.canvas.height !== bmp.height) this.canvas.height = bmp.height;
        this.cctx.drawImage(bmp, 0, 0);
        bmp.close && bmp.close();
      } catch (err) {}
    };
  }

  // ---- input ----
  wireInput() {
    const cv = this.canvas;
    // The canvas fills its box with object-fit: contain, so the drawn image is
    // letterboxed inside the element. Map client coords to the actual image area.
    const toRemote = (ev) => {
      const r = cv.getBoundingClientRect();
      const iw = this.remoteW || cv.width || 1;
      const ih = this.remoteH || cv.height || 1;
      const scale = Math.min(r.width / iw, r.height / ih) || 1;
      const dispW = iw * scale, dispH = ih * scale;
      const offX = (r.width - dispW) / 2, offY = (r.height - dispH) / 2;
      let x = (ev.clientX - r.left - offX) / dispW;
      let y = (ev.clientY - r.top - offY) / dispH;
      x = Math.max(0, Math.min(1, x));
      y = Math.max(0, Math.min(1, y));
      return {mx: Math.round(x * iw), my: Math.round(y * ih)};
    };
    cv.addEventListener('mousemove', (ev) => {
      const p = toRemote(ev); this.sendCtrl({type: 'mouse_move', mx: p.mx, my: p.my});
    });
    cv.addEventListener('mousedown', (ev) => {
      ev.preventDefault(); cv.focus();
      const p = toRemote(ev);
      this.sendCtrl({type: 'mouse_button', btn: btnName(ev.button), down: true, mx: p.mx, my: p.my});
    });
    window.addEventListener('mouseup', (ev) => {
      if (!this.ctrl) return;
      const p = toRemote(ev);
      this.sendCtrl({type: 'mouse_button', btn: btnName(ev.button), down: false, mx: p.mx, my: p.my});
    });
    cv.addEventListener('contextmenu', (ev) => ev.preventDefault());
    cv.addEventListener('wheel', (ev) => {
      ev.preventDefault();
      this.sendCtrl({type: 'mouse_scroll', dx: Math.sign(ev.deltaX) * -1, dy: Math.sign(ev.deltaY) * -1});
    }, {passive: false});
    cv.addEventListener('keydown', (ev) => {
      ev.preventDefault();
      this.sendCtrl({type: 'key', code: ev.code, down: true, alt: ev.altKey, ctrl: ev.ctrlKey, shift: ev.shiftKey, meta: ev.metaKey});
    });
    cv.addEventListener('keyup', (ev) => {
      ev.preventDefault();
      this.sendCtrl({type: 'key', code: ev.code, down: false, alt: ev.altKey, ctrl: ev.ctrlKey, shift: ev.shiftKey, meta: ev.metaKey});
    });
    // Touch (Fold): map single-finger tap/drag to mouse.
    let touchDown = false;
    cv.addEventListener('touchstart', (ev) => {
      ev.preventDefault(); touchDown = true;
      const p = toRemote(ev.touches[0]);
      this.sendCtrl({type: 'mouse_move', mx: p.mx, my: p.my});
      this.sendCtrl({type: 'mouse_button', btn: 'left', down: true, mx: p.mx, my: p.my});
    }, {passive: false});
    cv.addEventListener('touchmove', (ev) => {
      ev.preventDefault();
      const p = toRemote(ev.touches[0]);
      this.sendCtrl({type: 'mouse_move', mx: p.mx, my: p.my});
    }, {passive: false});
    cv.addEventListener('touchend', (ev) => {
      ev.preventDefault();
      if (touchDown) this.sendCtrl({type: 'mouse_button', btn: 'left', down: false});
      touchDown = false;
    }, {passive: false});
  }

  // ---- files channel ----
  bindFiles(ch) {
    this.files = ch;
    ch.binaryType = 'arraybuffer';
    this.filesPendingHdr = null;
    ch.onmessage = (e) => this.onFileMessage(e);
  }
  wireFiles() {
    const stage = this.el.querySelector('.tile-stage');
    ['dragenter', 'dragover'].forEach(t =>
      stage.addEventListener(t, (e) => { e.preventDefault(); stage.classList.add('dragover'); }));
    ['dragleave', 'drop'].forEach(t =>
      stage.addEventListener(t, (e) => { e.preventDefault(); stage.classList.remove('dragover'); }));
    stage.addEventListener('drop', (e) => {
      const fl = e.dataTransfer.files;
      for (const f of fl) this.sendFile(f);
    });
    this.el.querySelector('.file-input').addEventListener('change', (e) => {
      for (const f of e.target.files) this.sendFile(f);
      e.target.value = '';
    });
  }
  sendFileMsg(obj) { if (this.files && this.files.readyState === 'open') this.files.send(JSON.stringify(obj)); }

  async sendFile(file) {
    if (!this.files || this.files.readyState !== 'open') { alert('file channel not ready'); return; }
    const tid = crypto.randomUUID();
    const row = this.xferRow(tid, file.name, 'to remote');
    this.sendFileMsg({type: 'offer', tid, name: file.name, size: file.size, dir: 'upload'});
    const CHUNK = 64 * 1024;
    let sent = 0;
    const reader = file.stream().getReader();
    // Simple backpressure: pause when the channel buffer gets large.
    const drain = () => new Promise(res => {
      if (this.files.bufferedAmount < 4 * 1024 * 1024) return res();
      this.files.onbufferedamountlow = () => { this.files.onbufferedamountlow = null; res(); };
      this.files.bufferedAmountLowThreshold = 1 * 1024 * 1024;
    });
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      for (let off = 0; off < value.length; off += CHUNK) {
        const slice = value.subarray(off, off + CHUNK);
        this.sendFileMsg({type: 'chunk', tid, offset: sent});
        this.files.send(slice);
        sent += slice.length;
        row.set(sent / file.size);
        await drain();
      }
    }
    this.sendFileMsg({type: 'done', tid});
    row.set(1); row.label('sent'); row.done();
  }

  onFileMessage(e) {
    if (typeof e.data === 'string') {
      let m; try { m = JSON.parse(e.data); } catch { return; }
      if (m.type === 'offer' && m.dir === 'download') {
        // Host is sending us a file (e.g. drag from host UI later, or reply).
        this.transfers.set(m.tid, {meta: m, chunks: [], received: 0, row: this.xferRow(m.tid, m.name, 'from remote')});
        this.sendFileMsg({type: 'accept', tid: m.tid});
      } else if (m.type === 'chunk') {
        this.filesPendingHdr = m;
      } else if (m.type === 'done') {
        const t = this.transfers.get(m.tid);
        if (t) { this.saveTransfer(t); this.transfers.delete(m.tid); }
      } else if (m.type === 'list_resp') {
        this.renderRemoteList(m.path, m.entries || []);
      } else if (m.type === 'file_err') {
        alert('file transfer error: ' + m.message);
      }
      return;
    }
    // Binary chunk for the last chunk header.
    const hdr = this.filesPendingHdr; this.filesPendingHdr = null;
    if (!hdr) return;
    const t = this.transfers.get(hdr.tid);
    if (!t) return;
    t.chunks.push(new Uint8Array(e.data));
    t.received += e.data.byteLength;
    if (t.meta.size) t.row.set(t.received / t.meta.size);
  }
  saveTransfer(t) {
    const blob = new Blob(t.chunks);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = t.meta.name; a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    t.row.label('downloaded'); t.row.done();
  }
  xferRow(tid, name, dir) {
    const list = this.el.querySelector('.xfer-list');
    const row = document.createElement('div');
    row.className = 'xfer';
    row.innerHTML = `<div><b>${escapeHtml(name)}</b> <span class="x-dir">${dir}</span></div><div class="bar"><i></i></div>`;
    list.prepend(row);
    const bar = row.querySelector('.bar i');
    const dirEl = row.querySelector('.x-dir');
    return {
      set: (frac) => { bar.style.width = Math.round(frac * 100) + '%'; },
      label: (t) => { dirEl.textContent = t; },
      // Fade the row out a few seconds after it finishes, so completed
      // transfers do not accumulate into a permanent list.
      done: () => setTimeout(() => row.remove(), 4000),
    };
  }

  // ---- remote file browser (download from the remote machine) ----
  refreshRemoteList(path) {
    this.remotePath = path || '';
    this.sendFileMsg({type: 'list', path: this.remotePath});
  }
  renderRemoteList(path, entries) {
    const box = this.el.querySelector('.remote-list');
    if (!box) return;
    this.remotePath = path || '';
    const sep = path && path.indexOf('\\') >= 0 ? '\\' : '/';
    box.innerHTML = '';
    const head = document.createElement('div');
    head.className = 'rl-head';
    head.textContent = 'Remote: ' + (path || '(downloads)');
    box.appendChild(head);
    // Up a level
    const up = document.createElement('div');
    up.className = 'rl-row rl-dir';
    up.textContent = '.. up';
    up.onclick = () => {
      const p = this.remotePath.replace(/[\\/]+$/, '');
      const parent = p.substring(0, p.lastIndexOf(sep));
      this.refreshRemoteList(parent || '');
    };
    box.appendChild(up);
    entries.sort((a, b) => (b.dir - a.dir) || a.name.localeCompare(b.name));
    for (const en of entries) {
      const row = document.createElement('div');
      row.className = 'rl-row' + (en.dir ? ' rl-dir' : '');
      const full = (this.remotePath ? this.remotePath.replace(/[\\/]+$/, '') + sep : '') + en.name;
      if (en.dir) {
        row.textContent = '[' + en.name + ']';
        row.onclick = () => this.refreshRemoteList(full);
      } else {
        row.innerHTML = `<span>${escapeHtml(en.name)}</span><button class="rl-dl">download</button>`;
        // Ask the host to send this file; its "offer" reply drives the download.
        row.querySelector('.rl-dl').onclick = () => this.sendFileMsg({type: 'get', path: full});
      }
      box.appendChild(row);
    }
  }

  // ---- remote command ----
  openCmd() {
    const cmd = prompt('Run a command on ' + this.device.name + ' (e.g. taskkill /f /im notepad.exe):');
    if (!cmd) return;
    this.sendCtrl({type: 'exec', cmd});
    this.showCmdOutput('Running: ' + cmd + '\n...');
  }
  showCmdOutput(text) {
    let m = document.getElementById('cmd-out-modal');
    if (!m) {
      m = document.createElement('div');
      m.id = 'cmd-out-modal';
      m.className = 'modal';
      m.innerHTML = `<div class="modal-card" style="width:560px">
        <div class="modal-head"><span>Command output</span><button class="files-close" id="cmd-out-close">close</button></div>
        <pre id="cmd-out-body" style="margin:0;padding:14px;max-height:60vh;overflow:auto;white-space:pre-wrap;font-size:12.5px;color:var(--text)"></pre></div>`;
      document.body.appendChild(m);
      m.querySelector('#cmd-out-close').onclick = () => m.classList.add('hidden');
      m.addEventListener('click', (e) => { if (e.target === m) m.classList.add('hidden'); });
    }
    m.classList.remove('hidden');
    m.querySelector('#cmd-out-body').textContent = text;
  }

  // ---- clipboard ----
  // Remote -> local is automatic (see clipboard_tx above). This sends the
  // local clipboard to the remote machine on demand.
  async sendClipboardToRemote() {
    try {
      const t = await navigator.clipboard.readText();
      this.sendCtrl({type: 'clipboard_rx', text: t});
      toast('Clipboard sent to ' + this.device.name);
    } catch (e) {
      toast('Could not read the local clipboard (browser permission).');
    }
  }

  close(tellPeer) {
    if (tellPeer) signal({type: 'bye', session_id: this.id, host_id: this.peerId});
    try { this.pc && this.pc.close(); } catch {}
    sessions.delete(this.id);
    peers.delete(this.id);
    this.el.remove();
    updateGrid();
  }
}

function btnName(b) { return b === 2 ? 'right' : b === 1 ? 'middle' : 'left'; }

// =====================================================================
//  Device list + grid
// =====================================================================
function renderDevices() {
  const ul = document.getElementById('device-list');
  ul.innerHTML = '';
  const arr = [...devices.values()].sort((a, b) => a.name.localeCompare(b.name));
  document.getElementById('device-hint').style.display = arr.length ? 'none' : 'block';
  for (const d of arr) {
    const li = document.createElement('li');
    li.className = 'device' + (d.online ? ' online' : '') + (d.owner !== API.username ? ' cross-user' : '');
    li.innerHTML = `<span class="dot"></span>
      <span class="d-main"><span class="d-name">${escapeHtml(d.name)}</span><br>
      <span class="d-meta">${escapeHtml(d.platform || '')}${d.owner !== API.username ? ' &middot; ' + escapeHtml(d.owner) : ''}</span></span>
      <span class="d-actions">
        <button class="d-restart" title="Restart the CtrlFreak agent on this machine">&#8635;</button>
        <button class="d-reboot" title="Reboot this machine">&#9211;</button>
      </span>`;
    li.querySelector('.d-main').onclick = () => openSession(d);
    li.querySelector('.d-restart').onclick = (e) => {
      e.stopPropagation();
      if (confirm(`Restart the CtrlFreak agent on "${d.name}"?\n\nThis bounces the agent (a few seconds) without rebooting the machine. Try this first for a stuck connection.`))
        sendHostCommand(d, 'restart_agent');
    };
    li.querySelector('.d-reboot').onclick = (e) => {
      e.stopPropagation();
      if (confirm(`REBOOT "${d.name}"?\n\nThe machine restarts in 10 seconds and open apps are force-closed. It will come back online on its own.`))
        sendHostCommand(d, 'reboot');
    };
    ul.appendChild(li);
  }
  // Keep the file-manager machine pickers in sync with who is online.
  if (typeof FileMgr !== 'undefined') FileMgr.refreshDevices();
}

// sendHostCommand sends a reboot / restart-agent over the signaling link. It does
// not need an open session, which is the whole point: use it when a session is
// refusing or frozen.
function sendHostCommand(device, cmd) {
  if (!ws || ws.readyState !== 1) { alert('Not connected to the relay right now.'); return; }
  if (!device.online) { alert(`"${device.name}" is offline, so it cannot receive the command.`); return; }
  signal({type: 'command', host_id: device.id, command: cmd});
  const label = cmd === 'reboot' ? 'Reboot' : 'Restart agent';
  toast(`${label} sent to ${device.name}.`);
}

// toast shows a brief status line at the bottom of the screen.
function toast(text) {
  let t = document.getElementById('cf-toast');
  if (!t) {
    t = document.createElement('div');
    t.id = 'cf-toast';
    document.body.appendChild(t);
  }
  t.textContent = text;
  t.classList.add('show');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => t.classList.remove('show'), 3000);
}

function openSession(device) {
  if (sessions.size >= MAX_SESSIONS) {
    alert(`This client allows ${MAX_SESSIONS} simultaneous session${MAX_SESSIONS > 1 ? 's' : ''}.`);
    return;
  }
  // Avoid opening a duplicate session to the same host.
  for (const s of sessions.values()) if (s.peerId === device.id) return;
  const s = new Session(device);
  sessions.set(s.id, s);
  s.makePC();
  s.start();
  updateGrid();
}

function updateGrid() {
  const grid = document.getElementById('session-grid');
  const n = sessions.size;
  grid.classList.toggle('n2', n === 2);
  grid.classList.toggle('n3', n >= 3);
  document.getElementById('session-count').textContent = `${n}/${MAX_SESSIONS}`;
}

// =====================================================================
//  Admin
// =====================================================================
async function refreshAdmin() {
  try {
    const [{users}, {audit}] = await Promise.all([API.adminUsers(), API.adminAudit()]);
    const tb = document.querySelector('#users-table tbody');
    tb.innerHTML = '';
    for (const u of users) {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${escapeHtml(u.username)}</td>
        <td><span class="badge ${u.is_admin ? 'admin' : ''}">${u.is_admin ? 'admin' : 'user'}</span></td>
        <td>${u.must_change ? 'must reset pw' : 'active'}</td>
        <td></td>`;
      const actions = tr.lastElementChild;
      if (u.username !== API.username) {
        const reset = mkBtn('Reset pw', async () => {
          const p = prompt(`Temporary password for ${u.username} (min 8 chars):`);
          if (!p) return;
          try { await API.adminReset(u.username, p); alert('Password reset. User must set a new one at next login.'); refreshAdmin(); }
          catch (e) { alert(e.message); }
        });
        const role = mkBtn(u.is_admin ? 'Make user' : 'Make admin', async () => {
          try { await API.adminRole(u.username, !u.is_admin); refreshAdmin(); } catch (e) { alert(e.message); }
        });
        const del = mkBtn('Delete', async () => {
          if (!confirm(`Delete ${u.username}? This removes their account and hosts.`)) return;
          try { await API.adminDelete(u.username); refreshAdmin(); } catch (e) { alert(e.message); }
        }, true);
        actions.append(reset, role, del);
      } else {
        actions.textContent = '(you)';
      }
      tb.appendChild(tr);
    }
    const at = document.querySelector('#audit-table tbody');
    at.innerHTML = '';
    for (const e of (audit || [])) {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td>${new Date(e.ts).toLocaleString()}</td><td>${escapeHtml(e.actor)}</td>
        <td>${escapeHtml(e.action)}</td><td>${escapeHtml(e.target || '')}</td><td>${escapeHtml(e.detail || '')}</td>`;
      at.appendChild(tr);
    }
  } catch (e) { /* not admin or expired */ }
}
function mkBtn(label, fn, danger) {
  const b = document.createElement('button');
  b.textContent = label; if (danger) b.className = 'danger'; b.onclick = fn;
  return b;
}

// =====================================================================
//  Wiring
// =====================================================================
document.getElementById('login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = document.getElementById('login-error'); err.textContent = '';
  try {
    const res = await API.login(document.getElementById('login-user').value.trim(),
                                document.getElementById('login-pass').value);
    API.token = res.token; localStorage.setItem('cf_token', res.token);
    API.isAdmin = res.is_admin;
    API.username = document.getElementById('login-user').value.trim();
    if (res.must_change) { show('change'); return; }
    enterApp();
  } catch (ex) { err.textContent = ex.message || 'login failed'; }
});

document.getElementById('change-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = document.getElementById('change-error'); err.textContent = '';
  const oldp = document.getElementById('change-old').value;
  const n1 = document.getElementById('change-new').value;
  const n2 = document.getElementById('change-new2').value;
  if (n1 !== n2) { err.textContent = 'passwords do not match'; return; }
  try { await API.changePassword(oldp, n1); enterApp(); }
  catch (ex) { err.textContent = ex.message; }
});

document.getElementById('logout').onclick = logout;
document.getElementById('nav-devices').onclick = () => switchPanel('devices');
document.getElementById('nav-files').onclick = () => { switchPanel('files'); FileMgr.init(); FileMgr.refreshDevices(); };
document.getElementById('nav-admin').onclick = () => { switchPanel('admin'); refreshAdmin(); };

// Account: self-service password change for every user, admin included.
const accountModal = document.getElementById('account-modal');
document.getElementById('nav-account').onclick = () => {
  document.getElementById('account-form').reset();
  document.getElementById('account-msg').textContent = '';
  accountModal.classList.remove('hidden');
};
document.getElementById('account-close').onclick = () => accountModal.classList.add('hidden');
accountModal.addEventListener('click', (e) => { if (e.target === accountModal) accountModal.classList.add('hidden'); });
document.getElementById('account-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const msg = document.getElementById('account-msg'); msg.className = 'msg'; msg.textContent = '';
  const oldp = document.getElementById('acct-old').value;
  const n1 = document.getElementById('acct-new').value;
  const n2 = document.getElementById('acct-new2').value;
  if (n1 !== n2) { msg.className = 'msg err'; msg.textContent = 'New passwords do not match.'; return; }
  if (n1.length < 8) { msg.className = 'msg err'; msg.textContent = 'New password must be at least 8 characters.'; return; }
  try {
    await API.changePassword(oldp, n1);
    msg.className = 'msg ok'; msg.textContent = 'Password updated.';
    setTimeout(() => accountModal.classList.add('hidden'), 900);
  } catch (ex) { msg.className = 'msg err'; msg.textContent = ex.message; }
});

document.getElementById('create-user-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const msg = document.getElementById('create-user-msg'); msg.className = 'msg'; msg.textContent = '';
  try {
    await API.adminCreate({
      username: document.getElementById('cu-name').value.trim(),
      password: document.getElementById('cu-pass').value,
      is_admin: document.getElementById('cu-admin').checked,
    });
    msg.className = 'msg ok'; msg.textContent = 'User created.';
    e.target.reset(); refreshAdmin();
  } catch (ex) { msg.className = 'msg err'; msg.textContent = ex.message; }
});

function switchPanel(which) {
  document.getElementById('devices-panel').classList.toggle('hidden', which !== 'devices');
  document.getElementById('files-panel').classList.toggle('hidden', which !== 'files');
  document.getElementById('admin-panel').classList.toggle('hidden', which !== 'admin');
  document.getElementById('nav-devices').classList.toggle('active', which === 'devices');
  document.getElementById('nav-files').classList.toggle('active', which === 'files');
  document.getElementById('nav-admin').classList.toggle('active', which === 'admin');
}

async function enterApp() {
  try {
    const me = await API.me();
    API.username = me.username; API.isAdmin = me.is_admin;
    // Laptop uses the server-configured cap; phones stay at 1.
    if (!IS_MOBILE && me.max_sessions > 0) MAX_SESSIONS = me.max_sessions;
  } catch { logout(); return; }
  document.getElementById('who').textContent = API.username + (API.isAdmin ? ' (admin)' : '');
  document.getElementById('nav-admin').classList.toggle('hidden', !API.isAdmin);
  document.getElementById('session-count').textContent = `0/${MAX_SESSIONS}`;
  show('main'); switchPanel('devices');
  try { const {ice_servers} = await API.ice(); if (ice_servers && ice_servers.length) iceServers = ice_servers; } catch {}
  connectSignaling();
}

function logout() {
  API.token = ''; localStorage.removeItem('cf_token');
  for (const s of sessions.values()) s.close(true);
  if (ws) { ws.onclose = null; ws.close(); ws = null; }
  show('login');
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
}

// =====================================================================
//  File manager (dual pane, machine <-> machine)
// =====================================================================
//
// A FileConn is a lightweight session to one machine used only for browsing and
// transferring files: it opens the same ctrl+files channels as a screen session
// but immediately pauses video, so no bandwidth is wasted on a picture nobody is
// looking at. Two panes each hold a FileConn; a transfer streams the file from
// the source machine down to the browser and straight back up to the
// destination machine (both are agents, so both filesystems are browsable).

class FileConn {
  constructor(device) {
    this.id = crypto.randomUUID();
    peers.set(this.id, this);
    this.device = device;
    this.peerId = device.id;
    this.pc = null; this.ctrl = null; this.files = null;
    this.onList = null; this.onErrorCb = null; this.readyCb = null; this.onState = null;
    this.pendingHdr = null;
    this.dl = null; // active download {sink, resolve, reject, received, size, onProgress}
  }
  start() { this.makePC(); signal({type: 'connect', session_id: this.id, host_id: this.peerId}); }
  makePC() {
    this.pc = new RTCPeerConnection({iceServers});
    this.pc.onicecandidate = (e) => {
      if (e.candidate) signal({type: 'candidate', session_id: this.id, host_id: this.peerId, candidate: e.candidate});
    };
    this.pc.onconnectionstatechange = () => {
      const st = this.pc.connectionState;
      if (this.onState) this.onState(st);
    };
    this.pc.ondatachannel = (e) => {
      const ch = e.channel;
      if (ch.label === 'ctrl') this.bindCtrl(ch);
      else if (ch.label === 'files') this.bindFiles(ch);
      else if (ch.label === 'video') { try { ch.onmessage = () => {}; } catch (x) {} }
    };
  }
  async onOffer(msg) {
    if (!this.pc) this.makePC();
    this.peerId = msg.host_id || this.peerId;
    await this.pc.setRemoteDescription({type: 'offer', sdp: msg.sdp});
    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    signal({type: 'answer', session_id: this.id, host_id: this.peerId, sdp: answer.sdp});
  }
  async onCandidate(msg) { if (this.pc && msg.candidate) { try { await this.pc.addIceCandidate(msg.candidate); } catch (e) {} } }
  onError(m) { if (this.onErrorCb) this.onErrorCb(m); if (this.dl && this.dl.reject) { this.dl.reject(new Error(m)); this.dl = null; } }

  bindCtrl(ch) {
    this.ctrl = ch;
    ch.onopen = () => { try { ch.send(JSON.stringify({type: 'pause', paused: true})); } catch (e) {} };
    ch.onmessage = () => {}; // ignore screen_info etc.
  }
  bindFiles(ch) {
    this.files = ch; ch.binaryType = 'arraybuffer'; this.pendingHdr = null;
    ch.onopen = () => { if (this.readyCb) this.readyCb(); };
    ch.onmessage = (e) => this.onFiles(e);
  }
  sendFiles(obj) { if (this.files && this.files.readyState === 'open') this.files.send(JSON.stringify(obj)); }

  list(path) { this.sendFiles({type: 'list', path: path || ''}); }
  drives() { this.sendFiles({type: 'drives'}); }

  onFiles(e) {
    if (typeof e.data === 'string') {
      let m; try { m = JSON.parse(e.data); } catch { return; }
      if (m.type === 'list_resp') { if (this.onList) this.onList(m.path || '', m.parent || '', m.entries || []); }
      else if (m.type === 'file_err') { this.onError(m.message || 'error'); }
      else if (m.type === 'offer' && m.dir === 'download') { this.sendFiles({type: 'accept', tid: m.tid}); }
      else if (m.type === 'chunk') { this.pendingHdr = m; }
      else if (m.type === 'done') { if (this.dl) { const d = this.dl; this.dl = null; if (d.resolve) d.resolve(); } }
      return;
    }
    const hdr = this.pendingHdr; this.pendingHdr = null;
    if (!hdr || !this.dl) return;
    const u = new Uint8Array(e.data);
    this.dl.sink(u);
    this.dl.received += u.length;
    if (this.dl.onProgress && this.dl.size) this.dl.onProgress(this.dl.received / this.dl.size);
  }

  // getToSink downloads one file, streaming each chunk to sink(Uint8Array).
  // One download at a time per connection.
  getToSink(path, size, sink, onProgress) {
    return new Promise((resolve, reject) => {
      this.dl = {sink, resolve, reject, received: 0, size: size || 0, onProgress};
      this.sendFiles({type: 'get', path});
    });
  }

  beginUpload(tid, name, size, destDir) { this.sendFiles({type: 'offer', tid, name, size, dir: 'upload', dest: destDir || ''}); }
  async uploadChunk(tid, chunk) {
    this.sendFiles({type: 'chunk', tid});
    this.files.send(chunk);
    if (this.files.bufferedAmount > 4 * 1024 * 1024) {
      await new Promise(res => {
        this.files.bufferedAmountLowThreshold = 1 * 1024 * 1024;
        this.files.onbufferedamountlow = () => { this.files.onbufferedamountlow = null; res(); };
      });
    }
  }
  endUpload(tid) { this.sendFiles({type: 'done', tid}); }

  async uploadFile(file, destDir, onProgress) {
    const tid = crypto.randomUUID();
    this.beginUpload(tid, file.name, file.size, destDir);
    const reader = file.stream().getReader(); const CH = 64 * 1024; let sent = 0;
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      for (let off = 0; off < value.length; off += CH) {
        await this.uploadChunk(tid, value.subarray(off, off + CH));
        sent += Math.min(CH, value.length - off);
        if (onProgress && file.size) onProgress(sent / file.size);
      }
    }
    this.endUpload(tid);
  }

  close() { peers.delete(this.id); try { this.pc && this.pc.close(); } catch (e) {} this.files = null; this.ctrl = null; }
}

// pipeTransfer moves one file from srcConn to dstConn's destDir, going through
// the browser. A queue serializes the upload so chunk headers and bodies never
// interleave (which would corrupt the file on the receiving host).
async function pipeTransfer(srcConn, srcPath, name, size, dstConn, destDir, onProgress) {
  const tid = crypto.randomUUID();
  dstConn.beginUpload(tid, name, size, destDir);
  const queue = []; let done = false; let notify = null; let received = 0;
  const wake = () => { if (notify) { const n = notify; notify = null; n(); } };
  const sink = (chunk) => {
    queue.push(chunk); received += chunk.length;
    if (onProgress && size) onProgress(received / size);
    wake();
  };
  const finished = srcConn.getToSink(srcPath, size, sink).then(() => { done = true; wake(); });
  while (true) {
    if (queue.length) { await dstConn.uploadChunk(tid, queue.shift()); continue; }
    if (done) break;
    await new Promise(res => { notify = res; });
  }
  await finished;
  dstConn.endUpload(tid);
}

function fmtSize(n) {
  if (!n) return '';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)) + ' ' + u[i];
}
function prettyName(name) { return name.replace(/\\$/, ''); }

class Pane {
  constructor(side) {
    this.side = side;
    this.root = document.querySelector(`.fm-pane[data-side="${side}"]`);
    this.sel = this.root.querySelector('.fm-device');
    this.listEl = this.root.querySelector('.fm-list');
    this.pathEl = this.root.querySelector('.fm-path');
    this.xferEl = this.root.querySelector('.fm-xfer');
    this.conn = null; this.path = ''; this.parent = ''; this.selected = null;

    this.sel.onchange = () => this.connect();
    this.root.querySelector('.fm-drives').onclick = () => { if (this.conn) this.conn.drives(); };
    this.root.querySelector('.fm-up').onclick = () => this.goUp();
    this.root.querySelector('.fm-refresh').onclick = () => this.reload();
    this.root.querySelector('.fm-copy').onclick = () => this.copyToOther();

    ['dragenter', 'dragover'].forEach(t => this.listEl.addEventListener(t, e => { e.preventDefault(); this.listEl.classList.add('drop'); }));
    ['dragleave', 'drop'].forEach(t => this.listEl.addEventListener(t, e => { e.preventDefault(); this.listEl.classList.remove('drop'); }));
    this.listEl.addEventListener('drop', e => this.onDrop(e));
    this.fillDevices();
  }
  other() { return FileMgr.panes[this.side === 'L' ? 'R' : 'L']; }
  sideLabel() { return this.side === 'L' ? 'left' : 'right'; }

  fillDevices() {
    const cur = this.sel.value;
    this.sel.innerHTML = '<option value="">— pick a machine —</option>';
    [...devices.values()].filter(d => d.online).sort((a, b) => a.name.localeCompare(b.name)).forEach(d => {
      const o = document.createElement('option');
      o.value = d.id; o.textContent = d.name + (d.owner !== API.username ? ` (${d.owner})` : '');
      this.sel.appendChild(o);
    });
    if (cur && devices.has(cur)) this.sel.value = cur;
  }
  connect() {
    if (this.conn) { this.conn.close(); this.conn = null; }
    this.selected = null; this.path = ''; this.parent = '';
    const id = this.sel.value;
    if (!id) { this.listEl.innerHTML = ''; this.pathEl.textContent = ''; return; }
    const dev = devices.get(id); if (!dev) return;
    this.pathEl.textContent = 'connecting…';
    this.conn = new FileConn(dev);
    this.conn.onList = (p, par, ents) => this.render(p, par, ents);
    this.conn.onErrorCb = (m) => toast(dev.name + ': ' + m);
    this.conn.readyCb = () => this.conn.drives();
    this.conn.onState = (st) => { if (st === 'failed' || st === 'disconnected') this.pathEl.textContent = 'disconnected'; };
    this.conn.start();
  }
  reload() { if (this.conn) { if (this.path) this.conn.list(this.path); else this.conn.drives(); } }
  goUp() { if (this.conn) { if (this.parent) this.conn.list(this.parent); else this.conn.drives(); } }
  joinPath(path, name) {
    if (!path) return name; // drives view: name is already a full root
    const sep = path.indexOf('\\') >= 0 ? '\\' : '/';
    return path.replace(/[\\/]+$/, '') + sep + name;
  }
  render(path, parent, entries) {
    this.path = path; this.parent = parent; this.selected = null;
    this.pathEl.textContent = path || 'Drives';
    entries.sort((a, b) => (b.dir - a.dir) || a.name.localeCompare(b.name));
    this.listEl.innerHTML = '';
    for (const en of entries) {
      const row = document.createElement('div');
      row.className = 'fm-row' + (en.dir ? ' dir' : '');
      const full = this.joinPath(path, en.name);
      if (en.dir) {
        row.innerHTML = `<span class="fm-ic">&#128193;</span><span class="fm-nm">${escapeHtml(prettyName(en.name))}</span>`;
        row.onclick = () => this.conn.list(full);
      } else {
        row.innerHTML = `<span class="fm-ic">&#128196;</span><span class="fm-nm">${escapeHtml(en.name)}</span><span class="fm-sz">${fmtSize(en.size)}</span>`;
        row.draggable = true;
        row.onclick = () => this.select(row, {full, name: en.name, size: en.size});
        row.ondblclick = () => this.downloadLocal(full, en.name, en.size);
        row.addEventListener('dragstart', (e) => {
          e.dataTransfer.setData('text/cf', JSON.stringify({side: this.side, full, name: en.name, size: en.size}));
        });
      }
      this.listEl.appendChild(row);
    }
  }
  select(row, f) {
    this.listEl.querySelectorAll('.fm-row.sel').forEach(r => r.classList.remove('sel'));
    row.classList.add('sel'); this.selected = f;
  }
  copyToOther() {
    const o = this.other();
    if (!this.selected) { toast('Pick a file on the ' + this.sideLabel() + ' first.'); return; }
    if (!o.conn || !o.path) { toast('Open a destination folder on the ' + o.sideLabel() + '.'); return; }
    this.doTransfer(this.selected, o);
  }
  async doTransfer(file, destPane) {
    if (!this.conn || !destPane.conn || !destPane.path) { toast('Open a destination folder first.'); return; }
    const row = this.xrow(file.name, '→ ' + destPane.sideLabel());
    try {
      await pipeTransfer(this.conn, file.full, file.name, file.size, destPane.conn, destPane.path, f => row.set(f));
      row.set(1); row.label('done'); row.done();
      destPane.reload();
    } catch (e) { row.label('failed'); row.done(); toast('Transfer failed: ' + e.message); }
  }
  async onDrop(e) {
    const cf = e.dataTransfer.getData('text/cf');
    if (cf) {
      let f; try { f = JSON.parse(cf); } catch { return; }
      const src = FileMgr.panes[f.side];
      if (src && src !== this) src.doTransfer({full: f.full, name: f.name, size: f.size}, this);
      return;
    }
    if (!this.conn || !this.path) { toast('Open a folder on this side first.'); return; }
    for (const file of e.dataTransfer.files) {
      const row = this.xrow(file.name, 'upload');
      try { await this.conn.uploadFile(file, this.path, f => row.set(f)); row.set(1); row.label('done'); row.done(); this.reload(); }
      catch (err) { row.label('failed'); row.done(); }
    }
  }
  downloadLocal(full, name, size) {
    if (!this.conn) return;
    const chunks = []; const row = this.xrow(name, 'to this device');
    this.conn.getToSink(full, size, c => chunks.push(c), f => row.set(f)).then(() => {
      const blob = new Blob(chunks); const url = URL.createObjectURL(blob);
      const a = document.createElement('a'); a.href = url; a.download = name; a.click();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
      row.set(1); row.label('downloaded'); row.done();
    }).catch(() => { row.label('failed'); row.done(); });
  }
  xrow(name, dir) {
    const row = document.createElement('div'); row.className = 'fm-x';
    row.innerHTML = `<div class="fm-xt"><span class="fm-xn">${escapeHtml(name)}</span><span class="fm-xd">${dir}</span></div><div class="bar"><i></i></div>`;
    this.xferEl.prepend(row);
    const bar = row.querySelector('.bar i'); const d = row.querySelector('.fm-xd');
    return {
      set: (f) => { bar.style.width = Math.round(f * 100) + '%'; },
      label: (t) => { d.textContent = t; },
      done: () => setTimeout(() => row.remove(), 4000),
    };
  }
  destroy() { if (this.conn) { this.conn.close(); this.conn = null; } }
}

const FileMgr = {
  panes: {},
  ready: false,
  init() {
    if (this.ready) return;
    this.panes.L = new Pane('L');
    this.panes.R = new Pane('R');
    this.ready = true;
  },
  refreshDevices() { if (this.ready) { this.panes.L.fillDevices(); this.panes.R.fillDevices(); } },
};

// Auto-resume if we still have a token.
if (API.token) enterApp(); else show('login');
