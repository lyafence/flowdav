package com.flowdav.app

import android.content.Context
import android.net.Uri
import java.io.ByteArrayOutputStream

class ContentResolverConfigReader(private val context: Context) : ConfigReader {
    override fun read(uri: Uri): Result<ByteArray> = runCatching {
        context.contentResolver.openInputStream(uri)?.use { input ->
            val out = ByteArrayOutputStream()
            val buf = ByteArray(DEFAULT_BUFFER_SIZE)
            var total = 0
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                total += n
                require(total <= MAX_CONFIG_BYTES) { "config file exceeds $MAX_CONFIG_BYTES bytes" }
                out.write(buf, 0, n)
            }
            out.toByteArray()
        } ?: throw IllegalStateException("contentResolver returned no stream for $uri")
    }

    companion object {
        /** Hard cap for streamed config files; MainActivity import honors it too. */
        const val MAX_CONFIG_BYTES = 1024 * 1024
    }
}
