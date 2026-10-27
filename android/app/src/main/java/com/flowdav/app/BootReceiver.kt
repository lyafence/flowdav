package com.flowdav.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

/**
 * Opt-in autostart after device reboot (PREF_BOOT_AUTOSTART). Requires a
 * previously saved configuration; the service watchdog takes over if the
 * engine cannot start immediately (network not yet up).
 */
class BootReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        // PrefsStore init (Keystore + encrypted file) is too slow for the
        // receiver budget on a cold Keystore: go work off-main.
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                val prefs = PrefsStore.get(context)
                if (prefs.getBoolean(PrefsStore.PREF_BOOT_AUTOSTART, false) &&
                    LastRunStore.load(context) != null
                ) {
                    try {
                        context.startForegroundService(
                            Intent(context, ProxyService::class.java)
                                .apply { action = ProxyService.ACTION_START }
                        )
                    } catch (e: IllegalStateException) {
                        // Some OEM builds restrict FGS launches from
                        // BOOT_COMPLETED; the user opens the app manually.
                        Log.w(TAG, "boot autostart blocked by the system", e)
                    }
                }
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        private const val TAG = "BootReceiver"
    }
}
