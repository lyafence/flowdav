package com.flowdav.app

import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.SharedPreferences
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.text.SpannableString
import android.text.style.ForegroundColorSpan
import android.transition.TransitionManager
import android.util.Log
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.lifecycleScope
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
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
    private val b get() = _binding!!

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
                    uri, Intent.FLAG_GRANT_READ_URI_PERMISSION
                )
            } catch (e: Exception) {
                Log.w(TAG, "takePersistableUriPermission failed for $uri", e)
            }
            getPrefs().edit().putString(PREF_URI, uri.toString()).apply()
            refreshFileInfo(uri)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        DynamicColors.applyToActivityIfAvailable(this)

        _binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(b.root)

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

        b.configChip.text = getString(R.string.select_file)

        savedInstanceState?.let { state ->
            isManualMode = state.getBoolean("isManualMode", false)
            isEncrypted = state.getBoolean("isEncrypted", true)
            if (state.getBoolean("advancedExpanded", false)) {
                b.advancedContent.visibility = View.VISIBLE
                b.advancedArrow.text = getString(R.string.arrow_expanded)
            }
        }

        if (isManualMode) {
            b.fileSection.visibility = View.GONE
            b.manualSection.visibility = View.VISIBLE
            b.modeToggle.check(R.id.modeManual)
        }
        savedInstanceState?.let { state ->
            b.webdavUrlInput.setText(state.getString("webdavUrl", ""))
            b.webdavLoginInput.setText(state.getString("webdavLogin", ""))
            b.webdavTokenInput.setText(state.getString("webdavToken", ""))
            b.encKeyInput.setText(state.getString("encKey", ""))
            b.hmacKeyInput.setText(state.getString("hmacKey", ""))
        }

        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager
        if (clipboard != null) {
            b.encKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, b.encKeyInput) }
            b.hmacKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, b.hmacKeyInput) }
        }

        b.listenAddrInput.setText(DEFAULT_LISTEN_ADDR)

        setupListeners()

        val savedUri = getPrefs().getString(PREF_URI, null)
        if (savedUri != null) {
            fileUri = android.net.Uri.parse(savedUri)
            fileUri?.let { refreshFileInfo(it) }
        }

        b.passwordInput.setText(getPrefs().getString(PREF_PASSWORD, ""))

        b.versionText.text = getString(R.string.version_prefix) +
            (try {
                packageManager.getPackageInfo(packageName, 0).versionName
            } catch (e: PackageManager.NameNotFoundException) { "?" })

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
        outState.putBoolean("advancedExpanded", b.advancedContent.visibility == View.VISIBLE)
        outState.putString("webdavUrl", b.webdavUrlInput.text?.toString())
        outState.putString("webdavLogin", b.webdavLoginInput.text?.toString())
        outState.putString("webdavToken", b.webdavTokenInput.text?.toString())
        outState.putString("encKey", b.encKeyInput.text?.toString())
        outState.putString("hmacKey", b.hmacKeyInput.text?.toString())
    }

    private fun setupListeners() {
        b.modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            b.fileSection.visibility = if (isManualMode) View.GONE else View.VISIBLE
            b.manualSection.visibility = if (isManualMode) View.VISIBLE else View.GONE
        }

        b.configChip.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        b.configChip.setOnCloseIconClickListener {
            fileUri = null
            b.configChip.text = getString(R.string.select_file)
            b.configChip.setCloseIconVisible(false)
        }

        b.contentScroll.viewTreeObserver.addOnScrollChangedListener {
            val sv = b.contentScroll
            autoScrollLogs = sv.getChildAt(0).bottom - (sv.height + sv.scrollY) <= 0
        }

        b.advancedHeader.setOnClickListener {
            val expanded = b.advancedContent.visibility == View.VISIBLE
            TransitionManager.beginDelayedTransition(b.advancedHeader)
            b.advancedContent.visibility = if (expanded) View.GONE else View.VISIBLE
            b.advancedArrow.text = if (expanded) getString(R.string.arrow_collapsed) else getString(R.string.arrow_expanded)
        }

        b.copyLogsBtn.setOnClickListener {
            val text = b.logText.text?.toString() ?: return@setOnClickListener
            val clip = android.content.ClipData.newPlainText("flowdav logs", text)
            (getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager)
                ?.setPrimaryClip(clip)
            Snackbar.make(b.root, R.string.logs_copied, Snackbar.LENGTH_SHORT).show()
        }

        b.actionButton.setOnClickListener {
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

        val listenAddr = b.listenAddrInput.text?.toString()?.ifBlank { DEFAULT_LISTEN_ADDR }
            ?: DEFAULT_LISTEN_ADDR
        try {
            val addr = InetSocketAddress.createUnresolved(
                listenAddr.substringBeforeLast(":"),
                listenAddr.substringAfterLast(":").toInt()
            )
            if (addr.port < 1 || addr.port > 65535) throw NumberFormatException()
        } catch (_: Exception) {
            b.listenAddrLayout.error = getString(R.string.error_invalid_listen)
            hasError = true
        }

        val socks5User = b.socks5UserInput.text?.toString()?.ifBlank { null }
        val socks5Pass = b.socks5PassInput.text?.toString()?.ifBlank { null }
        if ((socks5User != null) != (socks5Pass != null)) {
            b.socks5UserLayout.error = getString(R.string.socks5_partial_error)
            b.socks5PassLayout.error = getString(R.string.socks5_partial_error)
            hasError = true
        }

        var manualFields: ProxyManager.ManualFields? = null
        if (isManualMode) {
            val url = b.webdavUrlInput.text?.toString()?.ifBlank { null }
            val login = b.webdavLoginInput.text?.toString()?.ifBlank { null }
            val token = b.webdavTokenInput.text?.toString()?.ifBlank { null }
            val encKey = b.encKeyInput.text?.toString()?.ifBlank { null }
            val hmacKey = b.hmacKeyInput.text?.toString()?.ifBlank { null }
            if (url == null) { b.webdavUrlLayout.error = getString(R.string.error_required); hasError = true }
            if (login == null) { b.webdavLoginLayout.error = getString(R.string.error_required); hasError = true }
            if (token == null) { b.webdavTokenLayout.error = getString(R.string.error_required); hasError = true }
            if (encKey == null) { b.encKeyLayout.error = getString(R.string.error_required); hasError = true }
            if (hmacKey == null) { b.hmacKeyLayout.error = getString(R.string.error_required); hasError = true }
            if (!hasError) manualFields = ProxyManager.ManualFields(url!!, login!!, token!!, encKey!!, hmacKey!!)
        } else if (fileUri == null) {
            Snackbar.make(b.root, R.string.select_file_first, Snackbar.LENGTH_LONG).show()
            hasError = true
        }

        if (hasError) return

        val password = if (isEncrypted) {
            val pw = b.passwordInput.text?.toString() ?: ""
            getPrefs().edit().putString(PREF_PASSWORD, pw).apply()
            pw
        } else ""

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
                try { contentResolver.openInputStream(uri)?.use { it.read() } } catch (_: Exception) { null }
            }
            showFileChip(name)
            if (firstByte == null) {
                Snackbar.make(b.root, R.string.config_read_failed, Snackbar.LENGTH_LONG).show()
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
                b.statusDot.visibility = View.VISIBLE
                b.connectingProgress.visibility = View.GONE
                b.dashboard.visibility = View.GONE
                b.logSection.visibility = View.GONE
                b.actionButton.text = getString(R.string.start_proxy)
                b.actionButton.contentDescription = getString(R.string.start_proxy)
                b.actionButton.isEnabled = true
                setButtonTint(false)
                setInputsEnabled(true)
                proxyServiceStarted = false
            }

            ProxyState.Status.CONNECTING -> {
                val color = getColorFromAttr(com.google.android.material.R.attr.colorTertiary)
                setStatusUi(color, color, getString(R.string.status_connecting))
                b.statusDot.visibility = View.GONE
                b.connectingProgress.visibility = View.VISIBLE
                b.actionButton.isEnabled = false
                setInputsEnabled(false)
            }

            ProxyState.Status.RUNNING -> {
                val color = getColorFromAttr(androidx.appcompat.R.attr.colorPrimary)
                setStatusUi(color, color, getString(R.string.status_running))
                b.statusDot.visibility = View.VISIBLE
                b.connectingProgress.visibility = View.GONE
                b.actionButton.text = getString(R.string.stop_proxy)
                b.actionButton.contentDescription = getString(R.string.stop_proxy)
                b.actionButton.isEnabled = true
                setButtonTint(true)
                setInputsEnabled(false)
                b.dashboard.visibility = View.VISIBLE
                b.addrValue.text = state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }
                b.sessionsValue.text = state.sessions.toString()
                val elapsed = if (state.startedAt > 0L) System.currentTimeMillis() - state.startedAt else 0L
                b.uptimeValue.text = formatDuration(elapsed)
                val logs = state.logs
                if (logs.isNotBlank()) {
                    b.logText.text = highlightLogs(logs)
                    b.logSection.visibility = View.VISIBLE
                    if (autoScrollLogs) {
                        b.contentScroll.post { b.contentScroll.fullScroll(android.view.View.FOCUS_DOWN) }
                    }
                }
                b.addrValue.contentDescription = "${getString(R.string.address_label)}: ${state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }}"
                b.sessionsValue.contentDescription = "${getString(R.string.sessions_label)}: ${state.sessions}"
                b.uptimeValue.contentDescription = "${getString(R.string.uptime_label)}: ${formatDuration(elapsed)}"
                if (!proxyServiceStarted) {
                    proxyServiceStarted = true
                    ProxyService.startRunning(this)
                } else {
                    ProxyService.updateNotification(this, state.listenAddr.ifBlank { DEFAULT_LISTEN_ADDR }, formatDuration(elapsed))
                }
            }

            ProxyState.Status.ERROR -> {
                val color = getColorFromAttr(androidx.appcompat.R.attr.colorError)
                setStatusUi(color, color, state.error ?: getString(R.string.status_error))
                b.statusDot.visibility = View.VISIBLE
                b.connectingProgress.visibility = View.GONE
                b.dashboard.visibility = View.GONE
                b.actionButton.text = getString(R.string.start_proxy)
                b.actionButton.contentDescription = getString(R.string.start_proxy)
                b.actionButton.isEnabled = true
                setButtonTint(false)
                setInputsEnabled(true)
                proxyServiceStarted = false
            }
        }
    }

    private fun setStatusUi(dotColor: Int, textColor: Int, text: String) {
        (b.statusDot.background as? GradientDrawable)?.setColor(dotColor)
        b.statusText.setTextColor(textColor)
        b.statusText.text = text
        b.statusDot.contentDescription = "${getString(R.string.status_dot_desc)}: $text"
    }

    private fun getColorFromAttr(attr: Int): Int =
        MaterialColors.getColor(this, attr, 0)

    private fun setButtonTint(running: Boolean) {
        val bg = getColorFromAttr(
            if (running) androidx.appcompat.R.attr.colorPrimary
            else com.google.android.material.R.attr.colorPrimaryContainer
        )
        val fg = getColorFromAttr(
            if (running) com.google.android.material.R.attr.colorOnPrimary
            else com.google.android.material.R.attr.colorOnPrimaryContainer
        )
        b.actionButton.backgroundTintList = android.content.res.ColorStateList.valueOf(bg)
        b.actionButton.setTextColor(fg)
    }

    private fun clearInputErrors() {
        b.listenAddrLayout.error = null
        b.socks5UserLayout.error = null
        b.socks5PassLayout.error = null
        b.webdavUrlLayout.error = null
        b.webdavLoginLayout.error = null
        b.webdavTokenLayout.error = null
        b.encKeyLayout.error = null
        b.hmacKeyLayout.error = null
    }

    private fun setInputsEnabled(enabled: Boolean) {
        b.configChip.isEnabled = enabled
        b.passwordLayout.isEnabled = enabled
        b.modeToggle.isEnabled = enabled
        b.webdavUrlInput.isEnabled = enabled
        b.webdavLoginInput.isEnabled = enabled
        b.webdavTokenInput.isEnabled = enabled
        b.encKeyLayout.isEnabled = enabled
        b.hmacKeyLayout.isEnabled = enabled
        b.listenAddrInput.isEnabled = enabled
        b.socks5UserInput.isEnabled = enabled
        b.socks5PassInput.isEnabled = enabled
        b.advancedHeader.isEnabled = enabled
    }

    private fun showFileChip(name: String) {
        b.configChip.text = name
        b.configChip.setCloseIconVisible(true)
        b.configChip.contentDescription = "${getString(R.string.config_chip_desc)}: $name"
    }

    private fun pasteFromClipboard(clipboard: android.content.ClipboardManager, target: TextInputEditText) {
        val clip = clipboard.primaryClip
        if (clip != null && clip.itemCount > 0) {
            target.setText(clip.getItemAt(0).text)
        }
    }

    private fun getDisplayName(uri: android.net.Uri): String {
        return try {
            contentResolver.query(uri, null, null, null, null)?.use { cursor ->
                val nameIdx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                if (nameIdx >= 0 && cursor.moveToFirst()) {
                    cursor.getString(nameIdx) ?: "config"
                } else "config"
            } ?: "config"
        } catch (e: Exception) {
            "config"
        }
    }

    private fun updatePasswordVisibility() {
        b.passwordLayout.visibility = if (isEncrypted) View.VISIBLE else View.GONE
    }

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

    private fun getPrefs(): SharedPreferences {
        val masterKey = MasterKey.Builder(this)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        return EncryptedSharedPreferences.create(
            this,
            PREF_NAME,
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
        )
    }

    private fun highlightLogs(text: String): SpannableString {
        val ss = SpannableString(text)
        val warnColor = getColorFromAttr(com.google.android.material.R.attr.colorTertiary)
        for (line in text.lines()) {
            val start = text.indexOf(line)
            if (start < 0) continue
            if ("Warning" in line || "warning" in line) {
                ss.setSpan(ForegroundColorSpan(warnColor), start, start + line.length, 0)
            }
        }
        return ss
    }

    private fun formatDuration(ms: Long): String {
        val sec = ms / 1000
        val min = sec / 60
        val hour = min / 60
        return when {
            hour > 0 -> "${hour}h ${min % 60}m"
            min > 0 -> "${min}m ${sec % 60}s"
            else -> "${sec}s"
        }
    }

    companion object {
        private const val TAG = "MainActivity"
        private const val PREF_NAME = "flowdav"
        private const val PREF_PASSWORD = "password"
        private const val PREF_URI = "config_uri"
        private const val DEFAULT_LISTEN_ADDR = "127.0.0.1:1080"
    }
}
