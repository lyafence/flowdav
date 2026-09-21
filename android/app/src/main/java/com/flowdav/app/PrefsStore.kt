@file:Suppress("DEPRECATION")

package com.flowdav.app

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

/**
 * Encrypted preferences (config URI + master password). androidx.security-crypto is
 * deprecated upstream but kept deliberately (Keystore-backed, `allowBackup=false`);
 * the file-scoped suppression above cannot hide warnings elsewhere.
 */
object PrefsStore {
    private const val PREFS_NAME = "flowdav"

    fun get(context: Context): SharedPreferences {
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
