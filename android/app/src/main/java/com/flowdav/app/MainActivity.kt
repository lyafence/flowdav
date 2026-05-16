package com.flowdav.app

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.flowdav.app.flowdavmobile.Flowdavmobile
import com.google.android.material.button.MaterialButton
import com.google.android.material.button.MaterialButtonToggleGroup
import com.google.android.material.textfield.TextInputEditText
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.File

class MainActivity : AppCompatActivity() {

    private var configPath: String? = null
    private var isManualMode = false

    private lateinit var modeToggle: MaterialButtonToggleGroup
    private lateinit var fileSection: android.widget.LinearLayout
    private lateinit var manualSection: android.widget.LinearLayout
    private lateinit var configLabel: android.widget.TextView
    private lateinit var passwordInput: TextInputEditText
    private lateinit var webdavUrlInput: TextInputEditText
    private lateinit var webdavLoginInput: TextInputEditText
    private lateinit var webdavTokenInput: TextInputEditText
    private lateinit var encKeyInput: TextInputEditText
    private lateinit var hmacKeyInput: TextInputEditText
    private lateinit var listenAddrInput: TextInputEditText
    private lateinit var statusText: android.widget.TextView
    private lateinit var actionButton: MaterialButton

    private val filePicker = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) {
            val result = ConfigHelper.copyToCache(this, uri)
            result.onSuccess { path ->
                configPath = path
                configLabel.text = File(path).name
            }.onFailure { e ->
                Toast.makeText(this, "Failed to read config: ${e.message}", Toast.LENGTH_LONG).show()
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        modeToggle = findViewById(R.id.modeToggle)
        fileSection = findViewById(R.id.fileSection)
        manualSection = findViewById(R.id.manualSection)
        configLabel = findViewById(R.id.configLabel)
        passwordInput = findViewById(R.id.passwordInput)
        webdavUrlInput = findViewById(R.id.webdavUrlInput)
        webdavLoginInput = findViewById(R.id.webdavLoginInput)
        webdavTokenInput = findViewById(R.id.webdavTokenInput)
        encKeyInput = findViewById(R.id.encKeyInput)
        hmacKeyInput = findViewById(R.id.hmacKeyInput)
        listenAddrInput = findViewById(R.id.listenAddrInput)
        statusText = findViewById(R.id.statusText)
        actionButton = findViewById(R.id.actionButton)

        modeToggle.addOnButtonCheckedListener { group, checkedId, isChecked ->
            if (!isChecked) return@addOnButtonCheckedListener
            isManualMode = checkedId == R.id.modeManual
            fileSection.visibility = if (isManualMode) android.view.View.GONE else android.view.View.VISIBLE
            manualSection.visibility = if (isManualMode) android.view.View.VISIBLE else android.view.View.GONE
        }

        configLabel.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }

        actionButton.setOnClickListener {
            if (isRunning()) {
                stopProxy()
            } else {
                startProxy()
            }
        }

        requestNotificationPermissionIfNeeded()

        lifecycleScope.launch {
            while (true) {
                updateUi()
                delay(1000)
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
    }

    private fun isRunning(): Boolean {
        return Flowdavmobile.getStatus()?.running ?: false
    }

    private fun startProxy() {
        val listenAddr = listenAddrInput.text?.toString()?.ifBlank { "0.0.0.0:1080" } ?: "0.0.0.0:1080"

        val path = if (isManualMode) {
            val url = webdavUrlInput.text?.toString()?.ifBlank { null }
            val login = webdavLoginInput.text?.toString()?.ifBlank { null }
            val token = webdavTokenInput.text?.toString()?.ifBlank { null }
            val encKey = encKeyInput.text?.toString()?.ifBlank { null }
            val hmacKey = hmacKeyInput.text?.toString()?.ifBlank { null }

            if (url == null || login == null || token == null || encKey == null || hmacKey == null) {
                Toast.makeText(this, R.string.fill_all_fields, Toast.LENGTH_SHORT).show()
                return
            }
            ConfigHelper.writeManualConfig(this, url, login, token, encKey, hmacKey)
        } else {
            val p = configPath
            if (p == null) {
                Toast.makeText(this, R.string.select_file_first, Toast.LENGTH_SHORT).show()
                return
            }
            p
        }

        val password = if (!isManualMode) passwordInput.text?.toString() ?: "" else ""

        actionButton.isEnabled = false
        statusText.setText(R.string.connecting)
        ProxyService.startAction(this, path, password, listenAddr)

        lifecycleScope.launch {
            delay(1500)
            if (isRunning()) {
                updateUi()
            } else {
                actionButton.isEnabled = true
                statusText.setText(R.string.status_stopped)
                val err = Flowdavmobile.stopAndError()
                if (err.isNotEmpty()) {
                    Toast.makeText(this@MainActivity, "Error: $err", Toast.LENGTH_LONG).show()
                }
            }
        }
    }

    private fun stopProxy() {
        ProxyService.stopAction(this)
        statusText.setText(R.string.status_stopped)
        actionButton.text = getString(R.string.start_proxy)
        actionButton.isEnabled = true
    }

    private fun updateUi() {
        val status = Flowdavmobile.getStatus()
        if (status?.running == true) {
            statusText.text = getString(R.string.status_running, status.listenAddr)
            actionButton.text = getString(R.string.stop_proxy)
            actionButton.isEnabled = true
        } else {
            statusText.setText(R.string.status_stopped)
            actionButton.text = getString(R.string.start_proxy)
            actionButton.isEnabled = true
        }
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED
            ) {
                requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 0)
            }
        }
    }
}
