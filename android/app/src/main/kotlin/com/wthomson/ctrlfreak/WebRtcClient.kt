package com.wthomson.ctrlfreak

import android.content.Context
import com.google.gson.Gson
import org.webrtc.DataChannel
import org.webrtc.IceCandidate
import org.webrtc.MediaConstraints
import org.webrtc.MediaStream
import org.webrtc.PeerConnection
import org.webrtc.RtpReceiver
import org.webrtc.SdpObserver
import org.webrtc.SessionDescription
import java.nio.ByteBuffer
import java.nio.charset.StandardCharsets

/**
 * One remote-control session. The host is the WebRTC offerer; this client answers
 * and then receives screen frames and sends input over data channels.
 */
class WebRtcClient(
    ctx: Context,
    iceServers: List<IceServer>,
    val sessionId: String,
    private var peerId: String,          // host connection id
    private val cb: Callbacks,
) {
    interface Callbacks {
        fun onScreenInfo(w: Int, h: Int, monitors: List<MonitorDim>)
        fun onFrame(jpeg: ByteArray)
        fun onState(state: String)
    }

    private val gson = Gson()
    private val factory = Rtc.factory(ctx)
    private var pc: PeerConnection? = null

    private var ctrl: DataChannel? = null
    private var screen: DataChannel? = null

    // The screen channel sends a text header then the binary JPEG; remember the
    // last header we saw so the following binary message is interpreted with it.
    private var pendingHeader: DataMsg? = null

    // Quality prefs pushed to the host once ctrl opens. Full resolution (scale
    // 100) keeps the remote screen legible on the Fold; the host chunks large
    // frames and we reassemble them below.
    var fps = 15
    var scale = 100
    var quality = 70

    // Declared before init so it is available when the peer connection is created.
    private val observer = object : PeerConnection.Observer {
        override fun onSignalingChange(state: PeerConnection.SignalingState?) {}
        override fun onIceConnectionChange(state: PeerConnection.IceConnectionState?) {}
        override fun onIceConnectionReceivingChange(receiving: Boolean) {}
        override fun onIceGatheringChange(state: PeerConnection.IceGatheringState?) {}
        override fun onIceCandidate(candidate: IceCandidate) {
            SignalClient.send(
                Signal(
                    type = Sig.CANDIDATE, sessionId = sessionId, hostId = peerId,
                    candidate = IceCandidateMsg(candidate.sdp, candidate.sdpMid, candidate.sdpMLineIndex)
                )
            )
        }
        override fun onIceCandidatesRemoved(candidates: Array<out IceCandidate>?) {}
        override fun onAddStream(stream: MediaStream?) {}
        override fun onRemoveStream(stream: MediaStream?) {}
        override fun onDataChannel(dc: DataChannel) { bindChannel(dc) }
        override fun onRenegotiationNeeded() {}
        override fun onAddTrack(receiver: RtpReceiver?, streams: Array<out MediaStream>?) {}
        override fun onConnectionChange(newState: PeerConnection.PeerConnectionState?) {
            cb.onState(newState?.name?.lowercase() ?: "")
        }
    }

    init {
        val cfg = PeerConnection.RTCConfiguration(iceServers.map { s ->
            val b = PeerConnection.IceServer.builder(s.urls)
            if (!s.username.isNullOrEmpty()) b.setUsername(s.username)
            if (!s.credential.isNullOrEmpty()) b.setPassword(s.credential)
            b.createIceServer()
        })
        cfg.sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN
        pc = factory.createPeerConnection(cfg, observer)
    }

    /** Ask the relay to connect us to the host; the host will answer with an offer. */
    fun start() {
        cb.onState("connecting")
        SignalClient.send(Signal(type = Sig.CONNECT, sessionId = sessionId, hostId = peerId))
    }

    /** Host's SDP offer arrived; set it, create our answer, send it back. */
    fun onRemoteOffer(sig: Signal) {
        peerId = sig.hostId ?: peerId
        val offer = SessionDescription(SessionDescription.Type.OFFER, sig.sdp ?: return)
        pc?.setRemoteDescription(object : SimpleSdp() {
            override fun onSetSuccess() {
                pc?.createAnswer(object : SimpleSdp() {
                    override fun onCreateSuccess(desc: SessionDescription) {
                        pc?.setLocalDescription(SimpleSdp(), desc)
                        SignalClient.send(
                            Signal(type = Sig.ANSWER, sessionId = sessionId, hostId = peerId, sdp = desc.description)
                        )
                    }
                }, MediaConstraints())
            }
        }, offer)
    }

    fun onRemoteCandidate(sig: Signal) {
        val c = sig.candidate ?: return
        pc?.addIceCandidate(IceCandidate(c.sdpMid, c.sdpMLineIndex, c.candidate))
    }

    fun close() {
        try { ctrl?.close() } catch (_: Exception) {}
        try { screen?.close() } catch (_: Exception) {}
        try { pc?.close() } catch (_: Exception) {}
        SignalClient.send(Signal(type = Sig.BYE, sessionId = sessionId, hostId = peerId))
        pc = null
    }

    // ---- sending input ----

    private fun sendCtrl(m: DataMsg) {
        val dc = ctrl ?: return
        val bytes = gson.toJson(m).toByteArray(StandardCharsets.UTF_8)
        dc.send(DataChannel.Buffer(ByteBuffer.wrap(bytes), false))
    }

    fun sendQuality() = sendCtrl(DataMsg(type = DM.SET_QUALITY, fps = fps, scale = scale, quality = quality))
    fun mouseMove(x: Int, y: Int) = sendCtrl(DataMsg(type = DM.MOUSE_MOVE, mx = x, my = y))
    fun mouseButton(btn: String, down: Boolean, x: Int, y: Int) =
        sendCtrl(DataMsg(type = DM.MOUSE_BUTTON, btn = btn, down = down, mx = x, my = y))
    fun scroll(dx: Int, dy: Int) = sendCtrl(DataMsg(type = DM.MOUSE_SCROLL, dx = dx, dy = dy))
    fun key(code: String, down: Boolean) = sendCtrl(DataMsg(type = DM.KEY, code = code, down = down))
    fun typeText(text: String) = sendCtrl(DataMsg(type = DM.TYPE_TEXT, text = text))
    fun selectMonitor(index: Int) = sendCtrl(DataMsg(type = DM.SELECT_MON, mon = index))

    // ---- channel wiring ----

    private fun bindChannel(dc: DataChannel) {
        when (dc.label()) {
            "ctrl" -> {
                ctrl = dc
                dc.registerObserver(object : DataChannel.Observer {
                    override fun onBufferedAmountChange(previousAmount: Long) {}
                    override fun onStateChange() {
                        if (dc.state() == DataChannel.State.OPEN) sendQuality()
                    }
                    override fun onMessage(buffer: DataChannel.Buffer) {
                        if (buffer.binary) return
                        val text = readText(buffer.data)
                        val m = try { gson.fromJson(text, DataMsg::class.java) } catch (e: Exception) { return }
                        if (m.type == DM.SCREEN_INFO) cb.onScreenInfo(m.w, m.h, m.monitors ?: emptyList())
                    }
                })
            }
            "video" -> {
                screen = dc
                dc.registerObserver(object : DataChannel.Observer {
                    // A frame arrives as a JSON header (with "parts": N) followed
                    // by N binary chunks. At full resolution a JPEG exceeds the
                    // data-channel message limit, so the host splits it; we collect
                    // the chunks and only decode once we have the whole frame.
                    private val parts = ArrayList<ByteArray>()
                    private var need = 0
                    override fun onBufferedAmountChange(previousAmount: Long) {}
                    override fun onStateChange() {}
                    override fun onMessage(buffer: DataChannel.Buffer) {
                        if (!buffer.binary) {
                            val text = readText(buffer.data)
                            val hdr = try { gson.fromJson(text, DataMsg::class.java) } catch (e: Exception) { null }
                            pendingHeader = hdr
                            parts.clear()
                            need = if (hdr != null && hdr.parts > 0) hdr.parts else 1
                            return
                        }
                        if (pendingHeader == null) return
                        val bytes = ByteArray(buffer.data.remaining())
                        buffer.data.get(bytes)
                        parts.add(bytes)
                        if (parts.size < need) return
                        var total = 0
                        for (p in parts) total += p.size
                        val full = ByteArray(total)
                        var off = 0
                        for (p in parts) { System.arraycopy(p, 0, full, off, p.size); off += p.size }
                        cb.onFrame(full)
                        pendingHeader = null
                        parts.clear()
                        need = 0
                    }
                })
            }
            // "files" channel is received but not used yet in the phone client.
        }
    }

    private fun readText(b: ByteBuffer): String {
        val arr = ByteArray(b.remaining())
        b.get(arr)
        return String(arr, StandardCharsets.UTF_8)
    }

    /** SdpObserver with no-op defaults so callers override only what they need. */
    private open class SimpleSdp : SdpObserver {
        override fun onCreateSuccess(desc: SessionDescription) {}
        override fun onSetSuccess() {}
        override fun onCreateFailure(error: String?) {}
        override fun onSetFailure(error: String?) {}
    }
}
