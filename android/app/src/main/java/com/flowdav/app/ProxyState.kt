package com.flowdav.app

data class ProxyState(
    val running: Boolean = false,
    val sessions: Long = 0L,
    val listenAddr: String = "",
    val webdavUrl: String = "",
    val logs: String = "",
    val error: ErrorKind = ErrorKind.NONE,
    val status: Status = Status.STOPPED,
    val startedAt: Long = 0L,
    val passwordSaved: Boolean = false
) {
    enum class Status { STOPPED, CONNECTING, RUNNING, ERROR }

    /** Stable failure codes; bridge-sourced values mirror ErrorCode in cmd/android/bridge.go. */
    enum class ErrorKind(val code: String) {
        NONE(""),
        ENCRYPTED_NO_PASSWORD("encrypted_no_password"),
        DECRYPT_FAILED("decrypt_failed"),
        INVALID_CONFIG("invalid_config"),
        INVALID_KEY("invalid_key"),
        ALREADY_RUNNING("already_running"),
        STORAGE_INIT("storage_init"),
        LISTEN_FAILED("listen_failed"),
        RUNTIME("runtime"),
        UNKNOWN("unknown"),
        MANUAL_FIELDS("manual_fields"),
        FILE_REQUIRED("file_required"),
        CONFIG_OPEN("config_open"),
        FAILED_START("failed_start");

        companion object {
            fun fromCode(code: String?): ErrorKind = entries.firstOrNull { it.code == code } ?: UNKNOWN
        }
    }
}
