package com.flowdav.app

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Reads the ErrorCode constants from cmd/android/bridge.go and asserts ErrorKind
 * maps each one; renaming a code on either side fails the test.
 */
class ErrorKindCodesTest {

    private val codeConstRegex = Regex("""Err\w+\s+ErrorCode\s*=\s*"([^"]+)"""")

    private fun bridgeGoFile(): File {
        var dir = File(".").absoluteFile
        repeat(6) {
            val candidate = File(dir, "cmd/android/bridge.go")
            if (candidate.isFile) return candidate
            dir = dir.parentFile
                ?: throw AssertionError("cmd/android/bridge.go not found from ${File(".").absolutePath}")
        }
        throw AssertionError("cmd/android/bridge.go not found from ${File(".").absolutePath}")
    }

    @Test
    fun everyBridgeCodeIsMapped() {
        val codes = codeConstRegex.findAll(bridgeGoFile().readText())
            .map { it.groupValues[1] }
            .toList()

        assertTrue("no ErrorCode constants parsed from bridge.go", codes.isNotEmpty())
        codes.forEach { code ->
            assertEquals(
                "bridge code '$code' is not mapped by ProxyState.ErrorKind",
                code,
                ProxyState.ErrorKind.fromCode(code).code
            )
        }
    }

    @Test
    fun unknownAndEmptyFallBack() {
        assertEquals(ProxyState.ErrorKind.NONE, ProxyState.ErrorKind.fromCode(""))
        assertEquals(ProxyState.ErrorKind.UNKNOWN, ProxyState.ErrorKind.fromCode("bogus"))
        assertEquals(ProxyState.ErrorKind.UNKNOWN, ProxyState.ErrorKind.fromCode(null))
    }
}
