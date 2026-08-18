package io.github.helios57.familyguard.policy

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * @param locksImmediately whether the keyguard is up by the time the call returns. On real hardware
 * it is raised asynchronously, so false is the normal case rather than a failure.
 */
private class FakeLockGateway(
    private val secure: Boolean = true,
    private var locked: Boolean = false,
    private val locksImmediately: Boolean = true,
    private val throwOn: Set<String> = emptySet(),
) : LockGateway {
    var lockCalls = 0
        private set

    private fun maybeThrow(call: String) {
        if (call in throwOn) throw SecurityException("$call is not available")
    }

    override fun lockNow() {
        lockCalls++
        maybeThrow("lockNow")
        if (locksImmediately) locked = true
    }

    override fun deviceSecure(): Boolean {
        maybeThrow("deviceSecure")
        return secure
    }

    override fun deviceLocked(): Boolean {
        maybeThrow("deviceLocked")
        return locked
    }
}

class LockManagerTest {

    @Test
    fun `locks a phone that is unlocked and has a credential`() {
        val gateway = FakeLockGateway(secure = true, locked = false)

        val outcome = LockManager(gateway).lock()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("locked", outcome.summary)
        assertNull(outcome.note)
        assertEquals(1, gateway.lockCalls)
    }

    @Test
    fun `a phone with no PIN reports a failure rather than a lock that does not hold`() {
        val gateway = FakeLockGateway(secure = false, locked = false)

        val outcome = LockManager(gateway).lock()

        assertFalse(outcome.toString(), outcome.ok)
        val failure = requireNotNull(outcome.failure) { "expected a failure, got $outcome" }
        assertTrue(failure, failure.contains("swipe"))
        // Still locked: the screen going off is worth something, and the parent is told exactly what
        // it is worth. A console showing "locked" for a phone the child swipes back into is worse
        // than one showing a failure, because a failure is actionable.
        assertEquals(1, gateway.lockCalls)
    }

    @Test
    fun `it does not re-lock a phone that is already locked`() {
        val gateway = FakeLockGateway(secure = true, locked = true)

        val outcome = LockManager(gateway).lock()

        assertTrue(outcome.toString(), outcome.ok)
        assertEquals("already locked", outcome.summary)
        // The parent lock is a standing flag applied on every sync. Calling lockNow each time blanks
        // the screen of a phone sitting in a bag, every few minutes, for no gain.
        assertEquals(0, gateway.lockCalls)
    }

    @Test
    fun `a keyguard that has not come up yet is a note, not a failure`() {
        val gateway = FakeLockGateway(secure = true, locked = false, locksImmediately = false)

        val outcome = LockManager(gateway).lock()

        // The keyguard is raised asynchronously, so "not yet" and "did not work" are the same read.
        // Calling it a failure makes a working lock report a problem on some hardware and not others
        // — and a problem is what stops the device claiming its policy version.
        assertTrue(outcome.toString(), outcome.ok)
        val note = requireNotNull(outcome.note) { "expected a note, got $outcome" }
        assertTrue(note, note.contains("not come up yet"))
    }

    @Test
    fun `a credential state that cannot be read is carried as a note, never as a guarantee`() {
        val gateway = FakeLockGateway(throwOn = setOf("deviceSecure"))

        val outcome = LockManager(gateway).lock()

        // Neither secure nor insecure. A guarantee that could not be checked must not be reported as
        // one that holds, and it must not be reported as one that fails.
        assertTrue(outcome.toString(), outcome.ok)
        val note = requireNotNull(outcome.note) { "expected a note, got $outcome" }
        assertTrue(note, note.contains("could not be read"))
        assertEquals(1, gateway.lockCalls)
    }

    @Test
    fun `a lockNow that throws is a failure`() {
        val gateway = FakeLockGateway(secure = true, throwOn = setOf("lockNow"))

        val outcome = LockManager(gateway).lock()

        assertFalse(outcome.toString(), outcome.ok)
        val failure = requireNotNull(outcome.failure) { "expected a failure, got $outcome" }
        assertTrue(failure, failure.contains("lockNow"))
    }

    @Test
    fun `an unreadable lock state does not stop the lock from being attempted`() {
        val gateway = FakeLockGateway(secure = true, throwOn = setOf("deviceLocked"))

        val outcome = LockManager(gateway).lock()

        // "Cannot tell whether it is locked" must not be read as "it is locked" — that is the branch
        // that silently skips the one call this whole command exists to make.
        assertEquals(1, gateway.lockCalls)
        assertEquals("locked", outcome.summary)
    }
}
