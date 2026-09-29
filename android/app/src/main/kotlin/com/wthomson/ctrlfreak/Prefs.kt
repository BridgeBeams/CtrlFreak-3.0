package com.wthomson.ctrlfreak

import android.content.Context

/** Small wrapper over SharedPreferences for the few things we persist. */
object Prefs {
    private const val FILE = "ctrlfreak"
    private const val K_RELAY = "relay"
    private const val K_TOKEN = "token"
    private const val K_USER = "user"
    private const val K_ADMIN = "admin"

    fun save(ctx: Context, relay: String, token: String, user: String, admin: Boolean) {
        ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit()
            .putString(K_RELAY, relay)
            .putString(K_TOKEN, token)
            .putString(K_USER, user)
            .putBoolean(K_ADMIN, admin)
            .apply()
    }

    fun relay(ctx: Context): String = get(ctx, K_RELAY)
    fun token(ctx: Context): String = get(ctx, K_TOKEN)
    fun user(ctx: Context): String = get(ctx, K_USER)
    fun isAdmin(ctx: Context): Boolean =
        ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE).getBoolean(K_ADMIN, false)

    fun clearToken(ctx: Context) {
        ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().remove(K_TOKEN).apply()
    }

    private fun get(ctx: Context, k: String): String =
        ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE).getString(k, "") ?: ""
}
