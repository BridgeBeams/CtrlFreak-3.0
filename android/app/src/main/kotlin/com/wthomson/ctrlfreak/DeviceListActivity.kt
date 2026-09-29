package com.wthomson.ctrlfreak

import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity

/** Lists the machines the signed-in user can reach, with reboot / restart / connect. */
class DeviceListActivity : AppCompatActivity(), SignalClient.Listener {

    private val devices = LinkedHashMap<String, DeviceInfo>()
    private lateinit var listContainer: LinearLayout
    private lateinit var status: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.parseColor("#0d1117"))
        }

        // Top bar
        val bar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setBackgroundColor(Color.parseColor("#161b22"))
            setPadding(dp(14), dp(12), dp(14), dp(12))
            gravity = Gravity.CENTER_VERTICAL
        }
        val title = TextView(this).apply {
            text = "CtrlFreak"
            setTextColor(Color.WHITE); textSize = 18f; setTypeface(null, Typeface.BOLD)
        }
        val spacer = View(this).apply {}
        val signOut = Button(this).apply { text = "Sign out" }
        signOut.setOnClickListener {
            SignalClient.disconnect()
            Prefs.clearToken(this)
            startActivity(Intent(this, MainActivity::class.java))
            finish()
        }
        bar.addView(title)
        bar.addView(spacer, LinearLayout.LayoutParams(0, 1, 1f))
        bar.addView(signOut)

        status = TextView(this).apply {
            text = "Connecting to hub..."
            setTextColor(Color.parseColor("#8b949e")); textSize = 13f
            setPadding(dp(14), dp(10), dp(14), dp(4))
        }

        listContainer = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(10), dp(6), dp(10), dp(10))
        }
        val scroll = ScrollView(this).apply { addView(listContainer) }

        root.addView(bar, mw())
        root.addView(status, mw())
        root.addView(scroll, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f))
        setContentView(root)

        SignalClient.addListener(this)
        if (!SignalClient.isOpen) {
            SignalClient.connect(Prefs.relay(this), Prefs.token(this))
        }
    }

    override fun onResume() {
        super.onResume()
        render()
    }

    override fun onDestroy() {
        SignalClient.removeListener(this)
        super.onDestroy()
    }

    // ---- signaling ----

    override fun onConnected() {
        status.text = "Connected"
    }

    override fun onDisconnected(reason: String) {
        status.text = "Reconnecting..."
    }

    override fun onSignal(s: Signal) {
        when (s.type) {
            Sig.DEVICE_LIST -> {
                devices.clear()
                s.devices?.forEach { devices[it.id] = it }
                render()
            }
            Sig.DEVICE_EVENT -> {
                val d = s.device ?: return
                if (s.online) devices[d.id] = d else devices.remove(d.id)
                render()
            }
            Sig.ERROR -> {
                if (s.sessionId.isNullOrEmpty()) {
                    Toast.makeText(this, s.message ?: "error", Toast.LENGTH_LONG).show()
                }
            }
        }
    }

    // ---- ui ----

    private fun render() {
        listContainer.removeAllViews()
        val me = Prefs.user(this)
        if (devices.isEmpty()) {
            listContainer.addView(TextView(this).apply {
                text = "No machines online yet. Install the CtrlFreak host on a PC and it will appear here."
                setTextColor(Color.parseColor("#8b949e")); textSize = 14f
                setPadding(dp(8), dp(16), dp(8), dp(8))
            })
            return
        }
        for (d in devices.values.sortedBy { it.name.lowercase() }) {
            listContainer.addView(deviceRow(d, me))
        }
    }

    private fun deviceRow(d: DeviceInfo, me: String): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setBackgroundColor(Color.parseColor("#1c2230"))
            setPadding(dp(12), dp(12), dp(8), dp(12))
        }
        (row.layoutParams as? LinearLayout.LayoutParams)

        val dot = TextView(this).apply {
            text = "●"
            setTextColor(if (d.online) Color.parseColor("#2ea043") else Color.parseColor("#8b949e"))
            textSize = 14f
            setPadding(0, 0, dp(10), 0)
        }
        val info = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        info.addView(TextView(this).apply {
            text = d.name
            setTextColor(Color.WHITE); textSize = 16f; setTypeface(null, Typeface.BOLD)
        })
        val sub = buildString {
            append(d.platform)
            if (d.owner.isNotEmpty() && d.owner != me) append("  ·  ${d.owner}")
        }
        info.addView(TextView(this).apply {
            text = sub
            setTextColor(Color.parseColor("#8b949e")); textSize = 12f
        })

        val restart = smallButton("↻").apply {
            setOnClickListener { confirmCommand(d, Cmd.RESTART_AGENT) }
        }
        val reboot = smallButton("⏻").apply {
            setOnClickListener { confirmCommand(d, Cmd.REBOOT) }
        }

        row.setOnClickListener { openSession(d) }
        info.setOnClickListener { openSession(d) }

        row.addView(dot)
        row.addView(info, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        row.addView(restart)
        row.addView(reboot)

        val wrap = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, 0, 0, dp(8))
        }
        wrap.addView(row, mw())
        return wrap
    }

    private fun openSession(d: DeviceInfo) {
        if (!d.online) {
            Toast.makeText(this, "${d.name} is offline.", Toast.LENGTH_SHORT).show()
            return
        }
        startActivity(Intent(this, SessionActivity::class.java).apply {
            putExtra(SessionActivity.EXTRA_HOST_ID, d.id)
            putExtra(SessionActivity.EXTRA_HOST_NAME, d.name)
        })
    }

    private fun confirmCommand(d: DeviceInfo, cmd: String) {
        if (!d.online) {
            Toast.makeText(this, "${d.name} is offline.", Toast.LENGTH_SHORT).show()
            return
        }
        val msg = if (cmd == Cmd.REBOOT)
            "Reboot \"${d.name}\"? It restarts in 10 seconds and open apps are force-closed."
        else
            "Restart the CtrlFreak agent on \"${d.name}\"? It bounces in a few seconds without rebooting."
        AlertDialog.Builder(this)
            .setTitle(if (cmd == Cmd.REBOOT) "Reboot" else "Restart agent")
            .setMessage(msg)
            .setPositiveButton("Yes") { _, _ ->
                SignalClient.send(Signal(type = Sig.COMMAND, hostId = d.id, command = cmd))
                Toast.makeText(this, "Sent to ${d.name}.", Toast.LENGTH_SHORT).show()
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun smallButton(label: String) = Button(this).apply {
        text = label; textSize = 16f
        minWidth = dp(44); minimumWidth = dp(44)
        setPadding(dp(6), dp(6), dp(6), dp(6))
    }

    private fun mw() = LinearLayout.LayoutParams(
        LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
    )

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
