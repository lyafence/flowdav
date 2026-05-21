package com.flowdav.app

import android.content.Context
import android.net.Uri

object ConfigHelper {

    fun readContent(context: Context, uri: Uri): Result<ByteArray> = runCatching {
        context.contentResolver.openInputStream(uri)?.use { input ->
            input.readBytes()
        } ?: throw IllegalStateException("Cannot open selected file")
    }
}
