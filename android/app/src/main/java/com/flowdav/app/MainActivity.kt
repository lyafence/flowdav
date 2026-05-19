package com.flowdav.app

import android.animation.ArgbEvaluator
import android.animation.ValueAnimator
import android.content.Context
import android.content.Intent
import android.provider.OpenableColumns
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import android.graphics.drawable.GradientDrawable
import android.os.Bundle
import android.util.Log
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.flowdav.app.flowdavmobile.Flowdavmobile
import com.google.android.material.button.MaterialButton
import com.google.android.material.button.MaterialButtonToggleGroup
import com.google.android.material.card.MaterialCardView
import com.google.android.material.color.DynamicColors
import com.google.android.material.chip.Chip
import com.google.android.material.snackbar.Snackbar
import com.google.android.material.textfield.TextInputEditText
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

class MainActivity : AppCompatActivity() {

    private var fileUri: android.net.Uri? = null
    private var isManualMode = false
    private var isConnecting = false
    private var isEncrypted = true
    private var startJob: Job? = null
    private var startTime: Long = 0L

    private var pulseAnimator: ValueAnimator? = null

    private lateinit var statusDot: View
    private lateinit var statusText: android.widget.TextView
    private lateinit var modeToggle: MaterialButtonToggleGroup
    private lateinit var fileSection: MaterialCardView
    private lateinit var manualSection: MaterialCardView
    private lateinit var passwordLayout: com.google.android.material.textfield.TextInputLayout
    private lateinit var configChip: Chip
    private lateinit var passwordInput: TextInputEditText
    private lateinit var webdavUrlInput: TextInputEditText
    private lateinit var webdavLoginInput: TextInputEditText
    private lateinit var webdavTokenInput: TextInputEditText
    private lateinit var encKeyInput: TextInputEditText
    private lateinit var encKeyLayout: com.google.android.material.textfield.TextInputLayout
    private lateinit var hmacKeyInput: TextInputEditText
    private lateinit var hmacKeyLayout: com.google.android.material.textfield.TextInputLayout
    private lateinit var socks5UserInput: TextInputEditText
    private lateinit var socks5PassInput: TextInputEditText
    private lateinit var listenAddrInput: TextInputEditText
    private lateinit var actionButton: MaterialButton
    private lateinit var statsText: android.widget.TextView
    private lateinit var advancedHeader: View
    private lateinit var advancedContent: View
    private lateinit var advancedArrow: android.widget.TextView

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
            isEncrypted = firstByte != '{'.code
        } catch (e: Exception) {
            isEncrypted = true
        }
        updatePasswordVisibility()
    }

    private fun updatePasswordVisibility() {
        passwordLayout.visibility = if (isEncrypted) View.VISIBLE else View.GONE
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        DynamicColors.applyToActivityIfAvailable(this)

        setContentView(R.layout.activity_main)

        statusDot = findViewById(R.id.statusDot)
        statusText = findViewById(R.id.statusText)
        modeToggle = findViewById(R.id.modeToggle)
        fileSection = findViewById(R.id.fileSection)
        manualSection = findViewById(R.id.manualSection)
        configChip = findViewById(R.id.configChip)
        passwordLayout = findViewById(R.id.passwordLayout)
        passwordInput = findViewById(R.id.passwordInput)
        webdavUrlInput = findViewById(R.id.webdavUrlInput)
        webdavLoginInput = findViewById(R.id.webdavLoginInput)
        webdavTokenInput = findViewById(R.id.webdavTokenInput)
        encKeyInput = findViewById(R.id.encKeyInput)
        encKeyLayout = findViewById(R.id.encKeyLayout)
        hmacKeyInput = findViewById(R.id.hmacKeyInput)
        hmacKeyLayout = findViewById(R.id.hmacKeyLayout)
        socks5UserInput = findViewById(R.id.socks5UserInput)
        socks5PassInput = findViewById(R.id.socks5PassInput)
        listenAddrInput = findViewById(R.id.listenAddrInput)
        listenAddrInput.setText(DEFAULT_LISTEN_ADDR)
        actionButton = findViewById(R.id.actionButton)
        statsText = findViewById(R.id.statsText)
        advancedHeader = findViewById(R.id.advancedHeader)
        advancedContent = findViewById(R.id.advancedContent)
        advancedArrow = findViewById(R.id.advancedArrow)

        configChip.text = getString(R.string.select_file)

        savedInstanceState?.let { state ->
            isManualMode = state.getBoolean("isManualMode", false)
            isEncrypted = state.getBoolean("isEncrypted", true)
            if (state.getBoolean("advancedExpanded", false)) {
                advancedContent.visibility = View.VISIBLE
                advancedArrow.text = "▼"
            }
            if (isManualMode) {
                fileSection.visibility = View.GONE
                manualSection.visibility = View.VISIBLE
                modeToggle.check(R.id.modeManual)
            }
        }

        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? android.content.ClipboardManager
        if (clipboard != null) {
            encKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, encKeyInput) }
            hmacKeyLayout.setEndIconOnClickListener { pasteFromClipboard(clipboard, hmacKeyInput) }
        }

        setupListeners()

        val savedUri = getPrefs().getString(PREF_URI, null)
        if (savedUri != null) {
            val uri = android.net.Uri.parse(savedUri)
            fileUri = uri
            showFileChip(getDisplayName(uri))
            detectEncrypted(uri)
        }

        passwordInput.setText(getPrefs().getString(PREF_PASSWORD, ""))

        lifecycleScope.launch {
            while (isActive) {
                updateUi()
                delay(1000)
            }
        }
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
        outState.putBoolean("isManualMode", isManualMode)
        outState.putBoolean("isEncrypted", isEncrypted)
        outState.putBoolean("advancedExpanded", advancedContent.visibility == View.VISIBLE)
    }

    private fun setupListeners() {
        modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            fileSection.visibility = if (isManualMode) View.GONE else View.VISIBLE
            manualSection.visibility = if (isManualMode) View.VISIBLE else View.GONE
        }

        configChip.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        configChip.setOnCloseIconClickListener {
            fileUri = null
            configChip.text = getString(R.string.select_file)
            configChip.setCloseIconVisible(false)
        }

        advancedHeader.setOnClickListener {
            val expanded = advancedContent.visibility == View.VISIBLE
            advancedContent.visibility = if (expanded) View.GONE else View.VISIBLE
            advancedArrow.text = if (expanded) "▶" else "▼"
        }

        actionButton.setOnClickListener {
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

        val dot = statusDot.background as? GradientDrawable
        val textColor: Int
        val dotColor: Int

        when (state) {
            StatusState.STOPPED -> {
                dotColor = getColorCompat(android.R.color.darker_gray)
                textColor = getColorCompat(android.R.color.darker_gray)
            }
            StatusState.CONNECTING -> {
                dotColor = getColorCompat(android.R.color.holo_orange_dark)
                textColor = getColorCompat(android.R.color.holo_orange_dark)
                startPulse()
            }
            StatusState.RUNNING -> {
                dotColor = 0xFF4CAF50.toInt()
                textColor = 0xFF4CAF50.toInt()
            }
            StatusState.ERROR -> {
                dotColor = getColorCompat(android.R.color.holo_red_dark)
                textColor = getColorCompat(android.R.color.holo_red_dark)
            }
        }

        dot?.setColor(dotColor)
        statusText.setTextColor(textColor)
    }

    private fun startPulse() {
        val startColor = 0xFF4CAF50.toInt()
        val endColor = 0x334CAF50.toInt()
        pulseAnimator = ValueAnimator.ofObject(ArgbEvaluator(), startColor, endColor).apply {
            duration = 800
            repeatCount = ValueAnimator.INFINITE
            repeatMode = ValueAnimator.REVERSE
            addUpdateListener { anim ->
                val color = anim.animatedValue as Int
                (statusDot.background as? GradientDrawable)?.setColor(color)
            }
            start()
        }
    }

    private fun getColorCompat(colorRes: Int): Int {
        return ContextCompat.getColor(this, colorRes)
    }

    // ── Actions ─────────────────────────────────────────

    private fun startProxy() {
        isConnecting = true
        startJob = lifecycleScope.launch(Dispatchers.Main) {
            val listenAddr = listenAddrInput.text?.toString()?.ifBlank { DEFAULT_LISTEN_ADDR } ?: DEFAULT_LISTEN_ADDR

            statusText.text = getString(R.string.status_connecting)
            actionButton.isEnabled = false
            setStatusState(StatusState.CONNECTING)

            val password = if (isEncrypted) {
                val pw = passwordInput.text?.toString() ?: ""
                getPrefs().edit().putString(PREF_PASSWORD, pw).apply()
                pw
            } else ""

            val goResult = withContext(Dispatchers.IO) {
                try {
                    if (isManualMode) {
                        val url = webdavUrlInput.text?.toString()?.ifBlank { null }
                        val login = webdavLoginInput.text?.toString()?.ifBlank { null }
                        val token = webdavTokenInput.text?.toString()?.ifBlank { null }
                        val encKey = encKeyInput.text?.toString()?.ifBlank { null }
                        val hmacKey = hmacKeyInput.text?.toString()?.ifBlank { null }
                        val socks5User = socks5UserInput.text?.toString()?.ifBlank { null }
                        val socks5Pass = socks5PassInput.text?.toString()?.ifBlank { null }
                        if (url == null || login == null || token == null || encKey == null || hmacKey == null) {
                            throw IllegalArgumentException(getString(R.string.fill_all_fields))
                        }
                        Flowdavmobile.startProxyManual(url, login, token, encKey, hmacKey, listenAddr)
                        if (socks5User != null && socks5Pass != null) {
                            Flowdavmobile.setSocks5Auth(socks5User, socks5Pass)
                        }
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
                statusText.text = getString(R.string.status_error)
                actionButton.isEnabled = true
                actionButton.text = getString(R.string.start_proxy)
                isConnecting = false
                setButtonTint(false)
                Snackbar.make(findViewById(android.R.id.content), getString(R.string.start_failed, goResult), Snackbar.LENGTH_LONG).show()
                return@launch
            }

            ProxyService.startRunning(this@MainActivity)
            delay(1500)

            if (!isActive) return@launch

            if (isRunning()) {
                startTime = System.currentTimeMillis()
                updateUi()
            } else {
                val err = Flowdavmobile.stopAndError()
                Log.e(TAG, "proxy stopped early: $err")
                setStatusState(StatusState.ERROR)
                statusText.text = getString(R.string.status_error)
                actionButton.isEnabled = true
                actionButton.text = getString(R.string.start_proxy)
                isConnecting = false
                setButtonTint(false)
                if (err.isNotEmpty()) {
                    Snackbar.make(findViewById(android.R.id.content), getString(R.string.start_failed, err), Snackbar.LENGTH_LONG).show()
                }
            }
            isConnecting = false
        }
    }

    private fun setButtonTint(running: Boolean) {
        val bg = com.google.android.material.color.MaterialColors.getColor(this,
            if (running) androidx.appcompat.R.attr.colorPrimary
            else com.google.android.material.R.attr.colorPrimaryContainer, 0)
        val fg = com.google.android.material.color.MaterialColors.getColor(this,
            if (running) com.google.android.material.R.attr.colorOnPrimary
            else com.google.android.material.R.attr.colorOnPrimaryContainer, 0)
        actionButton.backgroundTintList = android.content.res.ColorStateList.valueOf(bg)
        actionButton.setTextColor(fg)
    }

    private fun stopProxy() {
        startJob?.cancel()
        startJob = null
        isConnecting = false
        Flowdavmobile.stopProxy()
        stopService(Intent(this, ProxyService::class.java))
        actionButton.text = getString(R.string.start_proxy)
        actionButton.isEnabled = true
        setButtonTint(false)
    }

    // ── UI update loop ──────────────────────────────────

    private fun updateUi() {
        val status = Flowdavmobile.getStatus()
        val running = status?.running == true

        if (running) {
            isConnecting = false
            setStatusState(StatusState.RUNNING)
            val addr = status?.listenAddr?.ifBlank { DEFAULT_LISTEN_ADDR }
            statusText.text = getString(R.string.status_running_addr, addr)
            actionButton.text = getString(R.string.stop_proxy)
            actionButton.isEnabled = true
            setButtonTint(true)

            val sessions = status.activeSessions
            val uptime = if (startTime > 0) {
                formatDuration(System.currentTimeMillis() - startTime)
            } else ""
            statsText.visibility = View.VISIBLE
            statsText.text = getString(R.string.status_sessions, sessions, uptime)
        } else {
            val err = status?.error
            if (err?.isNotEmpty() == true) {
                setStatusState(StatusState.ERROR)
                statusText.text = err
            } else if (!isConnecting) {
                setStatusState(StatusState.STOPPED)
                statusText.text = getString(R.string.status_stopped)
            }
            if (!isConnecting) {
                statsText.visibility = View.GONE
                actionButton.text = getString(R.string.start_proxy)
                actionButton.isEnabled = true
                setButtonTint(false)
            }
        }
    }

    private fun showFileChip(name: String) {
        configChip.text = name
        configChip.setCloseIconVisible(true)
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
