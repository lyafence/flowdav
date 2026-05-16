package com.flowdav.app

import android.content.Context
import android.net.Uri
import java.io.File

object ConfigHelper {

    fun copyToCache(context: Context, uri: Uri): Result<String> = runCatching {
        val cacheFile = File(context.cacheDir, "flowdav_config.bin")
        context.contentResolver.openInputStream(uri)?.use { input ->
            cacheFile.outputStream().use { output ->
                input.copyTo(output)
            }
        } ?: throw IllegalStateException("Cannot open selected file")
        cacheFile.absolutePath
    }

    fun writeManualConfig(
        context: Context,
        webdavUrl: String,
        webdavLogin: String,
        webdavToken: String,
        encKey: String,
        hmacKey: String,
    ): String {
        val json = buildString {
            appendLine("{")
            appendLine("  \"storage_type\": \"webdav\",")
            appendLine("  \"webdav\": {")
            appendLine("    \"url\": \"${escape(webdavUrl)}\",")
            appendLine("    \"login\": \"${escape(webdavLogin)}\",")
            appendLine("    \"token\": \"${escape(webdavToken)}\"")
            appendLine("  },")
            appendLine("  \"enc_key\": \"${encKey}\",")
            appendLine("  \"hmac_key\": \"${hmacKey}\"")
            appendLine("}")
        }

        val cacheFile = File(context.cacheDir, "flowdav_config_manual.json")
        cacheFile.writeText(json)
        return cacheFile.absolutePath
    }

    fun deleteCache(context: Context) {
        File(context.cacheDir, "flowdav_config.bin").delete()
        File(context.cacheDir, "flowdav_config_manual.json").delete()
    }

    private fun escape(s: String): String {
        return s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n")
    }
}
