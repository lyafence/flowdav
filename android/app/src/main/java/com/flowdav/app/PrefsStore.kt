@file:Suppress("DEPRECATION")

package com.flowdav.app

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

/**
 * Encrypted preferences (config URI, master password, last-run parameters).
 * androidx.security-crypto is deprecated upstream but kept deliberately
 * (Keystore-backed, `allowBackup=false`); the file-scoped suppression above
 * cannot hide warnings elsewhere.
 */
object PrefsStore {
    private const val PREFS_NAME = "flowdav"

    const val PREF_URI = "config_uri"
    const val PREF_PASSWORD = "password"
    const val PREF_PASSWORD_CONFIG = "password_config"
    const val PREF_MODE_MANUAL = "mode_manual"
    const val PREF_BOOT_AUTOSTART = "boot_autostart"
    const val PREF_BATTERY_ASKED = "battery_asked"

    @Volatile
    private var instance: SharedPreferences? = null

    fun get(context: Context): SharedPreferences {
        instance?.let { return it }
        return synchronized(this) {
            instance ?: create(context.applicationContext).also { instance = it }
        }
    }

    private fun create(context: Context): SharedPreferences {
        val masterKey = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        return EncryptedSharedPreferences.create(
            context,
            PREFS_NAME,
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
        )
    }
}
