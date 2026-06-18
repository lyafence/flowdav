package com.flowdav.app

import android.content.Context
import android.net.Uri

class ContentResolverConfigReader(
    private val context: Context
) : ConfigReader {
    override fun read(uri: Uri): Result<ByteArray> = runCatching {
        context.contentResolver.openInputStream(uri)?.use { input ->
            input.readBytes()
        } ?: throw IllegalStateException("Cannot open selected file")
    }
}
