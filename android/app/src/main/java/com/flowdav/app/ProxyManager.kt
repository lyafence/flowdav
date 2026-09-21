package com.flowdav.app

import android.net.Uri
import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import com.flowdav.app.flowdavmobile.Flowdavmobile
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class ProxyManager(private val configReader: ConfigReader) : ViewModel() {

    private val _state = MutableStateFlow(ProxyState())
    val state: StateFlow<ProxyState> = _state.asStateFlow()

    private var startJob: Job? = null
    private var pollJob: Job? = null

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
            _state.value = _state.value.copy(status = ProxyState.Status.CONNECTING)

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
                        } else {
                            ProxyState.ErrorKind.fromCode(result.code)
                        }
                    } catch (e: Exception) {
                        Log.w(TAG, "startProxy failed", e)
                        ProxyState.ErrorKind.UNKNOWN
                    }
                }
            } catch (e: CancellationException) {
                // Blocking gomobile start: if it completed after cancellation, kill the orphan.
                Flowdavmobile.stopProxy()
                throw e
            }

            if (!isActive) {
                if (errorKind == ProxyState.ErrorKind.NONE) Flowdavmobile.stopProxy()
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

            val status = Flowdavmobile.getStatus()
            if (status?.running == true) {
                _state.value = ProxyState(
                    running = true,
                    status = ProxyState.Status.RUNNING,
                    listenAddr = listenAddr,
                    startedAt = System.currentTimeMillis()
                )
                startPolling()
            } else {
                // Phase 2: deferred error (see bridge.go StopAndError doc)
                val deferredCode = Flowdavmobile.stopAndError()
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
        pollJob?.cancel()
        pollJob = null
        Flowdavmobile.stopProxy()
        _state.value = ProxyState(status = ProxyState.Status.STOPPED)
    }

    private fun startPolling() {
        pollJob = viewModelScope.launch {
            while (isActive) {
                delay(1000)
                val s = Flowdavmobile.getStatus() ?: continue
                if (!s.running) {
                    val deferredCode = Flowdavmobile.stopAndError()
                    val code = s.errorCode?.ifEmpty { null } ?: deferredCode.ifEmpty { null }
                    _state.value = ProxyState(
                        status = if (code != null) ProxyState.Status.ERROR else ProxyState.Status.STOPPED,
                        error = code?.let { ProxyState.ErrorKind.fromCode(it) }
                            ?: ProxyState.ErrorKind.NONE
                    )
                    return@launch
                }
                val code = s.errorCode?.ifEmpty { null }
                _state.value = _state.value.copy(
                    sessions = s.activeSessions,
                    logs = s.logs ?: "",
                    error = code?.let { ProxyState.ErrorKind.fromCode(it) }
                        ?: ProxyState.ErrorKind.NONE
                )
            }
        }
    }

    companion object {
        private const val TAG = "ProxyManager"
    }
}

class ProxyManagerFactory(private val configReader: ConfigReader) : ViewModelProvider.Factory {
    @Suppress("UNCHECKED_CAST")
    override fun <T : ViewModel> create(modelClass: Class<T>): T = ProxyManager(configReader) as T
}
