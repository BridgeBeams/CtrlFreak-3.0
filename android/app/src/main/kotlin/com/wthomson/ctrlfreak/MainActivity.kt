package com.wthomson.ctrlfreak

import android.content.Context
import android.graphics.Color
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** Login screen: relay address + username + password. */
class MainActivity : AppCompatActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // Already signed in? Skip straight to the device list.
        if (Prefs.token(this).isNotEmpty() && Prefs.relay(this).isNotEmpty()) {
            startActivity(android.content.Intent(this, DeviceListActivity::class.java))
            finish()
            return
        }

        val inner = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(28), dp(48), dp(28), dp(28))
        }
        val root = android.widget.ScrollView(this).apply {
            setBackgroundColor(Color.parseColor("#0d1117"))
            addView(inner, android.widget.FrameLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.MATCH_PARENT))
        }

        val title = TextView(this).apply {
            text = "CtrlFreak"
            setTextColor(Color.WHITE)
            textSize = 32f
            gravity = Gravity.CENTER
        }
        val relay = field("Hub address").apply {
            setText(Prefs.relay(this@MainActivity).ifEmpty { "wss://hub.ctrlfreak.us/ws" })
            inputType = InputType.TYPE_TEXT_VARIATION_URI
        }
        val user = field("Username")
        val pass = field("Password").apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        }
        val error = TextView(this).apply {
            setTextColor(Color.parseColor("#f85149"))
            textSize = 13f
        }
        val button = Button(this).apply { text = "Sign in" }

        button.setOnClickListener {
            error.text = ""
            val ws = Api.normalizeWs(relay.text.toString())
            val u = user.text.toString().trim()
            val p = pass.text.toString()
            if (ws.isEmpty() || u.isEmpty() || p.isEmpty()) {
                error.text = "Fill in all fields."; return@setOnClickListener
            }
            button.isEnabled = false
            button.text = "Signing in..."
            lifecycleScope.launch {
                try {
                    val res = withContext(Dispatchers.IO) { Api.login(ws, u, p) }
                    Prefs.save(this@MainActivity, ws, res.token, u, res.isAdmin)
                    startActivity(android.content.Intent(this@MainActivity, DeviceListActivity::class.java))
                    finish()
                } catch (e: Exception) {
                    error.text = e.message ?: "Login failed"
                    button.isEnabled = true
                    button.text = "Sign in"
                }
            }
        }

        listOf(title, spacer(24), relay, user, pass, button, error).forEach {
            inner.addView(it, wide())
        }
        setContentView(root)
    }

    private fun field(hint: String) = EditText(this).apply {
        this.hint = hint
        setHintTextColor(Color.parseColor("#8b949e"))
        setTextColor(Color.WHITE)
        // Rounded bordered box so the fields are clearly visible on any device.
        background = android.graphics.drawable.GradientDrawable().apply {
            setColor(Color.parseColor("#161b22"))
            cornerRadius = dp(9).toFloat()
            setStroke(dp(1), Color.parseColor("#2a3240"))
        }
        minHeight = dp(48)
        setPadding(dp(14), dp(12), dp(14), dp(12))
        inputType = InputType.TYPE_CLASS_TEXT
    }

    private fun spacer(h: Int) = View(this).apply { minimumHeight = dp(h) }

    private fun wide() = LinearLayout.LayoutParams(
        LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
    ).apply { topMargin = dp(8) }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()
}
