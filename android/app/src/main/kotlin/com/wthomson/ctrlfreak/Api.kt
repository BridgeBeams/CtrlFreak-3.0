package com.wthomson.ctrlfreak

import com.google.gson.Gson
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Talks to the relay's HTTPS API (just login, for now). */
object Api {
    private val gson = Gson()
    private val JSON = "application/json".toMediaType()

    data class LoginResult(val token: String, val isAdmin: Boolean, val mustChange: Boolean)

    /**
     * login exchanges username/password for a token. relayWsUrl is the wss://.../ws
     * signaling URL the user entered; we convert it to the https base for the API.
     * Runs synchronously; call from a background thread / coroutine.
     */
    fun login(relayWsUrl: String, user: String, pass: String): LoginResult {
        val base = httpBase(relayWsUrl)
        val body = gson.toJson(mapOf("username" to user, "password" to pass))
            .toRequestBody(JSON)
        val req = Request.Builder().url("$base/api/login").post(body).build()
        Net.client.newCall(req).execute().use { resp ->
            val text = resp.body?.string().orEmpty()
            if (!resp.isSuccessful) {
                throw RuntimeException(if (text.isNotBlank()) text.trim() else "login failed (${resp.code})")
            }
            val obj = gson.fromJson(text, Map::class.java)
            return LoginResult(
                token = obj["token"] as? String ?: "",
                isAdmin = obj["is_admin"] as? Boolean ?: false,
                mustChange = obj["must_change"] as? Boolean ?: false,
            )
        }
    }

    /** Convert a wss/ws signaling URL to its https/http API base (no path). */
    fun httpBase(wsUrl: String): String {
        var u = wsUrl.trim()
        u = when {
            u.startsWith("wss://") -> "https://" + u.removePrefix("wss://")
            u.startsWith("ws://") -> "http://" + u.removePrefix("ws://")
            u.startsWith("https://") || u.startsWith("http://") -> u
            else -> "https://$u"
        }
        // strip trailing /ws and any trailing slash
        u = u.removeSuffix("/")
        if (u.endsWith("/ws")) u = u.removeSuffix("/ws")
        return u
    }

    /** Normalize whatever the user typed into a proper wss://host:port/ws URL. */
    fun normalizeWs(input: String): String {
        var u = input.trim()
        if (u.isEmpty()) return u
        u = when {
            u.startsWith("wss://") || u.startsWith("ws://") -> u
            u.startsWith("https://") -> "wss://" + u.removePrefix("https://")
            u.startsWith("http://") -> "ws://" + u.removePrefix("http://")
            else -> "wss://$u"
        }
        u = u.removeSuffix("/")
        if (!u.endsWith("/ws")) u += "/ws"
        return u
    }
}
