package com.wthomson.ctrlfreak

import com.google.gson.annotations.SerializedName

/**
 * Wire models that mirror internal/protocol/protocol.go on the Go side. Only the
 * fields the controller uses are represented. Parsed and produced with Gson.
 */

data class Signal(
    @SerializedName("type") var type: String = "",
    @SerializedName("session_id") var sessionId: String? = null,
    @SerializedName("token") var token: String? = null,
    @SerializedName("role") var role: String? = null,
    @SerializedName("device_name") var deviceName: String? = null,
    @SerializedName("platform") var platform: String? = null,
    @SerializedName("client_id") var clientId: String? = null,
    @SerializedName("host_id") var hostId: String? = null,
    @SerializedName("sdp") var sdp: String? = null,
    @SerializedName("candidate") var candidate: IceCandidateMsg? = null,
    @SerializedName("devices") var devices: List<DeviceInfo>? = null,
    @SerializedName("device") var device: DeviceInfo? = null,
    @SerializedName("online") var online: Boolean = false,
    @SerializedName("ice_servers") var iceServers: List<IceServer>? = null,
    @SerializedName("command") var command: String? = null,
    @SerializedName("message") var message: String? = null,
)

data class DeviceInfo(
    @SerializedName("id") val id: String = "",
    @SerializedName("name") val name: String = "",
    @SerializedName("owner") val owner: String = "",
    @SerializedName("platform") val platform: String = "",
    @SerializedName("online") val online: Boolean = false,
)

data class IceServer(
    @SerializedName("urls") val urls: List<String> = emptyList(),
    @SerializedName("username") val username: String? = null,
    @SerializedName("credential") val credential: String? = null,
)

data class IceCandidateMsg(
    @SerializedName("candidate") val candidate: String = "",
    @SerializedName("sdpMid") val sdpMid: String? = null,
    @SerializedName("sdpMLineIndex") val sdpMLineIndex: Int = 0,
)

/** DataChannel message (ctrl / screen channels), mirrors protocol.DataMsg. */
data class DataMsg(
    @SerializedName("type") var type: String = "",
    @SerializedName("w") var w: Int = 0,
    @SerializedName("h") var h: Int = 0,
    @SerializedName("key") var key: Boolean = false,
    @SerializedName("x") var x: Int = 0,
    @SerializedName("y") var y: Int = 0,
    @SerializedName("mx") var mx: Int = 0,
    @SerializedName("my") var my: Int = 0,
    @SerializedName("btn") var btn: String? = null,
    @SerializedName("down") var down: Boolean = false,
    @SerializedName("dx") var dx: Int = 0,
    @SerializedName("dy") var dy: Int = 0,
    @SerializedName("code") var code: String? = null,
    @SerializedName("fps") var fps: Int = 0,
    @SerializedName("quality") var quality: Int = 0,
    @SerializedName("scale") var scale: Int = 0,
    @SerializedName("text") var text: String? = null,
)

// Signal type constants.
object Sig {
    const val HELLO = "hello"
    const val WELCOME = "welcome"
    const val ERROR = "error"
    const val DEVICE_LIST = "device_list"
    const val DEVICE_EVENT = "device_event"
    const val CONNECT = "connect"
    const val OFFER = "offer"
    const val ANSWER = "answer"
    const val CANDIDATE = "candidate"
    const val BYE = "bye"
    const val COMMAND = "command"
}

// DataMsg type constants (subset used by the controller).
object DM {
    const val SCREEN_INFO = "screen_info"
    const val FRAME = "frame"
    const val MOUSE_MOVE = "mouse_move"
    const val MOUSE_BUTTON = "mouse_button"
    const val MOUSE_SCROLL = "mouse_scroll"
    const val KEY = "key"
    const val TYPE_TEXT = "type_text"
    const val SET_QUALITY = "set_quality"
}

// Command names.
object Cmd {
    const val REBOOT = "reboot"
    const val RESTART_AGENT = "restart_agent"
}
