package com.wthomson.ctrlfreak

import android.content.Context
import org.webrtc.PeerConnectionFactory

/** Holds the process-wide PeerConnectionFactory, initialized once. */
object Rtc {
    @Volatile
    private var factory: PeerConnectionFactory? = null

    fun factory(ctx: Context): PeerConnectionFactory {
        factory?.let { return it }
        synchronized(this) {
            factory?.let { return it }
            PeerConnectionFactory.initialize(
                PeerConnectionFactory.InitializationOptions
                    .builder(ctx.applicationContext)
                    .createInitializationOptions()
            )
            val f = PeerConnectionFactory.builder()
                .setOptions(PeerConnectionFactory.Options())
                .createPeerConnectionFactory()
            factory = f
            return f
        }
    }
}
