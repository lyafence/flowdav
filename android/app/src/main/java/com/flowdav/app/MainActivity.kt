package com.flowdav.app

import android.content.ClipDescription
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PersistableBundle
import android.os.PowerManager
import android.provider.OpenableColumns
import android.provider.Settings
import android.text.SpannableString
import android.text.Spanned
import android.text.TextPaint
import android.text.method.LinkMovementMethod
import android.text.style.ClickableSpan
import android.text.style.ForegroundColorSpan
import android.transition.TransitionManager
import android.util.Base64
import android.util.Log
import android.view.View
import android.view.WindowManager
import android.widget.TextView
import android.widget.Toast
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.annotation.RequiresApi
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.IntentCompat
import androidx.core.content.edit
import androidx.core.net.toUri
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.core.widget.doAfterTextChanged
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.flowdav.app.databinding.ActivityMainBinding
import com.flowdav.app.flowdavmobile.Flowdavmobile
import com.google.android.material.color.DynamicColors
import com.google.android.material.color.MaterialColors
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.snackbar.Snackbar
import com.google.android.material.textfield.TextInputEditText
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import java.io.File
import java.net.InetSocketAddress
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

/** Default SOCKS5 listen endpoint; shared with ProxyManager.startFromSaved. */
internal const val DEFAULT_LISTEN_ADDR = "127.0.0.1:1080"

class MainActivity : AppCompatActivity() {

    private val proxyManager: ProxyManager by lazy {
        ViewModelProvider(
            this,
            ProxyManagerFactory(applicationContext, ContentResolverConfigReader(applicationContext))
        )[ProxyManager::class.java]
    }

    private var _binding: ActivityMainBinding? = null
    private val binding get() = _binding!!

    private var fileUri: Uri? = null
    private var isManualMode = false
    private var isEncrypted = true
    private var advancedExpanded = false
    private var proxyServiceStarted = false
    private var autoScrollLogs = false
    private var logExpanded = false
    private var testSpin: android.animation.ObjectAnimator? = null
    private var testJob: Job? = null
    private var lastTestResult: ConnectionTest.Result? = null
    private var lastTestAt = 0L
    private var lastStatus: ProxyState.Status? = null
    private var lastError: ProxyState.ErrorKind? = null
    private var clipboardOffered = false

