package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class SupportMessageManagerTest {

    private class Fake(var stored: String? = null, val ignoreSet: Boolean = false) : SupportMessageGateway {
        var sets = 0
        override fun get(): String? = stored
        override fun set(text: String) {
            sets++
            if (!ignoreSet) stored = text
        }
    }

    @Test
    fun `the message is set and read back`() {
        val gateway = Fake()
        val outcome = SupportMessageManager(gateway).apply("Tageslimit erreicht")
        assertNull(outcome.failure)
        assertEquals("Tageslimit erreicht", gateway.stored)
    }

    @Test
    fun `an unchanged message is not written again`() {
        val gateway = Fake(stored = "Nachtruhe bis 07:00")
        SupportMessageManager(gateway).apply("Nachtruhe bis 07:00")
        assertEquals(0, gateway.sets)
    }

    /** setShortSupportMessage returns nothing, so a platform that ignores it is only caught by reading back. */
    @Test
    fun `a platform that accepts and keeps nothing is a failure`() {
        val outcome = SupportMessageManager(Fake(ignoreSet = true)).apply("Tageslimit erreicht")
        assertNotNull(outcome.failure)
    }

    @Test
    fun `a message past the platform's length is cut here, not by the platform`() {
        val gateway = Fake()
        SupportMessageManager(gateway).apply("x".repeat(500))
        assertEquals(SupportMessageManager.MAX_CHARS, gateway.stored?.length)
    }
}
