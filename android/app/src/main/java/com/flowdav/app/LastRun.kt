package com.flowdav.app

import android.content.Context
import androidx.core.content.edit
import androidx.core.net.toUri
import com.flowdav.app.flowdavmobile.Flowdavmobile
import com.flowdav.app.flowdavmobile.StartResult

/**
 * Parameters of the last successful start. Persisted (encrypted) so the quick
 * settings tile, boot autostart, the service watchdog and the UI auto-reconnect
 * can restart the proxy without opening the full UI.
 */
data class LastRun(
    val manual: Boolean,
    val listenAddr: String,
    val socks5User: String?,
    val socks5Pass: String?,
    val fileUri: String?,
    val manualUrl: String?,
    val manualLogin: String?,
    val manualToken: String?,
    val manualEncKey: String?,
    val manualHmacKey: String?
) {
    /** Listen address with the default applied; every start path must use this. */
    fun startListenAddr(): String = listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }
}

object LastRunStore {
    private const val KEY_PRESENT = "last_run_present"
    private const val KEY_MANUAL = "last_run_manual"
    private const val KEY_LISTEN = "last_run_listen"
    private const val KEY_SOCKS_USER = "last_run_socks_user"
    private const val KEY_SOCKS_PASS = "last_run_socks_pass"
    private const val KEY_FILE_URI = "last_run_file_uri"
    private const val KEY_URL = "last_run_url"
    private const val KEY_LOGIN = "last_run_login"
    private const val KEY_TOKEN = "last_run_token"
    private const val KEY_ENC = "last_run_enc"
    private const val KEY_HMAC = "last_run_hmac"

    fun save(context: Context, run: LastRun) {
        PrefsStore.get(context).edit {
            putBoolean(KEY_PRESENT, true)
            putBoolean(KEY_MANUAL, run.manual)
            putString(KEY_LISTEN, run.listenAddr)
            putString(KEY_SOCKS_USER, run.socks5User)
            putString(KEY_SOCKS_PASS, run.socks5Pass)
            putString(KEY_FILE_URI, run.fileUri)
            putString(KEY_URL, run.manualUrl)
            putString(KEY_LOGIN, run.manualLogin)
            putString(KEY_TOKEN, run.manualToken)
            putString(KEY_ENC, run.manualEncKey)
            putString(KEY_HMAC, run.manualHmacKey)
        }
    }

    fun load(context: Context): LastRun? {
        val prefs = PrefsStore.get(context)
        if (!prefs.getBoolean(KEY_PRESENT, false)) return null
        return LastRun(
            manual = prefs.getBoolean(KEY_MANUAL, false),
            listenAddr = prefs.getString(KEY_LISTEN, "") ?: "",
            socks5User = prefs.getString(KEY_SOCKS_USER, null),
            socks5Pass = prefs.getString(KEY_SOCKS_PASS, null),
            fileUri = prefs.getString(KEY_FILE_URI, null),
            manualUrl = prefs.getString(KEY_URL, null),
            manualLogin = prefs.getString(KEY_LOGIN, null),
            manualToken = prefs.getString(KEY_TOKEN, null),
            manualEncKey = prefs.getString(KEY_ENC, null),
            manualHmacKey = prefs.getString(KEY_HMAC, null)
        )
    }

    /** Clears the saved master password and its config scope. */
    fun forgetPassword(context: Context) {
        PrefsStore.get(context).edit {
            remove(PrefsStore.PREF_PASSWORD)
            remove(PrefsStore.PREF_PASSWORD_CONFIG)
        }
    }

    /**
     * Saved master password; only when bound to [configUri] (or unbound).
     * Single source of truth for "a password belongs to the config it was
     * confirmed for", shared by the UI, reconnect and background starts.
     */
    fun resolvePassword(context: Context, configUri: String?): String {
        val prefs = PrefsStore.get(context)
        val saved = prefs.getString(PrefsStore.PREF_PASSWORD, "") ?: ""
        val savedFor = prefs.getString(PrefsStore.PREF_PASSWORD_CONFIG, "") ?: ""
        return if (saved.isNotEmpty() && (savedFor.isEmpty() || savedFor == configUri)) saved else ""
    }
}

/**
 * Starts the proxy from [LastRunStore] state outside the ViewModel (tile,
 * boot receiver, service watchdog). Shares LastRunStore.resolvePassword and
 * LastRun.startListenAddr with the ViewModel path so they cannot drift.
 */
object SavedStart {

    /** Applies pending SOCKS5 credentials (bridge consumes them on start). */
    fun applySocks5Auth(run: LastRun) {
        val user = run.socks5User
        val pass = run.socks5Pass
        if (!user.isNullOrEmpty() && pass != null) {
            Flowdavmobile.setSocks5Auth(user, pass)
        }
    }

    /**
     * Starts the proxy engine from saved parameters.
     * @return true when the engine reports running; false otherwise.
     */
    fun start(context: Context): Boolean {
        val last = LastRunStore.load(context) ?: return false
        // Multiple overlap-prone start paths: never kill a live engine here.
        if (Flowdavmobile.getStatus()?.running == true) return true
        applySocks5Auth(last)

        return if (last.manual) {
            val url = last.manualUrl ?: return false
            applied(
                Flowdavmobile.startProxyManual(
                    url,
                    last.manualLogin ?: "",
                    last.manualToken ?: "",
                    last.manualEncKey ?: "",
                    last.manualHmacKey ?: "",
                    last.startListenAddr()
                )
            )
        } else {
            val uri = last.fileUri ?: return false
            val bytes = ContentResolverConfigReader(context)
                .read(uri.toUri())
                .getOrNull() ?: return false
            val password = LastRunStore.resolvePassword(context, uri)
            applied(Flowdavmobile.startProxyFromData(bytes, password, last.startListenAddr()))
        }
    }

    /** A concurrent path may have won the start race; that is still success. */
    private fun applied(result: StartResult): Boolean = result.success ||
        (
            result.code == ProxyState.ErrorKind.ALREADY_RUNNING.code &&
                Flowdavmobile.getStatus()?.running == true
            )
}
