package com.wthomson.ctrlfreak

import android.content.Context
import android.graphics.BitmapFactory
import android.graphics.Color
import android.os.Bundle
import android.text.Editable
import android.text.InputType
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.inputmethod.InputMethodManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/** A single live remote-control session. */
class SessionActivity : AppCompatActivity(), SignalClient.Listener, WebRtcClient.Callbacks, RemoteView.Listener {

    companion object {
        const val EXTRA_HOST_ID = "host_id"
        const val EXTRA_HOST_NAME = "host_name"
    }

    private val sessionId = UUID.randomUUID().toString()
    private lateinit var client: WebRtcClient
    private lateinit var remote: RemoteView
    private lateinit var status: TextView
    private lateinit var kbInput: EditText

    private val decodeExec = Executors.newSingleThreadExecutor()
    private val decoding = AtomicBoolean(false)

    private var lastX = 0
    private var lastY = 0
    private var prevKbText = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val hostId = intent.getStringExtra(EXTRA_HOST_ID) ?: run { finish(); return }
        val hostName = intent.getStringExtra(EXTRA_HOST_NAME) ?: "Remote"

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.BLACK)
        }

        // Top control bar.
        val bar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setBackgroundColor(Color.parseColor("#161b22"))
            setPadding(dp(8), dp(6), dp(8), dp(6))
        }
        status = TextView(this).apply {
            text = "$hostName  ·  connecting"
            setTextColor(Color.parseColor("#8b949e")); textSize = 12f
        }
        bar.addView(status, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        bar.addView(btn("⌨") { toggleKeyboard() })      // keyboard
        bar.addView(btn("R-clk") { rightClick() })            // right click
        bar.addView(btn("Esc") { tap("Escape") })
        bar.addView(btn("Win") { tap("MetaLeft") })
        bar.addView(btn("×") { finish() })               // close

        remote = RemoteView(this).apply { listener = this@SessionActivity }

        // Hidden field that drives the soft keyboard.
        kbInput = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            setBackgroundColor(Color.TRANSPARENT)
            setTextColor(Color.TRANSPARENT)
            height = 1
            addTextChangedListener(kbWatcher)
        }

        root.addView(bar, mw())
        root.addView(remote, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f))
        root.addView(kbInput, LinearLayout.LayoutParams(1, 1))
        setContentView(root)

        SignalClient.addListener(this)
        client = WebRtcClient(this, SignalClient.iceServers, sessionId, hostId, this)
        client.start()
    }

    override fun onDestroy() {
        SignalClient.removeListener(this)
        try { client.close() } catch (_: Exception) {}
        decodeExec.shutdownNow()
        super.onDestroy()
    }

    // ---- signaling routed to the peer connection ----

    override fun onSignal(s: Signal) {
        if (s.sessionId != sessionId) return
        when (s.type) {
            Sig.OFFER -> client.onRemoteOffer(s)
            Sig.CANDIDATE -> client.onRemoteCandidate(s)
            Sig.BYE -> finishWith("session ended by host")
            Sig.ERROR -> finishWith(s.message ?: "error")
        }
    }

    // ---- WebRtc callbacks (may arrive off the main thread) ----

    override fun onScreenInfo(w: Int, h: Int) = runOnUiThread {
        remote.remoteW = w; remote.remoteH = h
    }

    override fun onFrame(jpeg: ByteArray) {
        // Drop frames if a decode is already in flight, to keep latency low.
        if (!decoding.compareAndSet(false, true)) return
        decodeExec.execute {
            try {
                val bmp = BitmapFactory.decodeByteArray(jpeg, 0, jpeg.size)
                if (bmp != null) runOnUiThread { remote.setFrame(bmp) }
            } finally {
                decoding.set(false)
            }
        }
    }

    override fun onState(state: String) = runOnUiThread {
        val name = intent.getStringExtra(EXTRA_HOST_NAME) ?: "Remote"
        status.text = "$name  ·  $state"
    }

    // ---- touch -> input ----

    override fun onDown(x: Int, y: Int) { lastX = x; lastY = y; client.mouseButton("left", true, x, y) }
    override fun onMove(x: Int, y: Int) { lastX = x; lastY = y; client.mouseMove(x, y) }
    override fun onUp(x: Int, y: Int) { client.mouseButton("left", false, x, y) }
    override fun onScroll(dy: Int) { client.scroll(0, dy) }

    private fun rightClick() {
        client.mouseButton("right", true, lastX, lastY)
        client.mouseButton("right", false, lastX, lastY)
    }

    private fun tap(code: String) {
        client.key(code, true); client.key(code, false)
    }

    // ---- keyboard ----

    private fun toggleKeyboard() {
        val imm = getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager
        kbInput.requestFocus()
        imm.toggleSoftInput(InputMethodManager.SHOW_FORCED, 0)
    }

    private val kbWatcher = object : TextWatcher {
        override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
        override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
        override fun afterTextChanged(e: Editable?) {
            val now = e?.toString() ?: ""
            // Longest common prefix with the previous value.
            var i = 0
            val max = minOf(now.length, prevKbText.length)
            while (i < max && now[i] == prevKbText[i]) i++
            val deletions = prevKbText.length - i
            repeat(deletions) { client.key("Backspace", true); client.key("Backspace", false) }
            val added = now.substring(i)
            if (added.isNotEmpty()) sendTyped(added)
            prevKbText = now
        }
    }

    /** Send typed text, turning newlines into Enter key presses. */
    private fun sendTyped(text: String) {
        val buf = StringBuilder()
        for (ch in text) {
            if (ch == '\n') {
                if (buf.isNotEmpty()) { client.typeText(buf.toString()); buf.clear() }
                client.key("Enter", true); client.key("Enter", false)
            } else {
                buf.append(ch)
            }
        }
        if (buf.isNotEmpty()) client.typeText(buf.toString())
    }

    private fun finishWith(msg: String) = runOnUiThread {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
        finish()
    }

    private fun btn(label: String, onClick: () -> Unit) = Button(this).apply {
        text = label; textSize = 12f
        setPadding(dp(8), dp(4), dp(8), dp(4))
        minWidth = 0; minimumWidth = 0
        setOnClickListener { onClick() }
    }

    private fun mw() = LinearLayout.LayoutParams(
        LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
    )

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
