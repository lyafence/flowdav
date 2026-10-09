package com.flowdav.app

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.text.SpannableString
import android.text.style.ForegroundColorSpan
import android.transition.TransitionManager
import android.util.Log
import android.view.View
import android.view.WindowManager
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.annotation.RequiresApi
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.edit
import androidx.core.net.toUri
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.lifecycleScope
import com.flowdav.app.databinding.ActivityMainBinding
import com.google.android.material.color.DynamicColors
import com.google.android.material.color.MaterialColors
import com.google.android.material.snackbar.Snackbar
import com.google.android.material.textfield.TextInputEditText
import java.net.InetSocketAddress
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : AppCompatActivity() {

    private val proxyManager: ProxyManager by lazy {
        ViewModelProvider(
            this,
            ProxyManagerFactory(ContentResolverConfigReader(applicationContext))
        )[ProxyManager::class.java]
    }

    private var _binding: ActivityMainBinding? = null
    private val binding get() = _binding!!

    private var fileUri: android.net.Uri? = null
    private var isManualMode = false
    private var isEncrypted = true
    private var proxyServiceStarted = false
    private var autoScrollLogs = true

    private val notifPermLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (!granted) {
            window?.decorView?.rootView?.let { root ->
                Snackbar.make(root, R.string.notif_permission_denied, Snackbar.LENGTH_LONG).show()
            }
        }
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
            PrefsStore.get(this).edit { putString(PREF_URI, uri.toString()) }
            refreshFileInfo(uri)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
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

        savedInstanceState?.let { state ->
            isManualMode = state.getBoolean("isManualMode", false)
            isEncrypted = state.getBoolean("isEncrypted", true)
            if (state.getBoolean("advancedExpanded", false)) {
                binding.advancedContent.visibility = View.VISIBLE
                binding.advancedArrow.text = getString(R.string.arrow_expanded)
            }
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

        setupListeners()

        val savedUri = PrefsStore.get(this).getString(PREF_URI, null)
        if (savedUri != null) {
            fileUri = savedUri.toUri()
            fileUri?.let { refreshFileInfo(it) }
        }

        // Never render the master password; it is loaded from encrypted prefs
        // internally when the field is left empty.
        binding.passwordInput.setText("")

        val versionName = try {
            packageManager.getPackageInfo(packageName, 0).versionName
        } catch (e: PackageManager.NameNotFoundException) {
            "?"
        }
        binding.versionText.text = getString(R.string.version_fmt, versionName)

        lifecycleScope.launch {
            proxyManager.state.collectLatest { state -> render(state) }
        }
    }

    override fun onDestroy() {
        _binding = null
        super.onDestroy()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean("isManualMode", isManualMode)
        outState.putBoolean("isEncrypted", isEncrypted)
        outState.putBoolean("advancedExpanded", binding.advancedContent.isVisible)
        proxyManager.saveDraft(collectManualFields())
    }

    private fun setupListeners() {
        binding.modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            binding.fileSection.visibility = if (isManualMode) View.GONE else View.VISIBLE
            binding.manualSection.visibility = if (isManualMode) View.VISIBLE else View.GONE
        }

        binding.configChip.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        binding.configChip.setOnCloseIconClickListener {
            fileUri = null
            binding.configChip.text = getString(R.string.select_file)
            binding.configChip.contentDescription = getString(R.string.select_file)
            binding.configChip.setCloseIconVisible(false)
        }

        binding.contentScroll.viewTreeObserver.addOnScrollChangedListener {
            val sv = binding.contentScroll
            autoScrollLogs = sv.getChildAt(0).bottom - (sv.height + sv.scrollY) <= 0
        }

        binding.advancedHeader.setOnClickListener {
            val expanded = binding.advancedContent.isVisible
            TransitionManager.beginDelayedTransition(binding.advancedHeader)
            binding.advancedContent.visibility = if (expanded) View.GONE else View.VISIBLE
            binding.advancedArrow.text = if (expanded) getString(R.string.arrow_collapsed) else getString(R.string.arrow_expanded)
        }

        binding.copyLogsBtn.setOnClickListener {
            val text = binding.logText.text?.toString() ?: return@setOnClickListener
            val clip = android.content.ClipData.newPlainText("flowdav logs", text)
            (getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager)
                ?.setPrimaryClip(clip)
            Snackbar.make(binding.root, R.string.logs_copied, Snackbar.LENGTH_SHORT).show()
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
            val pw = typed.ifEmpty { PrefsStore.get(this).getString(PREF_PASSWORD, "") ?: "" }
            if (typed.isNotEmpty()) PrefsStore.get(this).edit { putString(PREF_PASSWORD, typed) }
            pw
        } else {
            ""
        }

        currentFocus?.let { view ->
            val imm = getSystemService(Context.INPUT_METHOD_SERVICE) as? android.view.inputmethod.InputMethodManager
            imm?.hideSoftInputFromWindow(view.windowToken, 0)
            view.clearFocus()
        }

        proxyManager.start(listenAddr, socks5User, socks5Pass, fileUri, password, isManualMode, manualFields)
    }

    private fun stopProxy() {
        proxyManager.stop()
        stopService(Intent(this, ProxyService::class.java))
        proxyServiceStarted = false
    }

    private fun refreshFileInfo(uri: android.net.Uri) {
        lifecycleScope.launch {
            val name = withContext(Dispatchers.IO) { getDisplayName(uri) }
            val firstByte: Int? = withContext(Dispatchers.IO) {
                try {
                    contentResolver.openInputStream(uri)?.use { it.read() }
                } catch (_: Exception) {
                    null
                }
            }
            showFileChip(name)
            if (firstByte == null) {
                Snackbar.make(binding.root, R.string.config_read_failed, Snackbar.LENGTH_LONG).show()
            }
            isEncrypted = (firstByte ?: -1) != '{'.code
            updatePasswordVisibility()
        }
    }

    private fun render(state: ProxyState) {
        when (state.status) {
            ProxyState.Status.STOPPED -> {
                val color = getColorFromAttr(com.google.android.material.R.attr.colorOnSurfaceVariant)
                setStatusUi(color, color, getString(R.string.status_stopped))
                binding.statusDot.visibility = View.VISIBLE
                binding.connectingProgress.visibility = View.GONE
                binding.dashboard.visibility = View.GONE
                binding.logSection.visibility = View.GONE
                binding.actionButton.text = getString(R.string.start_proxy)
                binding.actionButton.isEnabled = true
                setButtonTint(false)
                setInputsEnabled(true)
                proxyServiceStarted = false
            }

            ProxyState.Status.CONNECTING -> {
                val color = getColorFromAttr(com.google.android.material.R.attr.colorTertiary)
                setStatusUi(color, color, getString(R.string.status_connecting))
                binding.statusDot.visibility = View.GONE
                binding.connectingProgress.visibility = View.VISIBLE
                binding.actionButton.isEnabled = false
                setInputsEnabled(false)
            }

            ProxyState.Status.RUNNING -> {
                val color = getColorFromAttr(androidx.appcompat.R.attr.colorPrimary)
                setStatusUi(color, color, getString(R.string.status_running))
                binding.statusDot.visibility = View.VISIBLE
                binding.connectingProgress.visibility = View.GONE
                binding.actionButton.text = getString(R.string.stop_proxy)
                binding.actionButton.isEnabled = true
                setButtonTint(true)
                setInputsEnabled(false)
                binding.dashboard.visibility = View.VISIBLE
                binding.addrValue.text = state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }
                val sessionsText = java.text.NumberFormat.getInstance().format(state.sessions)
                binding.sessionsValue.text = sessionsText
                val elapsed = if (state.startedAt > 0L) System.currentTimeMillis() - state.startedAt else 0L
                binding.uptimeValue.text = formatDuration(elapsed)
                val logs = state.logs
                if (logs.isNotBlank()) {
                    binding.logText.text = highlightLogs(logs)
                    binding.logSection.visibility = View.VISIBLE
                    if (autoScrollLogs) {
                        binding.contentScroll.post { binding.contentScroll.fullScroll(android.view.View.FOCUS_DOWN) }
                    }
                }
                binding.addrValue.contentDescription =
                    "${getString(R.string.address_label)}: ${state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }}"
                binding.sessionsValue.contentDescription = "${getString(R.string.sessions_label)}: $sessionsText"
                binding.uptimeValue.contentDescription = "${getString(R.string.uptime_label)}: ${formatDuration(elapsed)}"
                if (!proxyServiceStarted) {
                    proxyServiceStarted = true
                    ProxyService.startRunning(this)
                } else {
                    ProxyService.updateNotification(this, state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }, formatDuration(elapsed))
                }
            }

            ProxyState.Status.ERROR -> {
                val color = getColorFromAttr(androidx.appcompat.R.attr.colorError)
                setStatusUi(color, color, errorMessage(state.error))
                binding.statusDot.visibility = View.VISIBLE
                binding.connectingProgress.visibility = View.GONE
                binding.dashboard.visibility = View.GONE
                binding.actionButton.text = getString(R.string.start_proxy)
                binding.actionButton.isEnabled = true
                setButtonTint(false)
                setInputsEnabled(true)
                proxyServiceStarted = false
            }
        }
    }

    private fun setStatusUi(dotColor: Int, textColor: Int, text: String) {
        (binding.statusDot.background as? GradientDrawable)?.setColor(dotColor)
        binding.statusText.setTextColor(textColor)
        binding.statusText.text = text
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

    private fun setButtonTint(running: Boolean) {
        val bg = getColorFromAttr(
            if (running) {
                androidx.appcompat.R.attr.colorPrimary
            } else {
                com.google.android.material.R.attr.colorPrimaryContainer
            }
        )
        val fg = getColorFromAttr(
            if (running) {
                com.google.android.material.R.attr.colorOnPrimary
            } else {
                com.google.android.material.R.attr.colorOnPrimaryContainer
            }
        )
        binding.actionButton.backgroundTintList = android.content.res.ColorStateList.valueOf(bg)
        binding.actionButton.setTextColor(fg)
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

    private fun getDisplayName(uri: android.net.Uri): String {
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
        var offset = 0
        for (line in text.split("\n")) {
            val start = offset
            offset += line.length + 1
            if (line.contains("warning", ignoreCase = true)) {
                val end = (start + line.length).coerceAtMost(text.length)
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
        private const val PREF_PASSWORD = "password"
        private const val PREF_URI = "config_uri"
        private const val DEFAULT_LISTEN_ADDR = "127.0.0.1:1080"
    }
}
