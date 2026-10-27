package com.flowdav.app

import android.content.Context
import android.net.Uri
import android.util.Log
import androidx.core.content.edit
import androidx.core.net.toUri
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import com.flowdav.app.flowdavmobile.Flowdavmobile
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class ProxyManager(private val appContext: Context, private val configReader: ContentResolverConfigReader) : ViewModel() {

    private val _state = MutableStateFlow(ProxyState())
    val state: StateFlow<ProxyState> = _state.asStateFlow()

    private var startJob: Job? = null
    private var reconnectAttempts = 0

    // In-memory only: a Bundle survives process death and is not encrypted.
    private var manualDraft: ManualFields? = null

    data class ManualFields(val url: String, val login: String, val token: String, val encKey: String, val hmacKey: String)

    fun saveDraft(fields: ManualFields?) {
        manualDraft = fields
    }

    fun loadDraft(): ManualFields? = manualDraft

    fun start(
        listenAddr: String,
        socks5User: String?,
        socks5Pass: String?,
        fileUri: Uri?,
        password: String,
        isManualMode: Boolean,
        manualFields: ManualFields?
    ) {
        if (isManualMode && manualFields == null) {
            _state.value = ProxyState(
                status = ProxyState.Status.ERROR,
                error = ProxyState.ErrorKind.MANUAL_FIELDS
            )
            return
        }
        if (!isManualMode && fileUri == null) {
            _state.value = ProxyState(
                status = ProxyState.Status.ERROR,
                error = ProxyState.ErrorKind.FILE_REQUIRED
            )
            return
        }

        startJob?.cancel()
        startJob = viewModelScope.launch {
            if (!isActive) return@launch
            _state.value = _state.value.copy(
                status = ProxyState.Status.CONNECTING,
                running = false,
                sessions = 0,
                logs = "",
                webdavUrl = "",
                error = ProxyState.ErrorKind.NONE
            )

            val errorKind: ProxyState.ErrorKind = try {
                withContext(Dispatchers.IO) {
                    try {
                        if (socks5User != null && socks5Pass != null) {
                            Flowdavmobile.setSocks5Auth(socks5User, socks5Pass)
                        }
                        val result = if (isManualMode && manualFields != null) {
                            Flowdavmobile.startProxyManual(
                                manualFields.url,
                                manualFields.login,
                                manualFields.token,
                                manualFields.encKey,
                                manualFields.hmacKey,
                                listenAddr
                            )
                        } else {
                            val read = configReader.read(fileUri!!)
                            if (read.isFailure) {
                                Log.w(TAG, "config read failed", read.exceptionOrNull())
                                return@withContext ProxyState.ErrorKind.CONFIG_OPEN
                            }
                            Flowdavmobile.startProxyFromData(read.getOrThrow(), password, listenAddr)
                        }
                        if (result.success) {
                            ProxyState.ErrorKind.NONE
                        } else if (result.code == ProxyState.ErrorKind.ALREADY_RUNNING.code &&
                            Flowdavmobile.getStatus()?.running == true
                        ) {
                            // Engine is already alive from a concurrent start
                            // path (tile, boot, watchdog): treat as success;
                            // the winner's address is the truth.
                            ProxyState.ErrorKind.NONE
                        } else {
                            ProxyState.ErrorKind.fromCode(result.code)
                        }
                    } catch (e: Exception) {
                        Log.w(TAG, "startProxy failed", e)
                        ProxyState.ErrorKind.UNKNOWN
                    }
                }
            } catch (e: CancellationException) {
                // Blocking gomobile start: if it completed after cancellation,
                // kill the orphan off the main thread.
                withContext(NonCancellable + Dispatchers.IO) {
                    Flowdavmobile.stopProxy()
                }
                throw e
            }

            if (!isActive) {
                if (errorKind == ProxyState.ErrorKind.NONE) {
                    withContext(NonCancellable + Dispatchers.IO) { Flowdavmobile.stopProxy() }
                }
                return@launch
            }

            if (errorKind != ProxyState.ErrorKind.NONE) {
                _state.value = ProxyState(
                    status = ProxyState.Status.ERROR,
                    error = errorKind
                )
                return@launch
            }

            // Give SOCKS5 server a moment to start, then check.
            delay(200)

            if (!isActive) return@launch

            val status = withContext(Dispatchers.IO) { Flowdavmobile.getStatus() }
            if (status?.running == true) {
                // Persist the password only after a confirmed start; a failed
                // attempt must never overwrite a previously valid secret.
                val passwordSaved = if (!isManualMode && fileUri != null && password.isNotEmpty()) {
                    try {
                        withContext(Dispatchers.IO) {
                            PrefsStore.get(appContext).edit(commit = true) {
                                putString(PrefsStore.PREF_PASSWORD, password)
                                putString(PrefsStore.PREF_PASSWORD_CONFIG, fileUri.toString())
                            }
                        }
                        true
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        Log.w(TAG, "master password save failed", e)
                        false
                    }
                } else {
                    false
                }
                rememberLastRun(
                    status.listenAddr.ifBlank { listenAddr },
                    socks5User,
                    socks5Pass,
                    fileUri,
                    isManualMode,
                    manualFields
                )
                _state.value = ProxyState(
                    running = true,
                    status = ProxyState.Status.RUNNING,
                    listenAddr = status.listenAddr.ifBlank { listenAddr },
                    webdavUrl = status.webdavURL.orEmpty(),
                    logs = status.logs.orEmpty(),
                    startedAt = System.currentTimeMillis(),
                    passwordSaved = passwordSaved
                )
            } else {
                // Phase 2: deferred error (see bridge.go StopAndError doc)
                val deferredCode = withContext(Dispatchers.IO) { Flowdavmobile.stopAndError() }
                val code = status?.errorCode?.ifEmpty { null } ?: deferredCode.ifEmpty { null }
                _state.value = ProxyState(
                    status = ProxyState.Status.ERROR,
                    error = code?.let { ProxyState.ErrorKind.fromCode(it) }
                        ?: ProxyState.ErrorKind.FAILED_START
                )
            }
        }
    }

    fun stop() {
        startJob?.cancel()
        startJob = null
        // Engine shutdown drains queued uploads (bounded) — keep it off the
        // main thread so the UI never blocks on JNI.
        viewModelScope.launch(Dispatchers.IO) { Flowdavmobile.stopProxy() }
        _state.value = _state.value.copy(
            running = false,
            status = ProxyState.Status.STOPPED,
            sessions = 0,
            error = ProxyState.ErrorKind.NONE
        )
    }

    fun startFromSaved() {
        val last = LastRunStore.load(appContext) ?: return
        start(
            listenAddr = last.startListenAddr(),
            socks5User = last.socks5User,
            socks5Pass = last.socks5Pass,
            fileUri = last.fileUri?.toUri(),
            password = LastRunStore.resolvePassword(appContext, last.fileUri),
            isManualMode = last.manual,
            manualFields = if (last.manual) {
                ManualFields(
                    last.manualUrl ?: "",
                    last.manualLogin ?: "",
                    last.manualToken ?: "",
                    last.manualEncKey ?: "",
                    last.manualHmacKey ?: ""
                )
            } else {
                null
            }
        )
    }

    /**
     * Restores UI state when the activity is (re)created while the engine is
     * still running. Called from onResume; without it the screen shows
     * "Stopped" even though the proxy is alive. startedAt is reset to now —
     * the session start timestamp lives in the native engine only.
     */
    fun reconcile() {
        viewModelScope.launch {
            val s = withContext(Dispatchers.IO) { Flowdavmobile.getStatus() } ?: return@launch
            if (!s.running) return@launch
            val cur = _state.value
            if (cur.status == ProxyState.Status.RUNNING) return@launch
            _state.value = ProxyState(
                running = true,
                status = ProxyState.Status.RUNNING,
                listenAddr = s.listenAddr.ifBlank { cur.listenAddr },
                webdavUrl = s.webdavURL.orEmpty(),
                logs = s.logs.orEmpty(),
                sessions = s.activeSessions,
                startedAt = System.currentTimeMillis()
            )
        }
    }

    /**
     * Polls bridge status while the UI is visible. Driven by
     * repeatOnLifecycle(STARTED) from the activity, so it stops in the
     * background — the foreground service keeps the proxy alive on its own,
     * and background polling would only waste battery.
     *
     * RUNTIME deaths (SOCKS5 goroutine died) are auto-retried with backoff up
     * to [MAX_AUTO_RECONNECTS]; the service watchdog covers the background.
     */
    suspend fun pollLoop() {
        var reportedDead = false
        var stableTicks = 0
        while (currentCoroutineContext().isActive) {
            delay(1000)
            val s = withContext(Dispatchers.IO) { Flowdavmobile.getStatus() } ?: continue
            if (!s.running) {
                stableTicks = 0
                if (reportedDead) continue
                reportedDead = true
                val deferredCode = withContext(Dispatchers.IO) { Flowdavmobile.stopAndError() }
                val code = s.errorCode?.ifEmpty { null } ?: deferredCode.ifEmpty { null }
                val kind = code?.let { ProxyState.ErrorKind.fromCode(it) } ?: ProxyState.ErrorKind.NONE
                // Don't clobber a specific error already on screen: a failed
                // auto-reconnect surfaces its own kind, and the next poll tick
                // would otherwise downgrade it to STOPPED/NONE before the user
                // can read it.
                if (_state.value.status != ProxyState.Status.ERROR) {
                    val cur = _state.value
                    _state.value = cur.copy(
                        running = false,
                        status = if (kind == ProxyState.ErrorKind.NONE) {
                            ProxyState.Status.STOPPED
                        } else {
                            ProxyState.Status.ERROR
                        },
                        sessions = 0,
                        error = kind,
                        webdavUrl = s.webdavURL.orEmpty().ifBlank { cur.webdavUrl },
                        logs = s.logs.orEmpty().ifBlank { cur.logs }
                    )
                }

                val autoReconnectable = kind == ProxyState.ErrorKind.RUNTIME &&
                    reconnectAttempts < MAX_AUTO_RECONNECTS &&
                    LastRunStore.load(appContext) != null
                if (autoReconnectable) {
                    reconnectAttempts++
                    delay(RECONNECT_DELAYS[reconnectAttempts - 1])
                    _state.value = _state.value.copy(
                        running = false,
                        status = ProxyState.Status.CONNECTING,
                        sessions = 0,
                        error = ProxyState.ErrorKind.NONE
                    )
                    reportedDead = false
                    startFromSaved()
                }
                continue
            }
            reportedDead = false
            stableTicks++
            if (stableTicks >= STABLE_TICKS_TO_RESET) reconnectAttempts = 0
            val code = s.errorCode?.ifEmpty { null }
            val cur = _state.value
            _state.value = if (cur.status != ProxyState.Status.RUNNING) {
                // Engine alive from a non-UI start: promote before UI
                // STOPPED/ERROR cleanup kills its foreground service.
                ProxyState(
                    running = true,
                    status = ProxyState.Status.RUNNING,
                    listenAddr = s.listenAddr.ifBlank { cur.listenAddr },
                    webdavUrl = s.webdavURL.orEmpty(),
                    startedAt = if (cur.startedAt > 0L) cur.startedAt else System.currentTimeMillis(),
                    sessions = s.activeSessions,
                    logs = s.logs ?: "",
                    error = code?.let { ProxyState.ErrorKind.fromCode(it) } ?: ProxyState.ErrorKind.NONE
                )
            } else {
                cur.copy(
                    sessions = s.activeSessions,
                    logs = s.logs ?: "",
                    webdavUrl = s.webdavURL.orEmpty().ifBlank { cur.webdavUrl },
                    error = code?.let { ProxyState.ErrorKind.fromCode(it) } ?: ProxyState.ErrorKind.NONE
                )
            }
        }
    }

    private fun rememberLastRun(
        listenAddr: String,
        socks5User: String?,
        socks5Pass: String?,
        fileUri: Uri?,
        isManualMode: Boolean,
        manualFields: ManualFields?
    ) {
        LastRunStore.save(
            appContext,
            LastRun(
                manual = isManualMode,
                listenAddr = listenAddr,
                socks5User = socks5User,
                socks5Pass = socks5Pass,
                fileUri = fileUri?.toString(),
                manualUrl = manualFields?.url,
                manualLogin = manualFields?.login,
                manualToken = manualFields?.token,
                manualEncKey = manualFields?.encKey,
                manualHmacKey = manualFields?.hmacKey
            )
        )
    }

    companion object {
        private const val TAG = "ProxyManager"
        private const val MAX_AUTO_RECONNECTS = 3
        private const val STABLE_TICKS_TO_RESET = 60
        private val RECONNECT_DELAYS = longArrayOf(2_000L, 5_000L, 15_000L)
    }
}

class ProxyManagerFactory(private val appContext: Context, private val configReader: ContentResolverConfigReader) :
    ViewModelProvider.Factory {
    @Suppress("UNCHECKED_CAST")
    override fun <T : ViewModel> create(modelClass: Class<T>): T = ProxyManager(appContext, configReader) as T
}
