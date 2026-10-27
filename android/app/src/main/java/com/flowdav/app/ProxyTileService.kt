package com.flowdav.app

import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import com.flowdav.app.flowdavmobile.Flowdavmobile
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Quick Settings tile: one tap starts or stops the proxy.
 *
 * Android 14/15 regression (issuetracker 377528724): a tile tap is not
 * exempted from background foreground-service launch restrictions, so a
 * direct startForegroundService() from onClick can be rejected. In that case
 * we fall back to opening the app; the user starts the proxy from the UI.
 * Stop always works.
 */
class ProxyTileService : TileService() {

    // getStatus() takes the bridge mutex, which a stop may hold for the
    // drain: bridge calls stay off the main thread.
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    override fun onStartListening() {
        scope.launch { refresh() }
    }

    override fun onClick() {
        scope.launch {
            val running = Flowdavmobile.getStatus()?.running == true
            withContext(Dispatchers.Main) {
                when {
                    // Stop lands asynchronously; flip first, the next
                    // onStartListening (or requestRefresh from the service)
                    // corrects it.
                    running -> {
                        setTileNow(Tile.STATE_INACTIVE)
                        stopService(Intent(this@ProxyTileService, ProxyService::class.java))
                    }

                    LastRunStore.load(this@ProxyTileService) == null -> openApp()

                    else -> startEngineService()
                }
            }
        }
    }

    private fun startEngineService() {
        val intent = Intent(this, ProxyService::class.java).apply {
            action = ProxyService.ACTION_START
        }
        try {
            startForegroundService(intent)
        } catch (e: IllegalStateException) {
            // ForegroundServiceStartNotAllowedException is an
            // IllegalStateException; referencing the API-31 class in a catch
            // clause would break older devices.
            openApp()
        }
    }

    @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
    private fun openApp() {
        val intent = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startActivityAndCollapse(
                PendingIntent.getActivity(
                    this,
                    0,
                    intent,
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
                )
            )
        } else {
            // Deprecated on API 34+ (throws there); the version guard above
            // makes this branch unreachable on those devices.
            startActivityAndCollapse(intent)
        }
    }

    /** Reads the bridge status off-main, then applies the state. */
    private suspend fun refresh() {
        val running = Flowdavmobile.getStatus()?.running == true
        setTileNow(if (running) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE)
    }

    private fun setTileNow(state: Int) {
        qsTile?.state = state
        qsTile?.updateTile()
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    companion object {
        /** Prods the tile to refresh even with the QS panel closed. */
        fun requestRefresh(context: Context) {
            TileService.requestListeningState(
                context,
                ComponentName(context, ProxyTileService::class.java)
            )
        }
    }
}