    private val notifPermLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (!granted) {
            window?.decorView?.rootView?.let { root ->
                Snackbar.make(root, R.string.notif_permission_denied, Snackbar.LENGTH_LONG).show()
            }
        }
    }

    private val cameraPermLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (granted) {
            launchScanner()
        } else {
            snack(R.string.camera_denied)
        }
    }

    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val contents = result.contents ?: return@registerForActivityResult
        offerImport(contents, getString(R.string.import_source_qr))
    }

    private val filePicker = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri != null) {
            fileUri = uri
            try {
                contentResolver.takePersistableUriPermission(
                    uri,
                    Intent.FLAG_GRANT_READ_URI_PERMISSION
                )
            } catch (e: Exception) {
                Log.w(TAG, "takePersistableUriPermission failed for $uri", e)
            }
            PrefsStore.get(this).edit { putString(PrefsStore.PREF_URI, uri.toString()) }
            refreshFileInfo(uri)
            refreshStartEnabled()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        // FLAG_SECURE blocks screenshots and screen recording. Keep it for
        // release builds (privacy for a proxy tool), but allow captures in
        // debug builds so the UI can be reviewed and debugged.
        val debuggable =
            (applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE) != 0
        if (!debuggable) {
            window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        }
        DynamicColors.applyToActivityIfAvailable(this)

        _binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        ViewCompat.setOnApplyWindowInsetsListener(binding.root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            val ime = insets.getInsets(WindowInsetsCompat.Type.ime())
            v.updatePadding(
                left = bars.left,
                top = bars.top,
                right = bars.right,
                bottom = maxOf(bars.bottom, ime.bottom)
            )
            WindowInsetsCompat.CONSUMED
        }

        if (Build.VERSION.SDK_INT >= 33) {
            if (shouldShowRequestPermissionRationale(
                    android.Manifest.permission.POST_NOTIFICATIONS
                )
            ) {
                showNotifRationale()
            } else {
                notifPermLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
            }
        }

        binding.configChip.text = getString(R.string.select_file)
        binding.forgetPasswordBtn.setTextColor(
            getColorFromAttr(com.google.android.material.R.attr.colorOnSurfaceVariant)
        )

        savedInstanceState?.let { state ->
            isManualMode = state.getBoolean("isManualMode", false)
            isEncrypted = state.getBoolean("isEncrypted", true)
            advancedExpanded = state.getBoolean("advancedExpanded", false)
            logExpanded = state.getBoolean("logExpanded", false)
            clipboardOffered = state.getBoolean("clipboardOffered", false)
            if (advancedExpanded) {
                binding.advancedContent.visibility = View.VISIBLE
            }
            binding.advancedArrow.rotation = if (advancedExpanded) 180f else 0f
        }
        if (savedInstanceState == null) {
            isManualMode = PrefsStore.get(this).getBoolean(PrefsStore.PREF_MODE_MANUAL, false)
        }

        if (isManualMode) {
            binding.fileSection.visibility = View.GONE
            binding.manualSection.visibility = View.VISIBLE
            binding.modeToggle.check(R.id.modeManual)
        }
        proxyManager.loadDraft()?.let { draft ->
            binding.webdavUrlInput.setText(draft.url)
            binding.webdavLoginInput.setText(draft.login)
            binding.webdavTokenInput.setText(draft.token)
            binding.encKeyInput.setText(draft.encKey)
            binding.hmacKeyInput.setText(draft.hmacKey)
        }

        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager
        if (clipboard != null) {
            binding.encKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, binding.encKeyInput) }
            binding.hmacKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, binding.hmacKeyInput) }
        }

        binding.listenAddrInput.setText(DEFAULT_LISTEN_ADDR)

        binding.bootSwitch.isChecked =
            PrefsStore.get(this).getBoolean(PrefsStore.PREF_BOOT_AUTOSTART, false)

        setupListeners()

        val savedUri = PrefsStore.get(this).getString(PrefsStore.PREF_URI, null)
        if (savedUri != null) {
            fileUri = savedUri.toUri()
            fileUri?.let { refreshFileInfo(it) }
        }

        // Never render the master password; it is loaded from encrypted prefs
        // internally when the field is left empty.
        binding.passwordInput.setText("")
        updateForgetPasswordVisibility()
        updateConfigUi()
        updateStartHint()

        ViewCompat.setStateDescription(
            binding.advancedHeader,
            getString(
                if (binding.advancedContent.isVisible) {
                    R.string.advanced_state_expanded
                } else {
                    R.string.advanced_state_collapsed
                }
            )
        )
        applyLogExpandedState()

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                launch { proxyManager.state.collectLatest { state -> render(state) } }
                launch { proxyManager.pollLoop() }
                launch {
                    while (true) {
                        delay(1_000)
                        val state = proxyManager.state.value
                        if (_binding != null && state.status == ProxyState.Status.RUNNING) {
                            updateRuntimeStats(state)
                        }
                    }
                }
            }
        }

        if (savedInstanceState == null) {
            handleIntent(intent)
        }
    }

    override fun onResume() {
        super.onResume()
        // The engine may still be running (activity recreated or user
        // returned) — reflect it instead of showing "Stopped".
        proxyManager.reconcile()
        maybeOfferClipboardImport()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleIntent(intent)
    }

    override fun onDestroy() {
        testJob?.cancel()
        testJob = null
        testSpin?.cancel()
        testSpin = null
        _binding = null
        super.onDestroy()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean("isManualMode", isManualMode)
        outState.putBoolean("isEncrypted", isEncrypted)
        outState.putBoolean("advancedExpanded", advancedExpanded)
        outState.putBoolean("logExpanded", logExpanded)
        outState.putBoolean("clipboardOffered", clipboardOffered)
        proxyManager.saveDraft(collectManualFields())
    }

    private fun setupListeners() {
        binding.modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            PrefsStore.get(this).edit { putBoolean(PrefsStore.PREF_MODE_MANUAL, isManualMode) }
            refreshStartEnabled()
        }

        binding.configChip.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        binding.configChip.setOnCloseIconClickListener {
            fileUri?.let { uri ->
                if (uri.scheme == "file" && uri.path?.contains("/imported/") == true) {
                    runCatching { File(uri.path!!).delete() }
                }
            }
            fileUri = null
            PrefsStore.get(this).edit { remove(PrefsStore.PREF_URI) }
            isEncrypted = false
            binding.passwordInput.setText("")
            updatePasswordVisibility()
            updateForgetPasswordVisibility()
            binding.configChip.text = getString(R.string.select_file)
            binding.configChip.contentDescription = getString(R.string.select_file)
            binding.configChip.setCloseIconVisible(false)
            refreshStartEnabled()
        }

        binding.scanQrButton.setOnClickListener {
            if (checkSelfPermission(android.Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                launchScanner()
            } else {
                cameraPermLauncher.launch(android.Manifest.permission.CAMERA)
            }
        }

        binding.forgetPasswordBtn.setOnClickListener {
            LastRunStore.forgetPassword(this)
            binding.passwordInput.setText("")
            updateForgetPasswordVisibility()
            refreshStartEnabled()
            snack(R.string.password_forgotten, Snackbar.LENGTH_SHORT)
        }

        binding.bootSwitch.setOnCheckedChangeListener { _, checked ->
            PrefsStore.get(this).edit { putBoolean(PrefsStore.PREF_BOOT_AUTOSTART, checked) }
        }

        binding.contentScroll.viewTreeObserver.addOnScrollChangedListener {
            val sv = binding.contentScroll
            autoScrollLogs = sv.getChildAt(0).bottom - (sv.height + sv.scrollY) <= 0
        }

        binding.advancedHeader.setOnClickListener {
            val expanded = binding.advancedContent.isVisible
            advancedExpanded = !expanded
            // Expansion moves siblings too — animate the scroll container.
            TransitionManager.beginDelayedTransition(binding.contentScroll)
            binding.advancedContent.visibility = if (expanded) View.GONE else View.VISIBLE
            binding.advancedArrow.animate()
                .rotation(if (advancedExpanded) 180f else 0f)
                .setDuration(150)
                .start()
            ViewCompat.setStateDescription(
                binding.advancedHeader,
                getString(if (expanded) R.string.advanced_state_collapsed else R.string.advanced_state_expanded)
            )
            updateAdvancedSummary()
        }

        binding.logHeader.setOnClickListener {
            logExpanded = !logExpanded
            applyLogExpandedState(animate = true)
        }

        binding.copyLogsBtn.setOnClickListener {
            val text = binding.logText.text?.toString() ?: return@setOnClickListener
            val clip = android.content.ClipData.newPlainText("flowdav logs", text)
            if (Build.VERSION.SDK_INT >= 33) {
                clip.description.extras = PersistableBundle().apply {
                    putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true)
                }
            }
            (getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager)
                ?.setPrimaryClip(clip)
            Snackbar.make(binding.root, R.string.logs_copied, Snackbar.LENGTH_SHORT).show()
        }

        binding.testConnectionButton.setOnClickListener {
            runConnectionTest()
        }

        binding.addrValue.setOnClickListener {
            copyProxyAddress()
        }

        binding.copyAddrButton.setOnClickListener {
            copyProxyAddress()
        }

        // Keep the sticky Start CTA honest: it is disabled until the active
        // mode has enough input to attempt a start.
        listOf(
            binding.webdavUrlInput,
            binding.webdavLoginInput,
            binding.webdavTokenInput,
            binding.encKeyInput,
            binding.hmacKeyInput
        ).forEach { field ->
            field.doAfterTextChanged { refreshStartEnabled() }
        }

        binding.listenAddrInput.doAfterTextChanged {
            binding.listenAddrLayout.error = null
            updateAdvancedSummary()
        }
        updateAdvancedSummary()

        binding.passwordInput.doAfterTextChanged {
            refreshStartEnabled()
        }

        binding.infoButton.setOnClickListener {
            showHowTo()
        }

        binding.actionButton.setOnClickListener {
            val cur = proxyManager.state.value
            if (cur.status == ProxyState.Status.CONNECTING) return@setOnClickListener

            if (cur.running) {
                stopProxy()
            } else {
                startProxy()
            }
        }
    }

    private fun startProxy() {
        clearInputErrors()

        var hasError = false

        val listenAddr = binding.listenAddrInput.text?.toString()?.ifBlank { DEFAULT_LISTEN_ADDR }
            ?: DEFAULT_LISTEN_ADDR
        try {
            val addr = InetSocketAddress.createUnresolved(
                listenAddr.substringBeforeLast(":"),
                listenAddr.substringAfterLast(":").toInt()
            )
            if (addr.port < 1 || addr.port > 65535) throw NumberFormatException()
        } catch (_: Exception) {
            binding.listenAddrLayout.error = getString(R.string.error_invalid_listen)
            hasError = true
        }

        val socks5User = binding.socks5UserInput.text?.toString()?.ifBlank { null }
        val socks5Pass = binding.socks5PassInput.text?.toString()?.ifBlank { null }
        if ((socks5User != null) != (socks5Pass != null)) {
            binding.socks5UserLayout.error = getString(R.string.socks5_partial_error)
            binding.socks5PassLayout.error = getString(R.string.socks5_partial_error)
            hasError = true
        }

        val listenHost = listenAddr.substringBeforeLast(":").removeSurrounding("[", "]")
        val loopback = listenHost == "localhost" || listenHost == "127.0.0.1" || listenHost == "::1"
        if (!loopback && (socks5User == null || socks5Pass == null)) {
            binding.listenAddrLayout.error = getString(R.string.error_non_loopback_no_auth)
            hasError = true
        }

        var manualFields: ProxyManager.ManualFields? = null
        if (isManualMode) {
            val url = binding.webdavUrlInput.text?.toString()?.ifBlank { null }
            val login = binding.webdavLoginInput.text?.toString()?.ifBlank { null }
            val token = binding.webdavTokenInput.text?.toString()?.ifBlank { null }
            val encKey = binding.encKeyInput.text?.toString()?.ifBlank { null }
            val hmacKey = binding.hmacKeyInput.text?.toString()?.ifBlank { null }
            if (url == null) {
                binding.webdavUrlLayout.error = getString(R.string.error_required)
                hasError = true
            }
            if (login == null) {
                binding.webdavLoginLayout.error = getString(R.string.error_required)
                hasError = true
            }
            if (token == null) {
                binding.webdavTokenLayout.error = getString(R.string.error_required)
                hasError = true
            }
            if (encKey == null) {
                binding.encKeyLayout.error = getString(R.string.error_required)
                hasError = true
            }
            if (hmacKey == null) {
                binding.hmacKeyLayout.error = getString(R.string.error_required)
                hasError = true
            }
            if (!hasError) manualFields = ProxyManager.ManualFields(url!!, login!!, token!!, encKey!!, hmacKey!!)
        } else if (fileUri == null) {
            Snackbar.make(binding.root, R.string.select_file_first, Snackbar.LENGTH_LONG).show()
            hasError = true
        }

        if (hasError) return

        val password = if (isEncrypted) {
            val typed = binding.passwordInput.text?.toString() ?: ""
            if (typed.isNotEmpty()) {
                typed
            } else {
                LastRunStore.resolvePassword(this, fileUri?.toString())
            }
        } else {
            ""
        }

        currentFocus?.let { view ->
            val imm = getSystemService(Context.INPUT_METHOD_SERVICE) as? android.view.inputmethod.InputMethodManager
            imm?.hideSoftInputFromWindow(view.windowToken, 0)
            view.clearFocus()
        }

        maybeRequestBatteryExemption()
        proxyManager.start(listenAddr, socks5User, socks5Pass, fileUri, password, isManualMode, manualFields)
    }

    private fun stopProxy() {
        proxyManager.stop()
        stopService(Intent(this, ProxyService::class.java))
        proxyServiceStarted = false
    }

    private fun stopProxyService() {
        // Engine may be alive from a non-UI start; never orphan it.
        lifecycleScope.launch(Dispatchers.IO) {
            if (Flowdavmobile.getStatus()?.running == true) return@launch
            withContext(Dispatchers.Main) {
                stopService(Intent(this@MainActivity, ProxyService::class.java))
                proxyServiceStarted = false
            }
        }
    }

    private fun refreshFileInfo(uri: Uri) {
        lifecycleScope.launch {
            val name = withContext(Dispatchers.IO) { getDisplayName(uri) }
            val encrypted = withContext(Dispatchers.IO) { detectConfigEncrypted(uri) }
            showFileChip(name)
            when (encrypted) {
                null -> snack(R.string.config_read_failed)

                else -> {
                    isEncrypted = encrypted
                    updatePasswordVisibility()
                    updateForgetPasswordVisibility()
                }
            }
            refreshStartEnabled()
        }
    }

    /**
     * True when the file is an encrypted config. Delegates to
     * config.IsEncryptedConfig via the bridge so the UI and the engine agree.
     * Returns null when the file cannot be read.
     */
    private fun detectConfigEncrypted(uri: Uri): Boolean? {
        val read = ContentResolverConfigReader(this).read(uri)
        if (read.isFailure) {
            Log.w(TAG, "config read failed", read.exceptionOrNull())
            return null
        }
        val bytes = read.getOrThrow()
        return if (bytes.isEmpty()) false else Flowdavmobile.isEncryptedConfig(bytes)
    }

    private fun render(state: ProxyState) {
        val statusChanged = state.status != lastStatus
        val errorChanged = state.error != lastError
        if (statusChanged || errorChanged) {
            when (state.status) {
                ProxyState.Status.STOPPED -> {
                    if (lastStatus != null) {
                        logExpanded = false
                    }
                    resetTestUi()
                }

                ProxyState.Status.CONNECTING -> Unit

                ProxyState.Status.RUNNING -> {
                    if (statusChanged) resetTestUi()
                }

                ProxyState.Status.ERROR -> {
                    logExpanded = isRetryableError(state.error)
                    if (logExpanded && state.logs.isNotBlank()) {
                        binding.contentScroll.post {
                            binding.contentScroll.smoothScrollTo(0, binding.logSection.top)
                        }
                    }
                }
            }
            lastStatus = state.status
            lastError = state.error
        }

        when (state.status) {
            ProxyState.Status.STOPPED -> renderStopped()
            ProxyState.Status.CONNECTING -> renderConnecting()
            ProxyState.Status.RUNNING -> renderRunning(state)
            ProxyState.Status.ERROR -> renderError(state)
        }

        updateConfigUi()
        updateStartHint()
    }

    private fun renderStopped() {
        val dotColor = getColorFromAttr(com.google.android.material.R.attr.colorOnSurfaceVariant)
        val textColor = getColorFromAttr(com.google.android.material.R.attr.colorOnSurface)
        setStatusUi(dotColor, textColor, getString(R.string.status_stopped))
        binding.statusDot.visibility = View.VISIBLE
        binding.connectingProgress.visibility = View.GONE
        binding.dashboard.visibility = View.GONE
        binding.actionButton.text = getString(R.string.start_proxy)
        binding.actionButton.isEnabled = canStart()
        setButtonTint()
        setInputsEnabled(true)
        binding.logSection.visibility = View.GONE
        stopProxyService()
    }

    private fun renderConnecting() {
        val color = getColorFromAttr(com.google.android.material.R.attr.colorTertiary)
        setStatusUi(
            color,
            color,
            getString(R.string.status_connecting),
            getString(R.string.status_detail_connecting)
        )
        binding.statusDot.visibility = View.GONE
        binding.connectingProgress.visibility = View.VISIBLE
        binding.dashboard.visibility = View.GONE
        binding.logSection.visibility = View.GONE
        binding.actionButton.text = getString(R.string.status_connecting)
        binding.actionButton.isEnabled = false
        setInputsEnabled(false)
    }

    private fun renderRunning(state: ProxyState) {
        val color = getColorFromAttr(androidx.appcompat.R.attr.colorPrimary)
        setStatusUi(color, color, getString(R.string.status_running))
        binding.statusDot.visibility = View.VISIBLE
        binding.connectingProgress.visibility = View.GONE
        binding.actionButton.text = getString(R.string.stop_proxy)
        binding.actionButton.isEnabled = true
        setButtonTint()
        setInputsEnabled(false)
        binding.dashboard.visibility = View.VISIBLE
        binding.addrValue.text = state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }
        binding.addrValue.contentDescription =
            "${getString(R.string.address_label)}: ${state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }}"

        updateRuntimeStats(state)

        val host = backendHost(state.webdavUrl)
        binding.backendText.isVisible = host != null
        binding.backendText.text = host?.let { getString(R.string.backend_fmt, it) }
        binding.backendText.contentDescription = binding.backendText.text

        renderDiagnostics(state, showEmpty = true)
        if (logExpanded && autoScrollLogs) {
            binding.contentScroll.post { binding.contentScroll.fullScroll(View.FOCUS_DOWN) }
        }

        if (state.passwordSaved) {
            binding.passwordInput.setText("")
        }
        updateForgetPasswordVisibility()
        if (!proxyServiceStarted) {
            proxyServiceStarted = true
            ProxyService.startRunning(this, state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR })
        }
    }

    private fun updateRuntimeStats(state: ProxyState) {
        val sessionsText = java.text.NumberFormat.getInstance().format(state.sessions)
        val elapsed = if (state.startedAt > 0L) System.currentTimeMillis() - state.startedAt else 0L
        val uptimeText = formatDuration(elapsed)
        val hasSessions = state.sessions > 0
        binding.statsText.text = if (hasSessions) {
            getString(R.string.stats_active_fmt, sessionsText, uptimeText)
        } else {
            getString(R.string.stats_idle_fmt, uptimeText)
        }
        binding.statsText.contentDescription = if (hasSessions) {
            "${getString(R.string.active_connections_label)}: $sessionsText, " +
                "${getString(R.string.uptime_label)}: $uptimeText"
        } else {
            "${getString(R.string.idle_label)}, ${getString(R.string.uptime_label)}: $uptimeText"
        }
        if (lastTestResult != null && testSpin == null) {
            applyTestResult(lastTestResult!!)
        }
    }

    private fun renderError(state: ProxyState) {
        val color = getColorFromAttr(androidx.appcompat.R.attr.colorError)
        setStatusUi(color, color, getString(R.string.status_error), errorMessage(state.error))
        binding.statusDot.visibility = View.VISIBLE
        binding.connectingProgress.visibility = View.GONE
        binding.dashboard.visibility = View.GONE
        binding.actionButton.text = getString(
            if (isRetryableError(state.error)) R.string.retry else R.string.start_proxy
        )
        binding.actionButton.isEnabled = canStart()
        setButtonTint()
        setInputsEnabled(true)
        renderDiagnostics(state, showEmpty = false)
        updateForgetPasswordVisibility()
        stopProxyService()
    }

    private fun renderDiagnostics(state: ProxyState, showEmpty: Boolean) {
        val visible = state.logs.isNotBlank() || showEmpty
        binding.logSection.visibility = if (visible) View.VISIBLE else View.GONE
        if (!visible) return
        binding.logText.text = highlightLogs(
            state.logs.ifBlank { getString(R.string.diagnostics_empty) }
        )
        applyLogExpandedState()
    }

    private fun updateConfigUi() {
        val status = proxyManager.state.value.status
        val editorVisible =
            status == ProxyState.Status.STOPPED || status == ProxyState.Status.ERROR
        binding.modeToggle.visibility = if (editorVisible) View.VISIBLE else View.GONE
        binding.fileSection.visibility = if (editorVisible && !isManualMode) View.VISIBLE else View.GONE
        binding.manualSection.visibility = if (editorVisible && isManualMode) View.VISIBLE else View.GONE
        binding.advancedHeader.visibility = if (editorVisible) View.VISIBLE else View.GONE
        binding.advancedContent.visibility =
            if (editorVisible && advancedExpanded) View.VISIBLE else View.GONE
    }

    private fun isPasswordSavedForCurrent(): Boolean {
        val prefs = PrefsStore.get(this)
        val hasSaved = !prefs.getString(PrefsStore.PREF_PASSWORD, "").isNullOrEmpty()
        val savedFor = prefs.getString(PrefsStore.PREF_PASSWORD_CONFIG, "") ?: ""
        val current = fileUri?.toString().orEmpty()
        return hasSaved && (savedFor.isEmpty() || savedFor == current)
    }

    private fun updateStartHint() {
        val state = proxyManager.state.value
        val hint = when (state.status) {
            ProxyState.Status.RUNNING,
            ProxyState.Status.CONNECTING -> null

            ProxyState.Status.STOPPED -> when {
                isManualMode && !hasManualConfig() -> R.string.hint_fill_fields

                !isManualMode && fileUri == null -> R.string.hint_select_config

                !isManualMode && isEncrypted && binding.passwordInput.text.isNullOrEmpty() &&
                    !isPasswordSavedForCurrent() -> R.string.hint_enter_password

                else -> R.string.hint_ready
            }

            ProxyState.Status.ERROR -> hintForError(state.error)
        }
        binding.startHint.text = hint?.let { getString(it) }.orEmpty()
        binding.startHint.isVisible = hint != null
    }

    private fun hintForError(kind: ProxyState.ErrorKind): Int? = when (kind) {
        ProxyState.ErrorKind.ENCRYPTED_NO_PASSWORD -> R.string.hint_enter_password

        ProxyState.ErrorKind.DECRYPT_FAILED -> R.string.hint_check_password

        ProxyState.ErrorKind.INVALID_CONFIG,
        ProxyState.ErrorKind.INVALID_KEY -> R.string.hint_replace_config

        ProxyState.ErrorKind.FILE_REQUIRED,
        ProxyState.ErrorKind.CONFIG_OPEN -> R.string.hint_select_config

        ProxyState.ErrorKind.MANUAL_FIELDS -> R.string.hint_fill_fields

        ProxyState.ErrorKind.STORAGE_INIT -> R.string.hint_check_webdav

        ProxyState.ErrorKind.LISTEN_FAILED -> R.string.hint_check_listen

        ProxyState.ErrorKind.RUNTIME,
        ProxyState.ErrorKind.ALREADY_RUNNING,
        ProxyState.ErrorKind.FAILED_START,
        ProxyState.ErrorKind.UNKNOWN,
        ProxyState.ErrorKind.NONE -> R.string.hint_retry
    }

    private fun isRetryableError(kind: ProxyState.ErrorKind): Boolean = when (kind) {
        ProxyState.ErrorKind.RUNTIME,
        ProxyState.ErrorKind.STORAGE_INIT,
        ProxyState.ErrorKind.LISTEN_FAILED,
        ProxyState.ErrorKind.ALREADY_RUNNING,
        ProxyState.ErrorKind.FAILED_START,
        ProxyState.ErrorKind.UNKNOWN -> true

        else -> false
    }

    private fun backendHost(raw: String): String? {
        if (raw.isBlank()) return null
        return runCatching {
            val uri = raw.toUri()
            val host = uri.host ?: return@runCatching raw
            if (uri.port > 0) "$host:${uri.port}" else host
        }.getOrNull()
    }

    private fun setStatusUi(dotColor: Int, textColor: Int, text: String, detail: String? = null) {
        (binding.statusDot.background as? GradientDrawable)?.setColor(dotColor)
        binding.statusText.setTextColor(textColor)
        binding.statusText.text = text
        binding.statusDetailText.text = detail.orEmpty()
        binding.statusDetailText.isVisible = !detail.isNullOrBlank()
    }

    private fun errorMessage(kind: ProxyState.ErrorKind): String = getString(
        when (kind) {
            ProxyState.ErrorKind.ENCRYPTED_NO_PASSWORD -> R.string.error_no_password

            ProxyState.ErrorKind.DECRYPT_FAILED -> R.string.error_decrypt_failed

            ProxyState.ErrorKind.INVALID_CONFIG -> R.string.error_invalid_config

            ProxyState.ErrorKind.INVALID_KEY -> R.string.error_invalid_key

            ProxyState.ErrorKind.ALREADY_RUNNING -> R.string.error_already_running

            ProxyState.ErrorKind.STORAGE_INIT -> R.string.error_storage

            ProxyState.ErrorKind.LISTEN_FAILED -> R.string.error_listen_failed

            ProxyState.ErrorKind.RUNTIME -> R.string.error_runtime

            ProxyState.ErrorKind.MANUAL_FIELDS -> R.string.error_manual_fields

            ProxyState.ErrorKind.FILE_REQUIRED -> R.string.error_file_required

            ProxyState.ErrorKind.CONFIG_OPEN -> R.string.error_config_open

            ProxyState.ErrorKind.FAILED_START -> R.string.error_failed_start

            ProxyState.ErrorKind.NONE,
            ProxyState.ErrorKind.UNKNOWN -> R.string.error_unknown
        }
    )

    private fun getColorFromAttr(attr: Int): Int = MaterialColors.getColor(this, attr, 0)

    // The CTA is the screen's single primary action: keep it a filled primary
    // button in both states (the label and status convey the state). A stateful
    // tint keeps it visibly disabled when it cannot start.
    private fun setButtonTint() {
        val bg = getColorFromAttr(androidx.appcompat.R.attr.colorPrimary)
        val fg = getColorFromAttr(com.google.android.material.R.attr.colorOnPrimary)
        val onSurface = getColorFromAttr(com.google.android.material.R.attr.colorOnSurface)
        val disabledBg = androidx.core.graphics.ColorUtils.setAlphaComponent(onSurface, 0x1F)
        val disabledFg = androidx.core.graphics.ColorUtils.setAlphaComponent(onSurface, 0x61)
        binding.actionButton.backgroundTintList = android.content.res.ColorStateList(
            arrayOf(intArrayOf(-android.R.attr.state_enabled), intArrayOf()),
            intArrayOf(disabledBg, bg)
        )
        binding.actionButton.setTextColor(
            android.content.res.ColorStateList(
                arrayOf(intArrayOf(-android.R.attr.state_enabled), intArrayOf()),
                intArrayOf(disabledFg, fg)
            )
        )
    }

    private fun clearInputErrors() {
        binding.listenAddrLayout.error = null
        binding.socks5UserLayout.error = null
        binding.socks5PassLayout.error = null
        binding.webdavUrlLayout.error = null
        binding.webdavLoginLayout.error = null
        binding.webdavTokenLayout.error = null
        binding.encKeyLayout.error = null
        binding.hmacKeyLayout.error = null
    }

    private fun setInputsEnabled(enabled: Boolean) {
        binding.configChip.isEnabled = enabled
        binding.scanQrButton.isEnabled = enabled
        binding.passwordLayout.isEnabled = enabled
        binding.modeToggle.isEnabled = enabled
        binding.webdavUrlInput.isEnabled = enabled
        binding.webdavLoginInput.isEnabled = enabled
        binding.webdavTokenInput.isEnabled = enabled
        binding.encKeyLayout.isEnabled = enabled
        binding.hmacKeyLayout.isEnabled = enabled
        binding.listenAddrInput.isEnabled = enabled
        binding.socks5UserInput.isEnabled = enabled
        binding.socks5PassInput.isEnabled = enabled
        binding.advancedHeader.isEnabled = enabled
    }

    private fun showFileChip(name: String) {
        binding.configChip.text = name
        binding.configChip.setCloseIconVisible(true)
        binding.configChip.contentDescription = "${getString(R.string.config_chip_desc)}: $name"
    }

    private fun pasteFromClipboard(clipboard: android.content.ClipboardManager, target: TextInputEditText) {
        val clip = clipboard.primaryClip
        if (clip != null && clip.itemCount > 0) {
            target.setText(clip.getItemAt(0).text)
        }
    }

    private fun getDisplayName(uri: Uri): String {
        val fallback = getString(R.string.config_display_fallback)
        return try {
            contentResolver.query(uri, null, null, null, null)?.use { cursor ->
                val nameIdx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (nameIdx >= 0 && cursor.moveToFirst()) {
                    cursor.getString(nameIdx) ?: fallback
                } else {
                    fallback
                }
            } ?: fallback
        } catch (e: Exception) {
            fallback
        }
    }

    private fun updatePasswordVisibility() {
        binding.passwordLayout.visibility = if (isEncrypted) View.VISIBLE else View.GONE
    }

    private fun updateForgetPasswordVisibility() {
        val prefs = PrefsStore.get(this)
        val hasSaved = !prefs.getString(PrefsStore.PREF_PASSWORD, "").isNullOrEmpty()
        val savedFor = prefs.getString(PrefsStore.PREF_PASSWORD_CONFIG, "") ?: ""
        val current = fileUri?.toString().orEmpty()
        val matches = savedFor.isEmpty() || savedFor == current
        val decryptFailed = proxyManager.state.value.error == ProxyState.ErrorKind.DECRYPT_FAILED
        binding.forgetPasswordBtn.visibility =
            if (hasSaved && fileUri != null) View.VISIBLE else View.GONE
        binding.passwordLayout.helperText = if (isEncrypted) {
            getString(
                when {
                    decryptFailed -> R.string.password_wrong_hint
                    hasSaved && matches -> R.string.password_hint
                    else -> R.string.password_required
                }
            )
        } else {
            null
        }
    }

    private fun hasManualConfig(): Boolean = listOf(
        binding.webdavUrlInput,
        binding.webdavLoginInput,
        binding.webdavTokenInput,
        binding.encKeyInput,
        binding.hmacKeyInput
    ).all { !it.text.isNullOrBlank() }

    private fun canStart(): Boolean = if (isManualMode) {
        hasManualConfig()
    } else {
        fileUri != null && (
            !isEncrypted ||
                !binding.passwordInput.text.isNullOrEmpty() ||
                isPasswordSavedForCurrent()
            )
    }

    // Keep the sticky Start CTA in sync with the current mode/inputs. Only
    // relevant while stopped or errored; other states drive the button
    // themselves (Connecting = disabled, Running = Stop).
    private fun refreshStartEnabled() {
        val status = proxyManager.state.value.status
        if (status == ProxyState.Status.STOPPED || status == ProxyState.Status.ERROR) {
            binding.actionButton.isEnabled = canStart()
            updateConfigUi()
            updateStartHint()
        }
    }

    // The collapsed Advanced row shows the current listen address instead of a
    // static feature list, so it carries information at a glance.
    private fun updateAdvancedSummary() {
        val addr = binding.listenAddrInput.text?.toString()?.trim()
            .orEmpty()
            .ifEmpty { DEFAULT_LISTEN_ADDR }
        binding.advancedSummary.text = getString(R.string.advanced_summary_fmt, addr)
        binding.advancedSummary.isVisible = !advancedExpanded
    }

    private fun applyLogExpandedState(animate: Boolean = false) {
        binding.logText.visibility = if (logExpanded) View.VISIBLE else View.GONE
        if (animate) {
            binding.logArrow.animate()
                .rotation(if (logExpanded) 180f else 0f)
                .setDuration(150)
                .start()
        } else {
            binding.logArrow.rotation = if (logExpanded) 180f else 0f
        }
        ViewCompat.setStateDescription(
            binding.logHeader,
            getString(if (logExpanded) R.string.advanced_state_expanded else R.string.advanced_state_collapsed)
        )
    }

    private fun snack(resId: Int, duration: Int = Snackbar.LENGTH_LONG) {
        _binding?.let { Snackbar.make(it.root, resId, duration).show() }
    }

    private fun launchScanner() {
        val options = ScanOptions()
            .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            .setBeepEnabled(false)
            .setPrompt(getString(R.string.qr_prompt))
        scanLauncher.launch(options)
    }

    private fun handleIntent(intent: Intent?) {
        intent ?: return
        when (intent.action) {
            Intent.ACTION_VIEW -> {
                val data = intent.dataString
                if (data != null) {
                    clipboardOffered = true
                    offerImport(data, getString(R.string.import_source_link))
                }
            }

            Intent.ACTION_SEND -> {
                val stream: Uri? = IntentCompat.getParcelableExtra(
                    intent,
                    Intent.EXTRA_STREAM,
                    Uri::class.java
                )
                if (stream != null) {
                    // Shared file (e.g. a .json from the Files app).
                    clipboardOffered = true
                    val bytes = ContentResolverConfigReader(this).read(stream).getOrNull()
                    if (bytes != null) {
                        offerImport(bytes.toString(Charsets.UTF_8), getString(R.string.import_source_shared))
                    } else {
                        snack(R.string.config_read_failed)
                    }
                } else {
                    val text = intent.getStringExtra(Intent.EXTRA_TEXT)
                    if (text != null) {
                        clipboardOffered = true
                        offerImport(text, getString(R.string.import_source_shared))
                    }
                }
            }
        }
    }

    /**
     * Imports config bytes from a flowdav:// URI or raw JSON text: stores
     * them in app-private storage and selects them as the active config.
     * @return true when the payload was recognized and stored.
     */
    private fun importConfigText(text: String) {
        lifecycleScope.launch {
            // The IO worker hands back the URI; the UI thread owns fileUri.
            val storedUri = withContext(Dispatchers.IO) {
                val bytes = decodeImport(text) ?: return@withContext null
                if (bytes.isEmpty() || bytes.size > ContentResolverConfigReader.MAX_CONFIG_BYTES) {
                    return@withContext null
                }
                try {
                    val dir = File(filesDir, "imported").apply { mkdirs() }
                    val file = File(dir, "imported-config.dat")
                    file.writeBytes(bytes)
                    val uri = file.toUri()
                    PrefsStore.get(this@MainActivity)
                        .edit { putString(PrefsStore.PREF_URI, uri.toString()) }
                    uri
                } catch (e: Exception) {
                    Log.w(TAG, "config import failed", e)
                    null
                }
            }
            _binding ?: return@launch
            if (storedUri != null) {
                fileUri = storedUri
                refreshFileInfo(storedUri)
                refreshStartEnabled()
                snack(R.string.config_imported, Snackbar.LENGTH_SHORT)
            }
        }
    }

    private fun decodeImport(text: String): ByteArray? {
        val trimmed = text.trim()
        if (trimmed.isEmpty()) return null
        // getQueryParameter throws on malformed percent-encoding (undocumented
        // but real); input comes from outside the app — never crash the UI.
        return runCatching {
            when {
                trimmed.startsWith("flowdav://") -> {
                    val data = trimmed.toUri().getQueryParameter("data") ?: return null
                    Base64.decode(data, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
                }

                trimmed.startsWith("{") -> trimmed.toByteArray(Charsets.UTF_8)

                else -> null
            }
        }.getOrElse { e ->
            Log.w(TAG, "malformed import payload", e)
            null
        }
    }

    /**
     * Anything that arrives from outside the app (link, share, QR, clipboard)
     * goes through an explicit confirmation with the config source and the
     * WebDAV host, so a malicious config cannot silently replace the active one.
     */
    private fun offerImport(text: String, source: String) {
        val bytes = decodeImport(text)
        if (bytes == null || bytes.isEmpty() || bytes.size > ContentResolverConfigReader.MAX_CONFIG_BYTES) {
            // Non-QR sources are not failures of scanning; blame the payload.
            val res = if (source == getString(R.string.import_source_qr)) {
                R.string.qr_import_failed
            } else {
                R.string.import_payload_invalid
            }
            snack(res)
            return
        }
        MaterialAlertDialogBuilder(this)
            .setTitle(R.string.import_confirm_title)
            .setMessage(getString(R.string.import_confirm_body, source, configDescription(bytes)))
            .setPositiveButton(R.string.import_action) { _, _ -> importConfigText(text) }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun configDescription(bytes: ByteArray): String {
        val json = try {
            JSONObject(String(bytes, Charsets.UTF_8))
        } catch (e: Exception) {
            return getString(R.string.import_confirm_encrypted)
        }
        val webdav = json.optJSONObject("webdav")
        val backend = webdav?.optJSONArray("backends")?.optJSONObject(0) ?: webdav
        val url = backend?.optString("url").orEmpty()
        val host = runCatching { url.toUri().host }.getOrNull()
        return if (!host.isNullOrBlank()) {
            getString(R.string.import_confirm_host, host)
        } else {
            getString(R.string.import_confirm_unverified)
        }
    }

    private fun maybeOfferClipboardImport() {
        if (clipboardOffered || fileUri != null) return
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager ?: return
        val clip = cm.primaryClip ?: return
        if (clip.itemCount == 0) return
        val text = clip.getItemAt(0)?.coerceToText(this)?.toString()?.trim() ?: return
        if (!text.startsWith("flowdav://") && !text.startsWith("{")) return
        clipboardOffered = true
        Snackbar.make(binding.root, R.string.clipboard_config_found, Snackbar.LENGTH_LONG)
            .setAction(R.string.import_action) {
                offerImport(text, getString(R.string.import_source_clipboard))
            }
            .show()
    }

    /** Bound address while running, configured address otherwise. */
    private fun currentListenAddr(): String {
        val state = proxyManager.state.value
        return if (state.status == ProxyState.Status.RUNNING) {
            state.listenAddr.ifBlank { configuredListenAddr() }
        } else {
            configuredListenAddr()
        }
    }

    private fun configuredListenAddr(): String = binding.listenAddrInput.text
        ?.toString()
        ?.trim()
        ?.ifBlank { DEFAULT_LISTEN_ADDR }
        ?: DEFAULT_LISTEN_ADDR

    private fun proxyUri(): String {
        val addr = currentListenAddr()
        val user = binding.socks5UserInput.text?.toString()?.ifBlank { null }
        val pass = binding.socks5PassInput.text?.toString()?.ifBlank { null }
        return if (user != null && pass != null) "socks5://$user:$pass@$addr" else "socks5://$addr"
    }

    private fun copyProxyUri() {
        val clip = android.content.ClipData.newPlainText("flowdav proxy", proxyUri())
        if (Build.VERSION.SDK_INT >= 33) {
            clip.description.extras = PersistableBundle().apply {
                putBoolean(ClipDescription.EXTRA_IS_SENSITIVE, true)
            }
        }
        (getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager)
            ?.setPrimaryClip(clip)
    }

    /** One-tap copy of the proxy address, with credentials when configured. */
    private fun copyProxyAddress() {
        copyProxyUri()
        snack(R.string.address_copied, Snackbar.LENGTH_SHORT)
    }

    private fun runConnectionTest() {
        val state = proxyManager.state.value
        val addr = state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }
        val port = addr.substringAfterLast(":").toIntOrNull()
        if (port == null) {
            snack(R.string.ct_addr_invalid)
            return
        }
        val host = addr.substringBeforeLast(":").removeSurrounding("[", "]")
        val user = binding.socks5UserInput.text?.toString()?.ifBlank { null }
        val pass = binding.socks5PassInput.text?.toString()?.ifBlank { null }
        lastTestResult = null
        lastTestAt = 0L
        startTestSpin()
        binding.testStatusText.text = getString(R.string.test_connection_running)
        binding.testStatusText.setTextColor(
            getColorFromAttr(com.google.android.material.R.attr.colorOnSurface)
        )
        binding.testDetailText.visibility = View.GONE
        testJob?.cancel()
        testJob = lifecycleScope.launch {
            val result = withContext(Dispatchers.IO) { ConnectionTest.probe(host, port, user, pass) }
            _binding ?: return@launch
            stopTestSpin()
            showTestResult(result)
        }
    }

    /** Same icon, just spinning — no size swap while a test runs. */
    private fun startTestSpin() {
        binding.testConnectionButton.isEnabled = false
        testSpin = android.animation.ObjectAnimator.ofFloat(
            binding.testConnectionButton,
            View.ROTATION,
            0f,
            360f
        ).apply {
            duration = 900
            repeatCount = android.animation.ValueAnimator.INFINITE
            interpolator = android.view.animation.LinearInterpolator()
            start()
        }
    }

    private fun stopTestSpin() {
        testSpin?.cancel()
        testSpin = null
        binding.testConnectionButton.rotation = 0f
        binding.testConnectionButton.isEnabled = true
    }

    private fun resetTestUi() {
        testJob?.cancel()
        testJob = null
        stopTestSpin()
        lastTestResult = null
        lastTestAt = 0L
        binding.testStatusText.text = getString(R.string.connection_not_tested)
        binding.testStatusText.setTextColor(
            getColorFromAttr(com.google.android.material.R.attr.colorOnSurfaceVariant)
        )
        binding.testDetailText.text = getString(R.string.test_hint_not_verified)
        binding.testDetailText.visibility = View.VISIBLE
    }

    private fun showTestResult(r: ConnectionTest.Result) {
        lastTestResult = r
        lastTestAt = System.currentTimeMillis()
        applyTestResult(r)
    }

    /** Headline = state (accent); detail = exit IP/time (muted). */
    private fun applyTestResult(r: ConnectionTest.Result) {
        val color = getColorFromAttr(
            if (r.ok) {
                com.google.android.material.R.attr.colorOnSurface
            } else {
                androidx.appcompat.R.attr.colorError
            }
        )
        val (title, detail) = testVerdict(r)
        binding.testStatusText.text = title
        binding.testStatusText.setTextColor(color)
        if (detail != null) {
            binding.testDetailText.text = detail
            binding.testDetailText.visibility = View.VISIBLE
        } else {
            binding.testDetailText.visibility = View.GONE
        }
    }

    private fun checkedLabel(): String {
        if (lastTestAt <= 0L) return getString(R.string.ct_checked_just_now)
        val elapsed = System.currentTimeMillis() - lastTestAt
        return when {
            elapsed < 60_000L -> getString(R.string.ct_checked_just_now)

            elapsed < 3_600_000L -> getString(
                R.string.ct_checked_min_ago,
                (elapsed / 60_000L).toInt()
            )

            else -> getString(R.string.ct_checked_hour_ago, (elapsed / 3_600_000L).toInt())
        }
    }

    private fun testVerdict(r: ConnectionTest.Result): Pair<String, String?> = when (r.kind) {
        ConnectionTest.Kind.PROXY_NOT_RUNNING ->
            getString(R.string.ct_state_not_running) to getString(R.string.ct_detail_not_running)

        ConnectionTest.Kind.STORAGE ->
            getString(R.string.ct_state_storage_error) to when (r.detail) {
                "not_running" -> getString(R.string.ct_storage_not_running)
                "storage_auth" -> getString(R.string.ct_storage_auth)
                "storage_unreachable" -> getString(R.string.ct_storage_unreachable)
                else -> getString(R.string.ct_storage_error_fmt, r.detail)
            }

        ConnectionTest.Kind.OK_EXIT ->
            getString(R.string.ct_state_connected) to
                getString(R.string.ct_detail_exit_fmt, r.ip, checkedLabel())

        ConnectionTest.Kind.OK_SERVER_TIMEOUT ->
            getString(R.string.ct_state_ready) to getString(R.string.ct_detail_warming)

        ConnectionTest.Kind.OK_SERVER_SILENT ->
            getString(R.string.ct_state_ready) to getString(R.string.ct_detail_silent)

        ConnectionTest.Kind.OK_SERVER_ERROR ->
            getString(R.string.ct_state_ready) to getString(R.string.ct_detail_server_error)
    }

    // A dialog is the canonical surface for a brief, read-once explanation.
    // Inflating with a null parent is fine for a dialog (lint's InflateParams
    // advice targets views added to a live hierarchy).
    @Suppress("InflateParams")
    private fun showHowTo() {
        val view = layoutInflater.inflate(R.layout.dialog_howto, null)

        view.findViewById<TextView>(R.id.howtoEndpoint).text = currentListenAddr()

        val user = binding.socks5UserInput.text?.toString()?.ifBlank { null }
        val pass = binding.socks5PassInput.text?.toString()?.ifBlank { null }
        view.findViewById<View>(R.id.howtoAuth).isVisible = user != null && pass != null

        view.findViewById<View>(R.id.howtoCopy).setOnClickListener {
            copyProxyUri()
            Toast.makeText(this, R.string.address_copied, Toast.LENGTH_SHORT).show()
        }

        // Colophon: the version is a small link to the project repo.
        val footer = view.findViewById<TextView>(R.id.howtoVersion)
        val version = appVersionName()
        val full = "${getString(R.string.about_version_fmt, version)} ↗"
        val ss = SpannableString(full)
        val muted = getColorFromAttr(com.google.android.material.R.attr.colorOnSurfaceVariant)
        ss.setSpan(
            ForegroundColorSpan(muted),
            0,
            full.length,
            Spanned.SPAN_EXCLUSIVE_EXCLUSIVE
        )
        ss.setSpan(
            object : ClickableSpan() {
                override fun onClick(widget: View) {
                    openRepoUrl()
                }

                override fun updateDrawState(ds: TextPaint) {
                    super.updateDrawState(ds)
                    ds.isUnderlineText = false
                    ds.color = muted
                }
            },
            0,
            full.length,
            Spanned.SPAN_EXCLUSIVE_EXCLUSIVE
        )
        footer.text = ss
        footer.movementMethod = LinkMovementMethod.getInstance()
        footer.highlightColor = Color.TRANSPARENT

        MaterialAlertDialogBuilder(this)
            .setTitle(R.string.howto_title)
            .setView(view)
            .setPositiveButton(R.string.howto_close, null)
            .show()
    }

    private fun appVersionName(): String = try {
        packageManager.getPackageInfo(packageName, 0).versionName ?: "?"
    } catch (e: PackageManager.NameNotFoundException) {
        "?"
    }

    private fun openRepoUrl() {
        try {
            startActivity(Intent(Intent.ACTION_VIEW, REPO_URL.toUri()))
        } catch (e: Exception) {
            Log.w(TAG, "repo link failed", e)
        }
    }

    // Play policy: REQUEST_IGNORE_BATTERY_OPTIMIZATIONS is acceptable for a
    // user-visible VPN/proxy tool (flowdav is distributed outside the Play
    // Store); without the exemption OEM power managers kill the tunnel.
    @Suppress("BatteryLife")
    private fun maybeRequestBatteryExemption() {
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        if (pm.isIgnoringBatteryOptimizations(packageName)) return
        val prefs = PrefsStore.get(this)
        if (prefs.getBoolean(PrefsStore.PREF_BATTERY_ASKED, false)) return
        prefs.edit { putBoolean(PrefsStore.PREF_BATTERY_ASKED, true) }
        AlertDialog.Builder(this)
            .setTitle(R.string.battery_title)
            .setMessage(R.string.battery_message)
            .setPositiveButton(R.string.battery_allow) { _, _ ->
                try {
                    startActivity(
                        Intent(
                            Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                            "package:$packageName".toUri()
                        )
                    )
                } catch (e: Exception) {
                    Log.w(TAG, "battery exemption intent failed", e)
                }
            }
            .setNegativeButton(R.string.battery_skip, null)
            .show()
    }

    @RequiresApi(Build.VERSION_CODES.TIRAMISU)
    private fun showNotifRationale() {
        AlertDialog.Builder(this)
            .setTitle(R.string.notif_permission_title)
            .setMessage(R.string.notif_permission_rationale)
            .setCancelable(false)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                notifPermLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
            }
            .setNegativeButton(android.R.string.cancel) { _, _ ->
                window?.decorView?.rootView?.let { root ->
                    Snackbar.make(root, R.string.notif_permission_denied, Snackbar.LENGTH_LONG).show()
                }
            }
            .show()
    }

    private fun highlightLogs(text: String): SpannableString {
        val ss = SpannableString(text)
        val warnColor = getColorFromAttr(com.google.android.material.R.attr.colorTertiary)
        val errorColor = getColorFromAttr(androidx.appcompat.R.attr.colorError)
        var offset = 0
        for (line in text.split("\n")) {
            val start = offset
            offset += line.length + 1
            val end = (start + line.length).coerceAtMost(text.length)
            if (line.contains("error", ignoreCase = true)) {
                ss.setSpan(ForegroundColorSpan(errorColor), start, end, 0)
            } else if (line.contains("warning", ignoreCase = true)) {
                ss.setSpan(ForegroundColorSpan(warnColor), start, end, 0)
            }
        }
        return ss
    }

    private fun formatDuration(ms: Long): String = android.text.format.DateUtils.formatElapsedTime(ms / 1000)

    private fun collectManualFields() = ProxyManager.ManualFields(
        binding.webdavUrlInput.text?.toString() ?: "",
        binding.webdavLoginInput.text?.toString() ?: "",
        binding.webdavTokenInput.text?.toString() ?: "",
        binding.encKeyInput.text?.toString() ?: "",
        binding.hmacKeyInput.text?.toString() ?: ""
    )

    companion object {
        private const val TAG = "MainActivity"
        private const val REPO_URL = "https://github.com/lyafence/flowdav"
    }
}
