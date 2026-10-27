package com.flowdav.app

import java.io.File
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Reads the storage self-test codes from cmd/android/bridge.go and asserts
 * MainActivity handles each literal; renaming on either side fails the test.
 */
class StorageCodesTest {

    private val codeConstRegex = Regex("""storageTest\w+\s*=\s*"([^"]+)"""")

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
    fun everyStorageTestCodeIsHandled() {
        val codes = codeConstRegex.findAll(bridgeGoFile().readText())
            .map { it.groupValues[1] }
            .filter { it != "ok" } // success carries no mapped failure code
            .toList()

        assertTrue("no storage test codes parsed from bridge.go", codes.isNotEmpty())

        val src = File("src/main/java/com/flowdav/app/MainActivity.kt").readText()
        codes.forEach { code ->
            assertTrue(
                "storage test code '$code' from bridge.go is not handled in MainActivity",
                src.contains("\"$code\"")
            )
        }
    }
}
