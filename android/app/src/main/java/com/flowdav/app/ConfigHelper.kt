package com.flowdav.app

import android.content.Context
import android.net.Uri
import java.io.File

object ConfigHelper {

    fun readContent(context: Context, uri: Uri): Result<ByteArray> = runCatching {
        context.contentResolver.openInputStream(uri)?.use { input ->
            input.readBytes()
        } ?: throw IllegalStateException("Cannot open selected file")
    }

    fun deleteCache(context: Context) {
        File(context.cacheDir, "flowdav_config.bin").delete()
        File(context.cacheDir, "flowdav_config_manual.json").delete()
    }
}
