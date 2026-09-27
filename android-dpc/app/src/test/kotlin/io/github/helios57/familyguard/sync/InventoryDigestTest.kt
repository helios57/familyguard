package io.github.helios57.familyguard.sync

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The inventory digest belongs to the device record it was sent to.
 *
 * Measured 2026-09-27 on the emulator: a phone enrolled a second time — a new device record on the
 * server — reported "inventory unchanged" after every sync, because the digest it held was the one
 * the PREVIOUS record had accepted. The new record never received an app list, and with no installed
 * apps on the server's side a pause, bedtime or a daily limit had nothing to take.
 */
class InventoryDigestTest {

    private var stored: String? = null
    private var device = "device-a"
    private val digest = InventoryDigest(read = { stored }, write = { stored = it }, deviceId = { device })

    @Test
    fun `a digest recorded for this device is the last one`() {
        digest.record("abc")
        assertEquals("abc", digest.last())
    }

    @Test
    fun `a digest recorded for another device record is not`() {
        digest.record("abc")
        device = "device-b"
        assertEquals("a new enrolment must send its inventory", "", digest.last())
    }

    @Test
    fun `a digest from before this was tracked is not trusted`() {
        stored = "abc"
        assertEquals("a bare digest says nothing about which record accepted it", "", digest.last())
    }

    @Test
    fun `without a credential nothing is the last digest`() {
        digest.record("abc")
        device = ""
        assertEquals("", digest.last())
    }
}
