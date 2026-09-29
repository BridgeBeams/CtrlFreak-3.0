package com.wthomson.ctrlfreak

import okhttp3.OkHttpClient
import java.security.SecureRandom
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

/**
 * Shared OkHttp client.
 *
 * By default the relay uses a self-signed certificate (that is the out-of-the-box
 * posture for the whole system), so this client is configured to accept it, the
 * same way the host agent does with its -verify=false default. Once you put a real
 * certificate on the relay, this can be tightened to normal validation.
 */
object Net {
    val client: OkHttpClient by lazy { build() }

    private fun build(): OkHttpClient {
        val trustAll = object : X509TrustManager {
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
            override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
        }
        val ssl = SSLContext.getInstance("TLS")
        ssl.init(null, arrayOf(trustAll), SecureRandom())
        return OkHttpClient.Builder()
            .sslSocketFactory(ssl.socketFactory, trustAll)
            .hostnameVerifier { _, _ -> true }
            .pingInterval(30, java.util.concurrent.TimeUnit.SECONDS)
            .build()
    }
}
