package com.flowdav.app

import com.flowdav.app.flowdavmobile.Flowdavmobile
import java.net.Authenticator
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.PasswordAuthentication
import java.net.Proxy
import java.net.Socket
import java.net.SocketException
import java.net.SocketTimeoutException
import java.net.URL

/**
 * Connection self-test, split into stages so the verdict is not hostage to the
 * remote server's polling cadence:
 *
 *  1. local SOCKS5 listener is accepting (instant, local);
 *  2. WebDAV storage round-trip via the running engine — fast and independent
 *     of poll tuning (max_poll_ms etc.); this is the deterministic verdict;
 *  3. end-to-end request to an IP-echo through the tunnel — proves server +
 *     target and surfaces the exit IP. This stage is informational: on a cold
 *     tunnel (server idle-backoff) the session may expire before the server
 *     polls, so it reports honestly instead of flipping the deterministic
 *     verdict. The very probe also wakes the server for the next poll.
 */
object ConnectionTest {

    // Plain "my IP" echoes, tried in shuffled order through the tunnel. The
    // response body is the public IP the request exits from — the server's
    // egress IP.
    private val TARGETS = listOf(
        "https://api.ipify.org/",
        "https://ifconfig.me/ip",
        "https://checkip.amazonaws.com/",
        "https://icanhazip.com/",
        "https://api.seeip.org/"
    )

    private const val MAX_BODY = 64
    private const val LOCAL_TIMEOUT_MS = 2_000

    // One attempt must outlast a warm round-trip but stay short enough to
    // retry several times within the total probe budget.
    private const val PER_ATTEMPT_MAX_MS = 12_000
    private const val RETRY_PAUSE_MS = 1_500L

    enum class Kind {
        PROXY_NOT_RUNNING,
        STORAGE,
        OK_EXIT,
        OK_SERVER_TIMEOUT,
        OK_SERVER_SILENT,
        OK_SERVER_ERROR
    }

    /** Structured verdict; the UI maps [Kind] to string resources. */
    data class Result(val ok: Boolean, val kind: Kind, val detail: String? = null, val ip: String? = null, val ms: Long = 0L)

    fun probe(host: String, port: Int, socks5User: String?, socks5Pass: String?): Result {
        // Stage 1 — local listener.
        try {
            Socket().use { it.connect(InetSocketAddress(host, port), LOCAL_TIMEOUT_MS) }
        } catch (e: Exception) {
            return Result(false, Kind.PROXY_NOT_RUNNING, e.javaClass.simpleName)
        }

        // Stage 2 — storage round-trip (deterministic, independent of defaults).
        val storage = Flowdavmobile.testStorage()
        if (storage == null || !storage.success) {
            return Result(false, Kind.STORAGE, storage?.code ?: "unknown")
        }

        // Stage 3 — end-to-end (informational). A cold server may only poll
        // after its idle backoff (max_poll_ms, up to 60s by default), so one
        // short attempt usually misses. Keep probing shuffled targets until
        // the total budget (ProbeTimeoutMs) is spent.
        val budgetMs = Flowdavmobile.probeTimeoutMs()
        val perAttemptMs = minOf(budgetMs, PER_ATTEMPT_MAX_MS)
        val started = System.nanoTime()
        val deadline = started + budgetMs * 1_000_000L
        val proxy = Proxy(Proxy.Type.SOCKS, InetSocketAddress(host, port))
        try {
            if (!socks5User.isNullOrEmpty()) {
                // JVM-global state; unset in the finally below.
                Authenticator.setDefault(object : Authenticator() {
                    override fun getPasswordAuthentication(): PasswordAuthentication =
                        PasswordAuthentication(socks5User, (socks5Pass ?: "").toCharArray())
                })
            }
            var lastDetail = ""
            var lastKind = Kind.OK_SERVER_SILENT
            while (System.nanoTime() < deadline) {
                for (target in TARGETS.shuffled()) {
                    try {
                        val hit = attempt(target, proxy, perAttemptMs, started)
                        return Result(true, Kind.OK_EXIT, ip = hit.ip, ms = hit.ms)
                    } catch (e: SocketTimeoutException) {
                        lastKind = Kind.OK_SERVER_TIMEOUT
                        // Report the wait the user actually experienced.
                        lastDetail = "${(System.nanoTime() - started) / 1_000_000_000}"
                    } catch (e: SocketException) {
                        lastKind = Kind.OK_SERVER_SILENT
                        lastDetail = ""
                    } catch (e: Exception) {
                        lastKind = Kind.OK_SERVER_ERROR
                        lastDetail = "${e.javaClass.simpleName}: ${e.message}"
                    }
                    if (System.nanoTime() >= deadline) break
                }
                if (System.nanoTime() >= deadline) break
                Thread.sleep(RETRY_PAUSE_MS)
            }
            // The app-controlled path is verified; the server leg is just cold.
            return Result(true, lastKind, detail = lastDetail.ifEmpty { null })
        } finally {
            Authenticator.setDefault(null)
        }
    }

    private data class Hit(val ip: String, val ms: Long)

    private fun attempt(target: String, proxy: Proxy, timeoutMs: Int, started: Long): Hit {
        val conn = URL(target).openConnection(proxy) as HttpURLConnection
        try {
            conn.connectTimeout = timeoutMs
            conn.readTimeout = timeoutMs
            conn.requestMethod = "GET"
            conn.instanceFollowRedirects = true
            conn.setRequestProperty(
                "User-Agent",
                "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 " +
                    "(KHTML, like Gecko) Chrome/133.0.0.0 Mobile Safari/537.36"
            )
            val code = conn.responseCode
            if (code !in 200..299) throw SocketException("HTTP $code")
            val body = conn.inputStream.use { input ->
                val buf = ByteArray(MAX_BODY)
                val n = input.read(buf)
                if (n > 0) String(buf, 0, n, Charsets.UTF_8).trim() else ""
            }
            if (body.isEmpty()) throw SocketException("empty response")
            return Hit(body, (System.nanoTime() - started) / 1_000_000)
        } finally {
            conn.disconnect()
        }
    }
}
