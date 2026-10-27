package com.flowdav.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import com.flowdav.app.flowdavmobile.Flowdavmobile
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

class ProxyService : Service() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var watchdogJob: Job? = null

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            // onDestroy below is the single engine-shutdown path.
            stopSelf()
            return START_NOT_STICKY
        }

        val addr = intent?.getStringExtra(EXTRA_ADDR).orEmpty()
        ServiceCompat.startForeground(
            this,
            NOTIFICATION_ID,
            buildNotification(addr),
            fgsType()
        )

        if (intent?.action == ACTION_START) {
            // Started by the quick settings tile, boot receiver or a sticky
            // restart: the engine must be launched from the saved config.
            scope.launch {
                if (Flowdavmobile.getStatus()?.running != true) {
                    val last = LastRunStore.load(applicationContext)
                    if (last == null) {
                        stopSelf()
                        return@launch
                    }
                    SavedStart.applySocks5Auth(last)
                    SavedStart.start(applicationContext)
                }
            }
        }

        startWatchdog()
        // START_STICKY: after a process kill the system restarts the service
        // with a null intent; the watchdog then relaunches the engine from
        // the saved configuration, making the proxy self-healing.
        return START_STICKY
    }

    // Defensive: this callback is only delivered for foreground service types
    // with a system-enforced timeout (e.g. dataSync, capped at 6h on API 35+).
    // specialUse is indefinite, but if it ever fires, stop cleanly instead of
    // letting the system kill the process.
    override fun onTimeout(startId: Int, fgsType: Int) {
        // onDestroy fires after stopSelf() and stops the engine off-main.
        stopSelf()
    }

    override fun onDestroy() {
        stopEngineOffMainThread()
        scope.cancel()
        // Engine is (about to be) down: the QS tile must not stay ACTIVE.
        ProxyTileService.requestRefresh(this)
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    /**
     * Monitors the engine while the service is alive: if the proxy dies in the
     * background (SOCKS5 goroutine crash, backend failure), it is restarted
     * from the saved configuration with a bounded attempt budget. Without
     * this, the proxy would silently stay down until the user opens the app.
     */
    private fun startWatchdog() {
        // onStartCommand can fire repeatedly (notification updates, sticky
        // restarts); a single watchdog loop is enough.
        if (watchdogJob?.isActive == true) return
        watchdogJob = scope.launch {
            var attempts = 0
            var stableChecks = 0
            var publishedAddr: String? = null
            while (isActive) {
                delay(WATCHDOG_INTERVAL_MS)
                val status = Flowdavmobile.getStatus() ?: continue
                if (status.running == true) {
                    stableChecks++
                    if (stableChecks >= STABLE_CHECKS_TO_RESET) attempts = 0
                    // The tile/boot paths start the engine without a listen
                    // addr intent; publish the real address once known.
                    if (status.listenAddr.isNotBlank() && publishedAddr != status.listenAddr) {
                        publishedAddr = status.listenAddr
                        publishNotification(status.listenAddr)
                    }
                    continue
                }
                publishedAddr = null
                stableChecks = 0
                val kind = status.errorCode?.ifEmpty { null }
                    ?.let { ProxyState.ErrorKind.fromCode(it) }
                // Only unclassified deaths self-heal; deterministic failures
                // (config/auth/listen) waste nothing but battery on restart.
                if (kind != null && kind != ProxyState.ErrorKind.RUNTIME) {
                    stopSelf()
                    return@launch
                }
                if (LastRunStore.load(applicationContext) == null) continue
                if (attempts >= MAX_WATCHDOG_RESTARTS) {
                    Flowdavmobile.stopAndError()
                    stopSelf()
                    return@launch
                }
                attempts++
                SavedStart.start(applicationContext)
            }
        }
    }

    private fun publishNotification(addr: String) {
        (getSystemService(NOTIFICATION_SERVICE) as NotificationManager)
            .notify(NOTIFICATION_ID, buildNotification(addr))
        // The QS tile cannot observe engine state transitions on its own.
        ProxyTileService.requestRefresh(this)
    }

    /** Engine shutdown drains queued uploads (bounded) — never block main. */
    private fun stopEngineOffMainThread() {
        Thread { Flowdavmobile.stopProxy() }.start()
    }

    private fun fgsType(): Int = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
        ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
    } else {
        0
    }

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.proxy_notification_channel),
            NotificationManager.IMPORTANCE_LOW
        )
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(channel)
    }

    private fun buildNotification(addr: String): Notification {
        val openIntent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val openPending = PendingIntent.getActivity(
            this,
            0,
            openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val stopIntent = Intent(this, ProxyService::class.java).apply {
            action = ACTION_STOP
        }
        val stopPending = PendingIntent.getService(
            this,
            1,
            stopIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val text = if (addr.isBlank()) {
            getString(R.string.proxy_running)
        } else {
            getString(R.string.notification_running_fmt, addr)
        }

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_launcher_monochrome)
            .setOngoing(true)
            .setUsesChronometer(true)
            .setWhen(System.currentTimeMillis())
            .setContentIntent(openPending)
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, getString(R.string.notification_stop), stopPending)
            .build()
    }

    companion object {
        private const val CHANNEL_ID = "proxy_status"
        private const val NOTIFICATION_ID = 1
        private const val EXTRA_ADDR = "addr"
        private const val WATCHDOG_INTERVAL_MS = 30_000L
        private const val MAX_WATCHDOG_RESTARTS = 3
        private const val STABLE_CHECKS_TO_RESET = 2
        const val ACTION_START = "com.flowdav.app.START_PROXY"
        const val ACTION_STOP = "com.flowdav.app.STOP_PROXY"

        fun startRunning(context: Context, addr: String) {
            val intent = Intent(context, ProxyService::class.java).apply {
                putExtra(EXTRA_ADDR, addr)
            }
            context.startForegroundService(intent)
        }
    }
}
