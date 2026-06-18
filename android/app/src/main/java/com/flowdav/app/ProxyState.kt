package com.flowdav.app

data class ProxyState(
    val running: Boolean = false,
    val sessions: Int = 0,
    val listenAddr: String = "",
    val logs: String = "",
    val error: String? = null,
    val status: Status = Status.STOPPED,
    val startedAt: Long = 0L
) {
    enum class Status { STOPPED, CONNECTING, RUNNING, ERROR }
}
