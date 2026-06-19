package com.flowdav.app

import android.net.Uri

interface ConfigReader {
    fun read(uri: Uri): Result<ByteArray>
}
