package com.wthomson.ctrlfreak

import android.os.Handler
import android.os.Looper
import com.google.gson.Gson
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.CopyOnWriteArraySet

/**
 * App-wide signaling connection to the relay. A single WebSocket is shared by the
 * device list and any active session. Incoming messages are parsed and delivered
 * to registered listeners on the main thread.
 */
object SignalClient {
    interface Listener {
        fun onSignal(s: Signal)
        fun onConnected() {}
        fun onDisconnected(reason: String) {}
    }

    private val gson = Gson()
    private val main = Handler(Looper.getMainLooper())
    private val listeners = CopyOnWriteArraySet<Listener>()

    private var ws: WebSocket? = null
    private var relay: String = ""
    private var token: String = ""
    private var wantOpen = false

    // Cached state so a screen that gets rebuilt (e.g. the Fold cover<->main
    // switch, or a rotation) can show the live device list immediately instead
    // of resetting to "Connecting..." with nothing. Touched only on the main
    // thread.
    private var connected = false
    private val deviceCache = LinkedHashMap<String, DeviceInfo>()

    var clientId: String = ""
        private set
    var iceServers: List<IceServer> = emptyList()
        private set

    fun addListener(l: Listener) {
        listeners.add(l)
        // Replay current state to the newcomer.
        main.post {
            if (connected) l.onConnected()
            if (deviceCache.isNotEmpty()) {
                l.onSignal(Signal(type = Sig.DEVICE_LIST, devices = deviceCache.values.toList()))
            }
        }
    }

    fun removeListener(l: Listener) = listeners.remove(l)

    val isOpen: Boolean get() = ws != null

    fun connect(relayWsUrl: String, tok: String) {
        relay = relayWsUrl
        token = tok
        wantOpen = true
        openSocket()
    }

    fun disconnect() {
        wantOpen = false
        ws?.close(1000, "bye")
        ws = null
    }

    fun send(sig: Signal) {
        ws?.send(gson.toJson(sig))
    }

    private fun openSocket() {
        val req = Request.Builder().url(relay).build()
        ws = Net.client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                // First message must be the hello with our token.
                webSocket.send(
                    gson.toJson(
                        Signal(type = Sig.HELLO, token = token, role = "controller", platform = "android")
                    )
                )
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                val sig = try {
                    gson.fromJson(text, Signal::class.java)
                } catch (e: Exception) {
                    return
                }
                if (sig.type == Sig.WELCOME) {
                    clientId = sig.clientId ?: ""
                    sig.iceServers?.let { iceServers = it }
                }
                main.post {
                    when (sig.type) {
                        Sig.WELCOME -> connected = true
                        Sig.DEVICE_LIST -> {
                            deviceCache.clear()
                            sig.devices?.forEach { deviceCache[it.id] = it }
                        }
                        Sig.DEVICE_EVENT -> {
                            val d = sig.device
                            if (d != null) {
                                if (sig.online) deviceCache[d.id] = d else deviceCache.remove(d.id)
                            }
                        }
                    }
                    if (sig.type == Sig.WELCOME) listeners.forEach { it.onConnected() }
                    listeners.forEach { it.onSignal(sig) }
                }
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                ws = null
                main.post { connected = false; listeners.forEach { it.onDisconnected(t.message ?: "connection failed") } }
                scheduleReconnect()
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                ws = null
                main.post { connected = false; listeners.forEach { it.onDisconnected(reason) } }
                if (wantOpen) scheduleReconnect()
            }
        })
    }

    private fun scheduleReconnect() {
        if (!wantOpen) return
        main.postDelayed({ if (wantOpen && ws == null) openSocket() }, 3000)
    }
}
