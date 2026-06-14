package com.flowdav.app

import android.animation.ArgbEvaluator
import android.animation.ValueAnimator
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.OpenableColumns
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.text.SpannableString
import android.text.style.ForegroundColorSpan
import android.util.Log
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.flowdav.app.databinding.ActivityMainBinding
import com.flowdav.app.flowdavmobile.Flowdavmobile
import com.google.android.material.color.DynamicColors
import com.google.android.material.color.MaterialColors
import com.google.android.material.snackbar.Snackbar
import com.google.android.material.textfield.TextInputEditText
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : AppCompatActivity() {

    private val vm: MainViewModel by viewModels()
    private var _binding: ActivityMainBinding? = null
    private val b get() = _binding!!

    private var fileUri: android.net.Uri? = null
    private var startJob: Job? = null
    private var startTime: Long = 0L

    private var pulseAnimator: ValueAnimator? = null

    private val notifPermLauncher = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (!granted) {
            window?.decorView?.rootView?.let { root ->
                Snackbar.make(root, getString(R.string.notif_permission_denied), Snackbar.LENGTH_LONG).show()
            }
        }
    }

    private val filePicker = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) {
            fileUri = uri
            try {
                contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
            } catch (_: Exception) { }
            getPrefs().edit().putString(PREF_URI, uri.toString()).apply()
            showFileChip(getDisplayName(uri))
            detectEncrypted(uri)
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

    private fun detectEncrypted(uri: android.net.Uri) {
        try {
            val firstByte = contentResolver.openInputStream(uri)?.use { it.read() } ?: -1
            vm.isEncrypted = firstByte != '{'.code
        } catch (e: Exception) {
            vm.isEncrypted = true
        }
        updatePasswordVisibility()
    }

    private fun updatePasswordVisibility() {
        b.passwordLayout.visibility = if (vm.isEncrypted) View.VISIBLE else View.GONE
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        DynamicColors.applyToActivityIfAvailable(this)

        _binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(b.root)

        if (Build.VERSION.SDK_INT >= 33) {
            if (shouldShowRequestPermissionRationale(android.Manifest.permission.POST_NOTIFICATIONS)) {
                showNotifRationale()
            } else {
                notifPermLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
            }
        }

        b.configChip.text = getString(R.string.select_file)

        savedInstanceState?.let { state ->
            vm.isManualMode = state.getBoolean("isManualMode", false)
            vm.isEncrypted = state.getBoolean("isEncrypted", true)
            if (state.getBoolean("advancedExpanded", false)) {
                b.advancedContent.visibility = View.VISIBLE
                b.advancedArrow.text = getString(R.string.arrow_expanded)
            }
        }

        if (vm.isManualMode) {
            b.fileSection.visibility = View.GONE
            b.manualSection.visibility = View.VISIBLE
            b.modeToggle.check(R.id.modeManual)
        }

        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager
        if (clipboard != null) {
            b.encKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, b.encKeyInput) }
            b.hmacKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, b.hmacKeyInput) }
        }

        b.listenAddrInput.setText(DEFAULT_LISTEN_ADDR)

        setupListeners()

        if (intent?.action == ProxyService.ACTION_STOP) {
            stopProxy()
        }

        val savedUri = getPrefs().getString(PREF_URI, null)
        if (savedUri != null) {
            val uri = android.net.Uri.parse(savedUri)
            fileUri = uri
            showFileChip(getDisplayName(uri))
            detectEncrypted(uri)
        }

        b.passwordInput.setText(getPrefs().getString(PREF_PASSWORD, ""))

        b.versionText.text = getString(R.string.version_prefix) + (try { packageManager.getPackageInfo(packageName, 0).versionName } catch (e: PackageManager.NameNotFoundException) { "?" })

        lifecycleScope.launch {
            while (isActive) {
                updateUi()
                delay(1000)
            }
        }
    }

    private fun showNotifRationale() {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.notif_permission_title))
            .setMessage(getString(R.string.notif_permission_rationale))
            .setCancelable(false)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                notifPermLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
            }
            .setNegativeButton(android.R.string.cancel) { _, _ ->
                window?.decorView?.rootView?.let { root ->
                    Snackbar.make(root, getString(R.string.notif_permission_denied), Snackbar.LENGTH_LONG).show()
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

    override fun onDestroy() {
        pulseAnimator?.cancel()
        startJob?.cancel()
        _binding = null
        super.onDestroy()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        if (intent.action == ProxyService.ACTION_STOP) {
            stopProxy()
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putBoolean("isManualMode", vm.isManualMode)
        outState.putBoolean("isEncrypted", vm.isEncrypted)
        outState.putBoolean("advancedExpanded", b.advancedContent.visibility == View.VISIBLE)
    }

    private fun setupListeners() {
        b.modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            vm.isManualMode = checkedId == R.id.modeManual
            b.fileSection.visibility = if (vm.isManualMode) View.GONE else View.VISIBLE
            b.manualSection.visibility = if (vm.isManualMode) View.VISIBLE else View.GONE
        }

        b.configChip.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        b.configChip.setOnCloseIconClickListener {
            fileUri = null
            b.configChip.text = getString(R.string.select_file)
            b.configChip.setCloseIconVisible(false)
        }

        b.advancedHeader.setOnClickListener {
            val expanded = b.advancedContent.visibility == View.VISIBLE
            b.advancedContent.visibility = if (expanded) View.GONE else View.VISIBLE
            b.advancedArrow.text = if (expanded) getString(R.string.arrow_collapsed) else getString(R.string.arrow_expanded)
        }

        b.copyLogsBtn.setOnClickListener {
            val text = b.logText.text?.toString() ?: return@setOnClickListener
            val clip = android.content.ClipData.newPlainText("flowdav logs", text)
            (getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager)
                ?.setPrimaryClip(clip)
            Snackbar.make(b.root, getString(R.string.logs_copied), Snackbar.LENGTH_SHORT).show()
        }

        b.actionButton.setOnClickListener {
            when {
                startJob?.isActive == true -> return@setOnClickListener
                isRunning() -> stopProxy()
                else -> startProxy()
            }
        }
    }

    // ── Status ─────────────────────────────────────────

    private fun isRunning(): Boolean {
        return Flowdavmobile.getStatus()?.running ?: false
    }

    private fun setStatusState(state: StatusState) {
        pulseAnimator?.cancel()
        pulseAnimator = null

        val dot = b.statusDot.background as? GradientDrawable
        val textColor: Int
        val dotColor: Int

        when (state) {
            StatusState.STOPPED -> {
                dotColor = MaterialColors.getColor(this, com.google.android.material.R.attr.colorOnSurfaceVariant, 0)
                textColor = dotColor
            }
            StatusState.CONNECTING -> {
                dotColor = MaterialColors.getColor(this, com.google.android.material.R.attr.colorTertiary, 0)
                textColor = dotColor
                startPulse()
            }
            StatusState.RUNNING -> {
                dotColor = MaterialColors.getColor(this, androidx.appcompat.R.attr.colorPrimary, 0)
                textColor = dotColor
            }
            StatusState.ERROR -> {
                dotColor = MaterialColors.getColor(this, androidx.appcompat.R.attr.colorError, 0)
                textColor = dotColor
            }
        }

        dot?.setColor(dotColor)
        b.statusText.setTextColor(textColor)
    }

    private fun startPulse() {
        val startColor = MaterialColors.getColor(this, com.google.android.material.R.attr.colorTertiary, 0)
        val endColor = (startColor and 0x00FFFFFF) or (0x66000000.toInt())
        pulseAnimator = ValueAnimator.ofObject(ArgbEvaluator(), startColor, endColor).apply {
            duration = 800
            repeatCount = ValueAnimator.INFINITE
            repeatMode = ValueAnimator.REVERSE
            addUpdateListener { anim ->
                val color = anim.animatedValue as Int
                (b.statusDot.background as? GradientDrawable)?.setColor(color)
            }
            start()
        }
    }

    // ── Actions ─────────────────────────────────────────

    private fun startProxy() {
        vm.isConnecting = true
        currentFocus?.let { view ->
            val imm = getSystemService(Context.INPUT_METHOD_SERVICE) as? android.view.inputmethod.InputMethodManager
            imm?.hideSoftInputFromWindow(view.windowToken, 0)
            view.clearFocus()
        }
        startJob = lifecycleScope.launch(Dispatchers.Main) {
            val listenAddr = b.listenAddrInput.text?.toString()?.ifBlank { DEFAULT_LISTEN_ADDR } ?: DEFAULT_LISTEN_ADDR

            b.statusText.text = getString(R.string.status_connecting)
            b.actionButton.isEnabled = false
            setStatusState(StatusState.CONNECTING)

            val password = if (vm.isEncrypted) {
                val pw = b.passwordInput.text?.toString() ?: ""
                getPrefs().edit().putString(PREF_PASSWORD, pw).apply()
                pw
            } else ""

            val socks5User = b.socks5UserInput.text?.toString()?.ifBlank { null }
            val socks5Pass = b.socks5PassInput.text?.toString()?.ifBlank { null }

            val goResult = withContext(Dispatchers.IO) {
                try {
                    if (socks5User != null && socks5Pass != null) {
                        Flowdavmobile.setSocks5Auth(socks5User, socks5Pass)
                    }
                    if (vm.isManualMode) {
                        val url = b.webdavUrlInput.text?.toString()?.ifBlank { null }
                        val login = b.webdavLoginInput.text?.toString()?.ifBlank { null }
                        val token = b.webdavTokenInput.text?.toString()?.ifBlank { null }
                        val encKey = b.encKeyInput.text?.toString()?.ifBlank { null }
                        val hmacKey = b.hmacKeyInput.text?.toString()?.ifBlank { null }
                        if (url == null || login == null || token == null || encKey == null || hmacKey == null) {
                            throw IllegalArgumentException(getString(R.string.fill_all_fields))
                        }
                        Flowdavmobile.startProxyManual(url, login, token, encKey, hmacKey, listenAddr)
                    } else {
                        val uri = fileUri ?: throw IllegalStateException(getString(R.string.select_file_first))
                        val data = ConfigHelper.readContent(this@MainActivity, uri).getOrThrow()
                        Flowdavmobile.startProxyFromData(data, password, listenAddr)
                    }
                    null as String?
                } catch (e: Exception) {
                    e.message ?: "unknown error"
                }
            }

            if (!isActive) return@launch

            if (goResult != null) {
                Log.e(TAG, "startProxy failed: $goResult")
                setStatusState(StatusState.ERROR)
                b.statusText.text = getString(R.string.status_error)
                setInputsEnabled(true)
                b.actionButton.isEnabled = true
                b.actionButton.text = getString(R.string.start_proxy)
                vm.isConnecting = false
                setButtonTint(false)
                Snackbar.make(b.root, getString(R.string.start_failed, goResult), Snackbar.LENGTH_LONG).show()
                return@launch
            }

            ProxyService.startRunning(this@MainActivity)

            if (!isActive) return@launch

            if (isRunning()) {
                startTime = System.currentTimeMillis()
                setInputsEnabled(false)
                updateUi()
            } else {
                val err = Flowdavmobile.stopAndError()
                Log.e(TAG, "proxy stopped early: $err")
                setStatusState(StatusState.ERROR)
                b.statusText.text = getString(R.string.status_error)
                b.actionButton.isEnabled = true
                b.actionButton.text = getString(R.string.start_proxy)
                vm.isConnecting = false
                setButtonTint(false)
                if (err.isNotEmpty()) {
                    Snackbar.make(b.root, getString(R.string.start_failed, err), Snackbar.LENGTH_LONG).show()
                }
            }
            vm.isConnecting = false
        }
    }

    private fun setButtonTint(running: Boolean) {
        val bg = com.google.android.material.color.MaterialColors.getColor(this,
            if (running) androidx.appcompat.R.attr.colorPrimary
            else com.google.android.material.R.attr.colorPrimaryContainer, 0)
        val fg = com.google.android.material.color.MaterialColors.getColor(this,
            if (running) com.google.android.material.R.attr.colorOnPrimary
            else com.google.android.material.R.attr.colorOnPrimaryContainer, 0)
        b.actionButton.backgroundTintList = android.content.res.ColorStateList.valueOf(bg)
        b.actionButton.setTextColor(fg)
    }

    private fun stopProxy() {
        startJob?.cancel()
        startJob = null
        vm.isConnecting = false
        Flowdavmobile.stopProxy()
        stopService(Intent(this, ProxyService::class.java))
        setInputsEnabled(true)
        b.actionButton.text = getString(R.string.start_proxy)
        b.actionButton.isEnabled = true
        setButtonTint(false)
    }

    // ── UI update loop ──────────────────────────────────

    private fun updateUi() {
        val status = Flowdavmobile.getStatus()
        val running = status?.running == true

        if (running) {
            vm.isConnecting = false
            setStatusState(StatusState.RUNNING)
            b.statusText.text = getString(R.string.status_running)
            b.actionButton.text = getString(R.string.stop_proxy)
            b.actionButton.isEnabled = true
            setButtonTint(true)

            val addr = status?.listenAddr?.ifBlank { DEFAULT_LISTEN_ADDR }
            b.addrValue.text = addr
            b.sessionsValue.text = status.activeSessions.toString()
            val uptime = if (startTime > 0) {
                formatDuration(System.currentTimeMillis() - startTime)
            } else "0s"
            b.uptimeValue.text = uptime
            b.dashboard.visibility = View.VISIBLE

            val logs = status?.logs
            if (!logs.isNullOrBlank()) {
                b.logText.text = highlightLogs(logs)
                b.logSection.visibility = View.VISIBLE
                b.contentScroll.post { b.contentScroll.fullScroll(android.view.View.FOCUS_DOWN) }
            }
        } else {
            val err = status?.error
            if (err?.isNotEmpty() == true) {
                setStatusState(StatusState.ERROR)
                b.statusText.text = err
            } else if (!vm.isConnecting) {
                setStatusState(StatusState.STOPPED)
                b.statusText.text = getString(R.string.status_stopped)
            }
            if (!vm.isConnecting) {
                b.dashboard.visibility = View.GONE
                b.logSection.visibility = View.GONE
                b.actionButton.text = getString(R.string.start_proxy)
                b.actionButton.isEnabled = true
                setButtonTint(false)
            }
        }
    }

    private fun showFileChip(name: String) {
        b.configChip.text = name
        b.configChip.setCloseIconVisible(true)
    }

    private fun pasteFromClipboard(clipboard: android.content.ClipboardManager, target: TextInputEditText) {
        val clip = clipboard.primaryClip
        if (clip != null && clip.itemCount > 0) {
            target.setText(clip.getItemAt(0).text)
        }
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

    private fun highlightLogs(text: String): SpannableString {
        val ss = SpannableString(text)
        val warnColor = 0xFFB0A000.toInt()
        for (line in text.lines()) {
            val start = text.indexOf(line)
            if (start < 0) continue
            if ("Warning" in line || "warning" in line) {
                ss.setSpan(ForegroundColorSpan(warnColor), start, start + line.length, 0)
            }
        }
        return ss
    }

    enum class StatusState {
        STOPPED, CONNECTING, RUNNING, ERROR
    }

    companion object {
        private const val TAG = "flowdav"
        private const val PREF_NAME = "flowdav"
        private const val PREF_PASSWORD = "password"
        private const val PREF_URI = "config_uri"
        private const val DEFAULT_LISTEN_ADDR = "127.0.0.1:1080"
    }
}
