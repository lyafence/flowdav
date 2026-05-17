package com.flowdav.app

import android.Manifest
import android.animation.ArgbEvaluator
import android.animation.ValueAnimator
import android.content.pm.PackageManager
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
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
    private var startJob: Job? = null
    private var startTime: Long = 0L

    private var pulseAnimator: ValueAnimator? = null

    private lateinit var statusDot: View
    private lateinit var statusText: android.widget.TextView
    private lateinit var modeToggle: MaterialButtonToggleGroup
    private lateinit var fileSection: MaterialCardView
    private lateinit var manualSection: MaterialCardView
    private lateinit var selectFileButton: MaterialButton
    private lateinit var configChip: Chip
    private lateinit var passwordInput: TextInputEditText
    private lateinit var webdavUrlInput: TextInputEditText
    private lateinit var webdavLoginInput: TextInputEditText
    private lateinit var webdavTokenInput: TextInputEditText
    private lateinit var encKeyInput: TextInputEditText
    private lateinit var hmacKeyInput: TextInputEditText
    private lateinit var listenAddrInput: TextInputEditText
    private lateinit var actionButton: MaterialButton
    private lateinit var statsText: android.widget.TextView
    private lateinit var advancedHeader: View
    private lateinit var advancedContent: MaterialCardView
    private lateinit var advancedArrow: android.widget.TextView

    private val filePicker = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) {
            fileUri = uri
            showFileChip(uri.lastPathSegment ?: "config")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        @Suppress("DEPRECATION")
        DynamicColors.applyIfAvailable(this)

        setContentView(R.layout.activity_main)

        statusDot = findViewById(R.id.statusDot)
        statusText = findViewById(R.id.statusText)
        modeToggle = findViewById(R.id.modeToggle)
        fileSection = findViewById(R.id.fileSection)
        manualSection = findViewById(R.id.manualSection)
        selectFileButton = findViewById(R.id.selectFileButton)
        configChip = findViewById(R.id.configChip)
        passwordInput = findViewById(R.id.passwordInput)
        webdavUrlInput = findViewById(R.id.webdavUrlInput)
        webdavLoginInput = findViewById(R.id.webdavLoginInput)
        webdavTokenInput = findViewById(R.id.webdavTokenInput)
        encKeyInput = findViewById(R.id.encKeyInput)
        hmacKeyInput = findViewById(R.id.hmacKeyInput)
        listenAddrInput = findViewById(R.id.listenAddrInput)
        actionButton = findViewById(R.id.actionButton)
        statsText = findViewById(R.id.statsText)
        advancedHeader = findViewById(R.id.advancedHeader)
        advancedContent = findViewById(R.id.advancedContent)
        advancedArrow = findViewById(R.id.advancedArrow)

        setupListeners()

        lifecycleScope.launch {
            while (isActive) {
                updateUi()
                delay(1000)
            }
        }
    }

    override fun onDestroy() {
        pulseAnimator?.cancel()
        super.onDestroy()
    }

    private fun setupListeners() {
        modeToggle.addOnButtonCheckedListener { _, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            fileSection.visibility = if (isManualMode) View.GONE else View.VISIBLE
            manualSection.visibility = if (isManualMode) View.VISIBLE else View.GONE
        }

        selectFileButton.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        configChip.setOnCloseIconClickListener {
            fileUri = null
            configChip.visibility = View.GONE
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
        startJob = lifecycleScope.launch {
            val listenAddr = listenAddrInput.text?.toString()?.ifBlank { "0.0.0.0:1080" } ?: "0.0.0.0:1080"

            statusText.text = getString(R.string.status_connecting)
            actionButton.isEnabled = false
            setStatusState(StatusState.CONNECTING)

            val error = withContext(Dispatchers.IO) {
                try {
                    if (isManualMode) {
                        val url = webdavUrlInput.text?.toString()?.ifBlank { null }
                        val login = webdavLoginInput.text?.toString()?.ifBlank { null }
                        val token = webdavTokenInput.text?.toString()?.ifBlank { null }
                        val encKey = encKeyInput.text?.toString()?.ifBlank { null }
                        val hmacKey = hmacKeyInput.text?.toString()?.ifBlank { null }
                        if (url == null || login == null || token == null || encKey == null || hmacKey == null) {
                            throw IllegalArgumentException(getString(R.string.fill_all_fields))
                        }
                        Flowdavmobile.startProxyManual(url, login, token, encKey, hmacKey, listenAddr)
                    } else {
                        val uri = fileUri
                        if (uri == null) {
                            throw IllegalArgumentException(getString(R.string.select_file_first))
                        }
                        val password = passwordInput.text?.toString() ?: ""
                        val data = ConfigHelper.readContent(this@MainActivity, uri).getOrThrow()
                        fileUri = null
                        configChip.visibility = View.GONE
                        Flowdavmobile.startProxyFromData(data, password, listenAddr)
                    }
                    null as String?
                } catch (e: Exception) {
                    e.message ?: "unknown error"
                }
            }

            if (!isActive) return@launch

            if (error != null) {
                setStatusState(StatusState.ERROR)
                statusText.text = getString(R.string.status_error)
                actionButton.isEnabled = true
                actionButton.text = getString(R.string.start_proxy)
                Snackbar.make(findViewById(android.R.id.content), getString(R.string.start_failed, error), Snackbar.LENGTH_LONG).show()
                return@launch
            }

            ProxyService.startRunning(this@MainActivity, listenAddr)
            delay(1500)

            if (!isActive) return@launch

            if (isRunning()) {
                startTime = System.currentTimeMillis()
                updateUi()
            } else {
                val err = Flowdavmobile.stopAndError()
                setStatusState(StatusState.ERROR)
                statusText.text = getString(R.string.status_error)
                actionButton.isEnabled = true
                actionButton.text = getString(R.string.start_proxy)
                if (err.isNotEmpty()) {
                    Snackbar.make(findViewById(android.R.id.content), getString(R.string.start_failed, err), Snackbar.LENGTH_LONG).show()
                }
            }
        }
    }

    private fun stopProxy() {
        startJob?.cancel()
        startJob = null
        ProxyService.stopAction(this)
        actionButton.text = getString(R.string.start_proxy)
        actionButton.isEnabled = true
    }

    // ── UI update loop ──────────────────────────────────

    private fun updateUi() {
        val status = Flowdavmobile.getStatus()
        val running = status?.running == true

        if (running) {
            setStatusState(StatusState.RUNNING)
            statusText.text = getString(R.string.status_running)
            actionButton.text = getString(R.string.stop_proxy)
            actionButton.isEnabled = true

            val sessions = status.activeSessions
            val uptime = if (startTime > 0) {
                formatDuration(System.currentTimeMillis() - startTime)
            } else ""
            statsText.visibility = View.VISIBLE
            statsText.text = getString(R.string.status_sessions, sessions, uptime)
        } else {
            setStatusState(StatusState.STOPPED)
            statusText.text = getString(R.string.status_stopped)
            statsText.visibility = View.GONE
            actionButton.text = getString(R.string.start_proxy)
            actionButton.isEnabled = true
        }
    }

    private fun showFileChip(name: String) {
        configChip.text = name
        configChip.visibility = View.VISIBLE
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
}
