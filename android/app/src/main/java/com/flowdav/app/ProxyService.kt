package com.flowdav.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.IBinder
import android.widget.Toast
import com.flowdav.app.flowdavmobile.Flowdavmobile
import androidx.core.app.NotificationCompat

class ProxyService : Service() {

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> {
                val configPath = intent.getStringExtra(EXTRA_CONFIG_PATH) ?: return START_NOT_STICKY
                val password = intent.getStringExtra(EXTRA_PASSWORD) ?: ""
                val listenAddr = intent.getStringExtra(EXTRA_LISTEN_ADDR) ?: "0.0.0.0:1080"

                startForeground(NOTIFICATION_ID, buildNotification("Starting…"))

                // Run blocking Go call on a worker thread
                Thread {
                    try {
                        Flowdavmobile.startProxy(configPath, password, listenAddr)
                        runOnUiThread { updateNotification("Running on $listenAddr") }
                    } catch (e: Exception) {
                        runOnUiThread {
                            Toast.makeText(this, "Error: ${e.message}", Toast.LENGTH_LONG).show()
                            stopSelf()
                        }
                    }
                }.start()
            }

            ACTION_STOP -> {
                Flowdavmobile.stopProxy()
                ConfigHelper.deleteCache(this)
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.proxy_notification_channel),
            NotificationManager.IMPORTANCE_LOW,
        )
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(channel)
    }

    private fun buildNotification(text: String): Notification {
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_menu_share)
            .setOngoing(true)
            .build()
    }

    private fun updateNotification(text: String) {
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIFICATION_ID, buildNotification(text))
    }

    private fun runOnUiThread(action: () -> Unit) {
        android.os.Handler(mainLooper).post(action)
    }

    companion object {
        private const val CHANNEL_ID = "proxy_status"
        private const val NOTIFICATION_ID = 1

        private const val ACTION_START = "com.flowdav.app.START"
        private const val ACTION_STOP = "com.flowdav.app.STOP"
        private const val EXTRA_CONFIG_PATH = "config_path"
        private const val EXTRA_PASSWORD = "password"
        private const val EXTRA_LISTEN_ADDR = "listen_addr"

        fun startAction(context: Context, configPath: String, password: String, listenAddr: String) {
            val intent = Intent(context, ProxyService::class.java).apply {
                action = ACTION_START
                putExtra(EXTRA_CONFIG_PATH, configPath)
                putExtra(EXTRA_PASSWORD, password)
                putExtra(EXTRA_LISTEN_ADDR, listenAddr)
            }
            context.startForegroundService(intent)
        }

        fun stopAction(context: Context) {
            val intent = Intent(context, ProxyService::class.java).apply {
                action = ACTION_STOP
            }
            context.startService(intent)
        }
    }
}
