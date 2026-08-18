package io.github.helios57.familyguard.recovery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The flag that keeps a recovered device released until the control plane is reached again
 * (FR-12.2).
 *
 * Two properties, and both of them are about *not* doing something:
 *
 * **No expiry.** A parent who recovers a phone in a car park has no way to know when it will next
 * see the server. A release that timed out would be the same brick with a delay, and would do it
 * while nobody was looking at the phone.
 *
 * **No re-stamping.** A second code entered while already released must not move the start, because
 * the start is what the console reports as "unmanaged since" — the number a parent uses to decide
 * whether the phone has been out of management for ten minutes or three days.
 */
class RecoveryModeTest {

    private var clock = FIRST
    private val store = InMemoryRecoveryModeStore()
    private val mode = RecoveryMode(store) { clock }

    @Test
    fun `a device that has not been recovered is not released`() {
        assertFalse(mode.active())
        assertNull(mode.activeSince())
    }

    @Test
    fun `activating records when it happened`() {
        mode.activate()

        assertTrue(mode.active())
        assertEquals(FIRST, mode.activeSince())
    }

    /** The console reports this number as "unmanaged since"; a later code must not move it. */
    @Test
    fun `a second code does not move the moment the release started`() {
        mode.activate()
        clock = LATER

        mode.activate()

        assertEquals(FIRST, mode.activeSince())
    }

    @Test
    fun `clearing ends the release`() {
        mode.activate()

        mode.clear()

        assertFalse(mode.active())
        assertNull(mode.activeSince())
    }

    /**
     * `Synchronizer` clears on every policy that came from the server, which for a managed phone is
     * every fifteen minutes for as long as it runs. Clearing what is already clear must be free and
     * must not write.
     */
    @Test
    fun `clearing a device that was never released writes nothing`() {
        val counting = CountingStore()

        RecoveryMode(counting) { clock }.clear()

        assertEquals("a no-op clear wrote to the store", 0, counting.writes)
    }

    /**
     * A release outlives the process. It is ended by reaching the server, and by nothing else — not
     * by a reboot, and not by the fifteen-minute alarm that restarts the service.
     */
    @Test
    fun `a restart finds the device still released`() {
        mode.activate()
        clock = LATER

        val afterRestart = RecoveryMode(store) { clock }

        assertTrue(afterRestart.active())
        assertEquals(FIRST, afterRestart.activeSince())
    }

    /** Counts writes, so "idempotent" can mean what it says rather than "ends in the same state". */
    private class CountingStore : RecoveryModeStore {
        private var since: Long? = null
        var writes = 0
            private set

        override fun activeSince(): Long? = since
        override fun setActiveSince(epochMillis: Long?) {
            writes++
            since = epochMillis
        }
    }

    private companion object {
        const val FIRST = 1_755_000_000_000L
        const val LATER = FIRST + 3 * 60 * 60 * 1000L
    }
}
