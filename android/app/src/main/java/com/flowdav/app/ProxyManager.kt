package com.flowdav.app

import android.net.Uri
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import com.flowdav.app.flowdavmobile.Flowdavmobile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class ProxyManager(
    private val configReader: ConfigReader
) : ViewModel() {

    private val _state = MutableStateFlow(ProxyState())
    val state: StateFlow<ProxyState> = _state.asStateFlow()

    private var startJob: Job? = null
    private var pollJob: Job? = null

    data class ManualFields(
        val url: String,
        val login: String,
        val token: String,
        val encKey: String,
        val hmacKey: String
    )

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
            _state.value = ProxyState(status = ProxyState.Status.ERROR, error = "Manual mode requires all fields")
            return
        }
        if (!isManualMode && fileUri == null) {
            _state.value = ProxyState(status = ProxyState.Status.ERROR, error = "File mode requires a config file")
            return
        }

        startJob?.cancel()
        startJob = viewModelScope.launch {
            if (!isActive) return@launch
            _state.value = _state.value.copy(status = ProxyState.Status.CONNECTING)

            val result = withContext(Dispatchers.IO) {
                try {
                    if (socks5User != null && socks5Pass != null) {
                        Flowdavmobile.setSocks5Auth(socks5User, socks5Pass)
                    }
                    if (isManualMode && manualFields != null) {
                        Flowdavmobile.startProxyManual(
                            manualFields.url, manualFields.login, manualFields.token,
                            manualFields.encKey, manualFields.hmacKey, listenAddr
                        )
                    } else if (fileUri != null) {
                        val data = configReader.read(fileUri).getOrThrow()
                        Flowdavmobile.startProxyFromData(data, password, listenAddr)
                    }
                    null as String?
                } catch (e: Exception) {
                    e.message ?: "unknown error"
                }
            }

            if (!isActive) return@launch

            if (result != null) {
                _state.value = ProxyState(
                    status = ProxyState.Status.ERROR,
                    error = result
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
                val deferredErr = Flowdavmobile.stopAndError()
                _state.value = ProxyState(
                    status = ProxyState.Status.ERROR,
                    error = deferredErr.ifEmpty { "unknown error" }
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
                    val deferredErr = Flowdavmobile.stopAndError()
                    val errorMsg = s.error?.ifEmpty { null } ?: deferredErr.ifEmpty { null }
                    _state.value = ProxyState(
                        status = if (errorMsg != null) ProxyState.Status.ERROR else ProxyState.Status.STOPPED,
                        error = errorMsg
                    )
                    return@launch
                }
                _state.value = _state.value.copy(
                    sessions = s.activeSessions,
                    logs = s.logs ?: "",
                    error = s.error?.ifEmpty { null }
                )
            }
        }
    }
}

class ProxyManagerFactory(
    private val configReader: ConfigReader
) : ViewModelProvider.Factory {
    @Suppress("UNCHECKED_CAST")
    override fun <T : ViewModel> create(modelClass: Class<T>): T =
        ProxyManager(configReader) as T
}
